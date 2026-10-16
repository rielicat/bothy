package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/rielicat/bothy/internal/core"
)

// discordAPI is the slice of the Discord REST API the adapter uses. The
// production implementation is disgo's client; tests substitute fakes.
type discordAPI interface {
	CurrentUser(ctx context.Context) (discord.User, error)
	GatewayURL(ctx context.Context) (string, error)
	// ThreadParent returns the parent channel of a thread, or 0 when id is
	// not a thread.
	ThreadParent(ctx context.Context, id snowflake.ID) (snowflake.ID, error)
	CreateMessage(ctx context.Context, channel snowflake.ID, m discord.MessageCreate) (snowflake.ID, error)
	UpdateMessage(ctx context.Context, channel, msg snowflake.ID, m discord.MessageUpdate) error
	DeleteMessage(ctx context.Context, channel, msg snowflake.ID) error
	Typing(ctx context.Context, channel snowflake.ID) error
	OpenDM(ctx context.Context, user snowflake.ID) (snowflake.ID, error)
	DeferUpdate(ctx context.Context, interaction snowflake.ID, token string) error
	Followup(ctx context.Context, app snowflake.ID, token string, m discord.MessageCreate) error
}

const (
	userAgent = "DiscordBot (https://github.com/rielicat/bothy, 0.1)"
	// maxRetries bounds disgo's per-route 429 retries so a rate-limited edit
	// cannot hold the streamer for long.
	maxRetries = 3
)

func (a *adapter) client() discordAPI {
	a.apiMu.Lock()
	defer a.apiMu.Unlock()
	if a.api == nil {
		set := a.conf()
		rc := rest.NewClient("",
			rest.WithURL(set.cfg.APIBase),
			rest.WithHTTPClient(a.egressClient(hostPort(set.apiBase))),
			rest.WithUserAgent(userAgent),
			rest.WithLogger(a.libLog),
			rest.WithRateLimiter(sharedLimiter(set.cfg.APIBase, a.libLog)),
		)
		a.api = &disgoAPI{r: rest.New(rc, rest.WithDefaultAllowedMentions(*allowedMentions(false))), token: a.token}
	}
	return a.api
}

// limiters holds one REST rate limiter per API base for the whole process.
// disgo starts a cleanup goroutine with every limiter that nothing ever stops
// (its Close does not), and a client is rebuilt whenever api_base changes,
// so a limiter per client would leak one goroutine per change.
var limiters struct {
	mu sync.Mutex
	m  map[string]rest.RateLimiter
}

func sharedLimiter(base string, log *slog.Logger) rest.RateLimiter {
	limiters.mu.Lock()
	defer limiters.mu.Unlock()
	if rl, ok := limiters.m[base]; ok {
		return rl
	}
	if limiters.m == nil {
		limiters.m = map[string]rest.RateLimiter{}
	}
	rl := rest.NewRateLimiter(rest.WithMaxRetries(maxRetries), rest.WithRateLimiterLogger(log))
	limiters.m[base] = rl
	return rl
}

// token returns the bot token, resolving and caching the secret reference.
func (a *adapter) token(ctx context.Context) (string, error) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if !a.tok.IsZero() {
		return a.tok.Reveal(), nil
	}
	ref := a.conf().cfg.Token
	if ref == "" {
		return "", &authError{hint: "no bot token configured (channels.discord.token)"}
	}
	s, err := a.deps.Secret(ctx, ref)
	switch {
	case errors.Is(err, core.ErrSealed):
		return "", fmt.Errorf("discord: bot token unavailable: %w", err)
	case err != nil:
		return "", &authError{hint: "cannot resolve the bot token (channels.discord.token)", err: err}
	case s.IsZero():
		return "", &authError{hint: "the bot token (channels.discord.token) is empty"}
	}
	a.tok = s
	return s.Reveal(), nil
}

// forgetToken drops the cached token so the next call re-resolves it, which
// picks up a token fixed in the vault after an auth failure.
func (a *adapter) forgetToken() {
	a.tokMu.Lock()
	a.tok = core.Secret{}
	a.tokMu.Unlock()
}

// apiErr wraps a REST error, turning 401 into an authError.
func (a *adapter) apiErr(op string, err error) error {
	var re *rest.Error
	if errors.As(err, &re) && re.Response != nil && re.Response.StatusCode == http.StatusUnauthorized {
		a.forgetToken()
		return &authError{hint: "Discord rejected the bot token (channels.discord.token)", err: err}
	}
	return fmt.Errorf("discord: %s: %w", op, err)
}

func statusCode(err error) int {
	var re *rest.Error
	if errors.As(err, &re) && re.Response != nil {
		return re.Response.StatusCode
	}
	return 0
}

// authError marks failures that retrying with the same credentials or
// settings cannot fix; Status reports them as auth_failed with hint.
type authError struct {
	hint string
	err  error
}

func (e *authError) Error() string {
	if e.err == nil {
		return e.hint
	}
	return e.hint + ": " + e.err.Error()
}

func (e *authError) Unwrap() error { return e.err }

func (e *authError) FailureCode() core.FailureCode { return core.FailKeyRejected }

type disgoAPI struct {
	r     rest.Rest
	token func(context.Context) (string, error)
}

func (d *disgoAPI) bot(ctx context.Context) ([]rest.RequestOpt, error) {
	tok, err := d.token(ctx)
	if err != nil {
		return nil, err
	}
	return []rest.RequestOpt{rest.WithCtx(ctx), rest.WithToken(discord.TokenTypeBot, tok)}, nil
}

func (d *disgoAPI) CurrentUser(ctx context.Context) (discord.User, error) {
	opts, err := d.bot(ctx)
	if err != nil {
		return discord.User{}, err
	}
	var u discord.OAuth2User
	err = d.r.Do(rest.GetCurrentUser.Compile(nil), nil, &u, opts...)
	return u.User, err
}

func (d *disgoAPI) GatewayURL(ctx context.Context) (string, error) {
	opts, err := d.bot(ctx)
	if err != nil {
		return "", err
	}
	gb, err := d.r.GetGatewayBot(opts...)
	if err != nil {
		return "", err
	}
	return gb.URL, nil
}

func (d *disgoAPI) ThreadParent(ctx context.Context, id snowflake.ID) (snowflake.ID, error) {
	opts, err := d.bot(ctx)
	if err != nil {
		return 0, err
	}
	ch, err := d.r.GetChannel(id, opts...)
	if err != nil {
		return 0, err
	}
	if t, ok := ch.(discord.GuildThread); ok {
		return *t.ParentID(), nil
	}
	return 0, nil
}

func (d *disgoAPI) CreateMessage(ctx context.Context, channel snowflake.ID, m discord.MessageCreate) (snowflake.ID, error) {
	opts, err := d.bot(ctx)
	if err != nil {
		return 0, err
	}
	msg, err := d.r.CreateMessage(channel, m, opts...)
	if err != nil {
		return 0, err
	}
	return msg.ID, nil
}

func (d *disgoAPI) UpdateMessage(ctx context.Context, channel, msg snowflake.ID, m discord.MessageUpdate) error {
	opts, err := d.bot(ctx)
	if err != nil {
		return err
	}
	_, err = d.r.UpdateMessage(channel, msg, m, opts...)
	return err
}

func (d *disgoAPI) DeleteMessage(ctx context.Context, channel, msg snowflake.ID) error {
	opts, err := d.bot(ctx)
	if err != nil {
		return err
	}
	return d.r.DeleteMessage(channel, msg, opts...)
}

func (d *disgoAPI) Typing(ctx context.Context, channel snowflake.ID) error {
	opts, err := d.bot(ctx)
	if err != nil {
		return err
	}
	return d.r.SendTyping(channel, opts...)
}

func (d *disgoAPI) OpenDM(ctx context.Context, user snowflake.ID) (snowflake.ID, error) {
	opts, err := d.bot(ctx)
	if err != nil {
		return 0, err
	}
	ch, err := d.r.CreateDMChannel(user, opts...)
	if err != nil {
		return 0, err
	}
	return ch.ID(), nil
}

// Interaction callbacks and followups authenticate with the interaction
// token in the path, so they carry no bot token.

func (d *disgoAPI) DeferUpdate(ctx context.Context, interaction snowflake.ID, token string) error {
	return hideURL(d.r.CreateInteractionResponse(interaction, token,
		discord.InteractionResponse{Type: discord.InteractionResponseTypeDeferredUpdateMessage}, rest.WithCtx(ctx)))
}

func (d *disgoAPI) Followup(ctx context.Context, app snowflake.ID, token string, m discord.MessageCreate) error {
	_, err := d.r.CreateFollowupMessage(app, token, m, rest.WithCtx(ctx))
	return hideURL(err)
}
