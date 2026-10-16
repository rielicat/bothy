package slack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/rielicat/bothy/internal/core"
)

// slackAPI is the slice of the Web API the adapter calls; *slack.Client
// implements it and tests may substitute fakes.
type slackAPI interface {
	AuthTestContext(ctx context.Context) (*slack.AuthTestResponse, error)
	PostMessageContext(ctx context.Context, channelID string, options ...slack.MsgOption) (string, string, error)
	UpdateMessageContext(ctx context.Context, channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error)
	DeleteMessageContext(ctx context.Context, channel, messageTimestamp string) (string, string, error)
	OpenConversationContext(ctx context.Context, params *slack.OpenConversationParameters) (*slack.Channel, bool, bool, error)
	GetUserInfoContext(ctx context.Context, user string, options ...slack.GetUserInfoOption) (*slack.User, error)
}

// socket is the Socket Mode connection: slack-go's socketmode client in
// production, substitutable in tests.
type socket interface {
	Run(ctx context.Context) error
	Events() <-chan socketmode.Event
	Ack(ctx context.Context, envelopeID string) error
}

type smSocket struct{ c *socketmode.Client }

func newSocketMode(api *slack.Client, opts ...socketmode.Option) socket {
	return smSocket{socketmode.New(api, opts...)}
}

func (s smSocket) Run(ctx context.Context) error { return s.c.RunContext(ctx) }

func (s smSocket) Events() <-chan socketmode.Event { return s.c.Events }

func (s smSocket) Ack(ctx context.Context, envelopeID string) error {
	return s.c.AckCtx(ctx, envelopeID, nil)
}

const (
	botKey = "channels.slack.bot_token"
	appKey = "channels.slack.app_token"
)

// authCodes are the Web API errors no retry with the same token can fix.
var authCodes = map[string]bool{
	"invalid_auth": true, "not_authed": true, "account_inactive": true, "token_revoked": true,
	"token_expired": true, "not_allowed_token_type": true,
}

// newClient builds a Web API client; app is empty unless Socket Mode needs
// it. 429s are retried after Retry-After, at most twice.
func (a *adapter) newClient(bot, app string) *slack.Client {
	set := a.conf()
	opts := []slack.Option{
		slack.OptionAPIURL(set.cfg.APIBase),
		slack.OptionHTTPClient(a.egressClient(hostPort(set.apiBase))),
		slack.OptionRetryConfig(slack.RetryConfig{MaxRetries: 2, RetryAfterDuration: 5 * time.Second}),
	}
	if app != "" {
		opts = append(opts, slack.OptionAppLevelToken(app))
	}
	return slack.New(bot, opts...)
}

// client returns the Web API client for the current bot token.
func (a *adapter) client(ctx context.Context) (slackAPI, error) {
	a.apiMu.Lock()
	defer a.apiMu.Unlock()
	if a.api != nil {
		return a.api, nil
	}
	bot, err := a.secret(ctx, &a.bot, a.conf().cfg.BotToken, botKey, "xoxb-")
	if err != nil {
		return nil, err
	}
	a.api = a.newClient(bot, "")
	return a.api, nil
}

// secret resolves and caches a token reference, checking its Slack prefix.
func (a *adapter) secret(ctx context.Context, cache *core.Secret, ref, key, prefix string) (string, error) {
	a.tokMu.Lock()
	defer a.tokMu.Unlock()
	if !cache.IsZero() {
		return cache.Reveal(), nil
	}
	if ref == "" {
		return "", &authError{hint: "no token configured (" + key + ")"}
	}
	s, err := a.deps.Secret(ctx, ref)
	switch {
	case errors.Is(err, core.ErrSealed):
		return "", fmt.Errorf("slack: %s unavailable: %w", key, err)
	case err != nil:
		return "", &authError{hint: "cannot resolve " + key, err: err}
	case !strings.HasPrefix(s.Reveal(), prefix):
		return "", &authError{hint: key + " must be a " + prefix + "… token"}
	}
	*cache = s
	return s.Reveal(), nil
}

// forgetTokens drops cached tokens and the client built from them, so a
// token fixed in the vault is picked up by the next call.
func (a *adapter) forgetTokens() {
	a.tokMu.Lock()
	a.bot, a.app = core.Secret{}, core.Secret{}
	a.tokMu.Unlock()
	a.apiMu.Lock()
	a.api = nil
	a.apiMu.Unlock()
}

// apiErr wraps a Web API error; token errors become authErrors.
func (a *adapter) apiErr(op, key string, err error) error {
	var se slack.SlackErrorResponse
	if errors.As(err, &se) && authCodes[se.Err] {
		a.forgetTokens()
		return &authError{hint: "Slack rejected " + key + " (" + se.Err + ")", err: err}
	}
	if _, ok := errors.AsType[*authError](err); ok {
		return err
	}
	return fmt.Errorf("slack: %s: %w", op, err)
}

func slackCode(err error) string {
	var se slack.SlackErrorResponse
	if errors.As(err, &se) {
		return se.Err
	}
	return ""
}

// authError marks failures that retrying with the same tokens or settings
// cannot fix; Status reports them as auth_failed with hint.
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
