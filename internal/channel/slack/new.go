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

// settings is what Reconfigure replaces while requests may be running, so
// every read goes through conf.
type settings struct {
	cfg         config.Slack
	apiBase     *url.URL
	privateHost string
}

type adapter struct {
	set       atomic.Pointer[settings]
	deps      core.AdapterDeps
	log       *slog.Logger
	clock     core.Clock
	newSocket func(*slack.Client, ...socketmode.Option) socket

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
	// selfTeam, selfEnterprise and teamName come from auth.test: who counts
	// as a member (CheckMember) and what the manage page calls the team.
	selfTeam       string
	selfEnterprise string
	teamName       string
	// appID comes from the Socket Mode hello, for links to the app's
	// settings page.
	appID string
	// scopeWarned is when missing channel scopes were last logged.
	scopeWarned time.Time

	names    nameCache // users: display name and handle
	channels nameCache // channels: "#name" as display
	members  memberCache
	inflight flights
	dropped  atomic.Int64
}

// New returns the adapter; it performs no network I/O until Run.
func New(cfg config.Slack, d core.AdapterDeps) (core.Channel, error) {
	return newAdapter(cfg, d)
}

func newAdapter(cfg config.Slack, d core.AdapterDeps) (*adapter, error) {
	if d.Secret == nil || d.HTTP == nil {
		return nil, fmt.Errorf("slack: AdapterDeps.Secret and AdapterDeps.HTTP are required: %w", core.ErrInvalid)
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
		deps:      d,
		log:       log,
		clock:     clock,
		newSocket: newSocketMode,
		state:     stateConnecting,
		since:     clock.Now(),
	}
	a.set.Store(set)
	if !cfg.Enabled {
		a.state = stateDisabled
	}
	return a, nil
}

func newSettings(cfg config.Slack) (*settings, error) {
	if !strings.HasSuffix(cfg.APIBase, "/") {
		cfg.APIBase += "/"
	}
	api, err := url.Parse(cfg.APIBase)
	if err != nil || (api.Scheme != "https" && api.Scheme != "http") || api.Host == "" {
		return nil, fmt.Errorf("slack: channels.slack.api_base %q is not an http(s) URL: %w", cfg.APIBase, core.ErrInvalid)
	}
	s := &settings{cfg: cfg, apiBase: api}
	// api_base is locked (file or env only), so an override is admin-set,
	// which is what AllowPrivate requires; it is how a self-hosted proxy or
	// the test fake on loopback is reached. Hosts learned at run time (the
	// Socket Mode URL, file and response URLs) never get it.
	if cfg.APIBase != config.Default().Channels.Slack.APIBase {
		s.privateHost = strings.ToLower(hostPort(api))
	}
	return s, nil
}

func (a *adapter) conf() *settings { return a.set.Load() }

// Reconfigure takes a new config snapshot; the hub restarts Run afterwards.
// Cached tokens go too: a token saved again under the same reference must
// be picked up.
func (a *adapter) Reconfigure(c *config.Config) error {
	set, err := newSettings(c.Channels.Slack)
	if err != nil {
		return err
	}
	a.set.Store(set)
	a.forgetTokens()
	if set.cfg.Enabled {
		a.setState(stateConnecting, "")
	} else {
		a.setState(stateDisabled, "")
	}
	return nil
}

func (a *adapter) Name() string { return name }

func (a *adapter) Caps() core.ChannelCaps { return caps }

func (a *adapter) Status() core.ChannelStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	return core.ChannelStatus{Name: name, Enabled: a.conf().cfg.Enabled, State: a.state, Detail: a.detail, Identity: a.identity, Since: a.since,
		Team: a.teamName, Org: a.selfEnterprise != "", AppID: a.appID}
}

func (a *adapter) setState(state, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == state && a.detail == detail {
		return
	}
	a.state, a.detail, a.since = state, detail, a.clock.Now()
}

func (a *adapter) setSelf(auth *slack.AuthTestResponse) {
	a.mu.Lock()
	moved := a.selfTeam != auth.TeamID || a.selfEnterprise != auth.EnterpriseID
	a.selfID, a.selfBot = auth.UserID, auth.BotID
	a.selfTeam, a.selfEnterprise, a.teamName = auth.TeamID, auth.EnterpriseID, auth.Team
	if auth.User != "" {
		a.identity = "@" + auth.User
	}
	a.mu.Unlock()
	if moved {
		a.members.clear()
	}
}

func (a *adapter) self() (userID, botID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.selfID, a.selfBot
}
