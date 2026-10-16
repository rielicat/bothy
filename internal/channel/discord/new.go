package discord

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const name = "discord"

const (
	// contentLimit is Discord's hard limit on message content. Caps.MaxLen
	// stays below it so the backslashes render adds still fit.
	contentLimit = 2000
	maxLen       = 1900

	intents = gateway.IntentGuildMessages | gateway.IntentDirectMessages | gateway.IntentMessageContent
)

const (
	stateConnecting = "connecting"
	stateConnected  = "connected"
	stateBackoff    = "backoff"
	stateAuthFailed = "auth_failed"
	stateDisabled   = "disabled"
)

var caps = core.ChannelCaps{Edit: true, Buttons: true, Threads: true, Typing: true, MaxLen: maxLen, EditMinInterval: time.Second}

// settings is what Reconfigure replaces while requests may be running, so
// every read goes through conf.
type settings struct {
	cfg          config.Discord
	apiBase      *url.URL
	privateHosts map[string]bool
}

type adapter struct {
	set        atomic.Pointer[settings]
	deps       core.AdapterDeps
	log        *slog.Logger
	libLog     *slog.Logger
	clock      core.Clock
	newGateway gateway.CreateFunc

	apiMu sync.Mutex
	api   discordAPI

	tokMu sync.Mutex
	tok   core.Secret

	mu       sync.Mutex
	state    string
	detail   string
	since    time.Time
	gw       gateway.Gateway
	selfID   snowflake.ID
	identity string
	resume   resumeState

	parents parentCache
	dropped atomic.Int64
}

// New returns the adapter; it performs no network I/O until Run.
func New(cfg config.Discord, d core.AdapterDeps) (core.Channel, error) {
	return newAdapter(cfg, d)
}

func newAdapter(cfg config.Discord, d core.AdapterDeps) (*adapter, error) {
	if d.Secret == nil || d.HTTP == nil {
		return nil, fmt.Errorf("discord: AdapterDeps.Secret and AdapterDeps.HTTP are required: %w", core.ErrInvalid)
	}
	set, err := newSettings(cfg)
	if err != nil {
		return nil, err
	}
	log := d.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	clock := d.Clock
	if clock == nil {
		clock = core.SystemClock{}
	}
	a := &adapter{
		deps:       d,
		log:        log,
		libLog:     slog.New(quietHandler{log.Handler()}),
		clock:      clock,
		newGateway: gateway.New,
		state:      stateConnecting,
		since:      clock.Now(),
	}
	a.set.Store(set)
	if !cfg.Enabled {
		a.state = stateDisabled
	}
	return a, nil
}

func newSettings(cfg config.Discord) (*settings, error) {
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	api, err := url.Parse(cfg.APIBase)
	if err != nil || (api.Scheme != "https" && api.Scheme != "http") || api.Host == "" {
		return nil, fmt.Errorf("discord: channels.discord.api_base %q is not an http(s) URL: %w", cfg.APIBase, core.ErrInvalid)
	}
	// The api_base and gateway_url keys are locked (file or env only), so an
	// override is admin-set, which is what AllowPrivate requires; it is how a
	// self-hosted proxy or the test fakes on loopback are reached. Hosts
	// learned at run time (GET /gateway/bot, resume URLs, CDN links) never
	// get it.
	private := map[string]bool{}
	if cfg.APIBase != strings.TrimRight(config.Default().Channels.Discord.APIBase, "/") {
		private[strings.ToLower(hostPort(api))] = true
	}
	if cfg.GatewayURL != "" {
		gw, err := url.Parse(cfg.GatewayURL)
		if err != nil || (gw.Scheme != "wss" && gw.Scheme != "ws") || gw.Host == "" {
			return nil, fmt.Errorf("discord: channels.discord.gateway_url %q is not a ws(s) URL: %w", cfg.GatewayURL, core.ErrInvalid)
		}
		private[strings.ToLower(hostPort(gw))] = true
	}
	return &settings{cfg: cfg, apiBase: api, privateHosts: private}, nil
}

func (a *adapter) conf() *settings { return a.set.Load() }

// Reconfigure takes a new config snapshot; the hub restarts Run afterwards.
// The cached token goes too: a token saved again under the same reference
// must be picked up.
func (a *adapter) Reconfigure(c *config.Config) error {
	set, err := newSettings(c.Channels.Discord)
	if err != nil {
		return err
	}
	if old := a.set.Swap(set); old.cfg.APIBase != set.cfg.APIBase {
		a.apiMu.Lock()
		a.api = nil
		a.apiMu.Unlock()
	}
	a.forgetToken()
	if set.cfg.Enabled {
		a.setState(stateConnecting, "")
	} else {
		a.setState(stateDisabled, "")
	}
	return nil
}

func (a *adapter) Name() string { return name }

func (a *adapter) Caps() core.ChannelCaps { return caps }

// DeepLink is unsupported: Discord has no start-parameter links for bots.
func (a *adapter) DeepLink(string) string { return "" }

func (a *adapter) Status() core.ChannelStatus {
	a.mu.Lock()
	st := core.ChannelStatus{Name: name, Enabled: a.conf().cfg.Enabled, State: a.state, Detail: a.detail, Identity: a.identity, Since: a.since}
	gw := a.gw
	a.mu.Unlock()
	// Between sessions disgo reconnects on its own; the gateway's status is
	// the truth while it does.
	if st.State == stateConnected && gw != nil && gw.Status() != gateway.StatusReady {
		st.State = stateConnecting
	}
	return st
}

func (a *adapter) setState(state, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == state && a.detail == detail {
		return
	}
	a.state, a.detail, a.since = state, detail, a.clock.Now()
}

func (a *adapter) setSelf(id snowflake.ID, username string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.selfID = id
	if username != "" {
		a.identity = "@" + username
	}
}

func (a *adapter) self() snowflake.ID {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.selfID
}

func (a *adapter) setGateway(gw gateway.Gateway) {
	a.mu.Lock()
	a.gw = gw
	a.mu.Unlock()
}
