package slack

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const name = "slack"

const (
	// maxLen is the section block text limit, so a card's text fits one
	// section; longer rendered text is split across sections.
	maxLen = 3000

	stateConnecting = "connecting"
	stateConnected  = "connected"
	stateBackoff    = "backoff"
	stateAuthFailed = "auth_failed"
	stateDisabled   = "disabled"
)

// Socket Mode has no typing indicator for bots, so Caps.Typing is off.
var caps = core.ChannelCaps{Edit: true, Buttons: true, Threads: true, MaxLen: maxLen, EditMinInterval: 1200 * time.Millisecond}

type adapter struct {
	cfg         config.Slack
	deps        core.AdapterDeps
	log         *slog.Logger
	clock       core.Clock
	apiBase     *url.URL
	privateHost string
	newSocket   func(*slack.Client, ...socketmode.Option) socket

	tokMu sync.Mutex
	bot   core.Secret
	app   core.Secret

	apiMu sync.Mutex
	api   slackAPI

	mu       sync.Mutex
	state    string
	detail   string
	since    time.Time
	selfID   string
	selfBot  string
	identity string

	names   nameCache
	dropped atomic.Int64
}

// New returns the adapter; it performs no network I/O until Run.
func New(cfg config.Slack, d core.AdapterDeps) (core.Channel, error) {
	return newAdapter(cfg, d)
}

func newAdapter(cfg config.Slack, d core.AdapterDeps) (*adapter, error) {
	if d.Secret == nil || d.HTTP == nil {
		return nil, fmt.Errorf("slack: AdapterDeps.Secret and AdapterDeps.HTTP are required: %w", core.ErrInvalid)
	}
	if !strings.HasSuffix(cfg.APIBase, "/") {
		cfg.APIBase += "/"
	}
	api, err := url.Parse(cfg.APIBase)
	if err != nil || (api.Scheme != "https" && api.Scheme != "http") || api.Host == "" {
		return nil, fmt.Errorf("slack: channels.slack.api_base %q is not an http(s) URL: %w", cfg.APIBase, core.ErrInvalid)
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
		cfg:       cfg,
		deps:      d,
		log:       log,
		clock:     clock,
		apiBase:   api,
		newSocket: newSocketMode,
		state:     stateConnecting,
		since:     clock.Now(),
	}
	// api_base is locked (file or env only), so an override is admin-set,
	// which is what AllowPrivate requires; it is how a self-hosted proxy or
	// the test fake on loopback is reached. Hosts learned at run time (the
	// Socket Mode URL, file and response URLs) never get it.
	if cfg.APIBase != config.Default().Channels.Slack.APIBase {
		a.privateHost = strings.ToLower(hostPort(api))
	}
	if !cfg.Enabled {
		a.state = stateDisabled
	}
	return a, nil
}

func (a *adapter) Name() string { return name }

func (a *adapter) Caps() core.ChannelCaps { return caps }

// DeepLink is unsupported: Slack has no start-parameter links for apps.
func (a *adapter) DeepLink(string) string { return "" }

func (a *adapter) Status() core.ChannelStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return core.ChannelStatus{Name: name, Enabled: a.cfg.Enabled, State: a.state, Detail: a.detail, Identity: a.identity, Since: a.since}
}

func (a *adapter) setState(state, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == state && a.detail == detail {
		return
	}
	a.state, a.detail, a.since = state, detail, a.clock.Now()
}

func (a *adapter) setSelf(userID, botID, user string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.selfID, a.selfBot = userID, botID
	if user != "" {
		a.identity = "@" + user
	}
}

func (a *adapter) self() (userID, botID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.selfID, a.selfBot
}
