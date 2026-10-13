package slack

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/channel/slack/slacktest"
	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// egressRecorder stands in for core.Egress.SystemClient: plain dialing, with
// every requested destination recorded.
type egressRecorder struct {
	mu    sync.Mutex
	dests []core.SystemDest
}

func (e *egressRecorder) client(d core.SystemDest) *http.Client {
	e.mu.Lock()
	e.dests = append(e.dests, d)
	e.mu.Unlock()
	return newPlainClient()
}

func (e *egressRecorder) recorded() []core.SystemDest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]core.SystemDest(nil), e.dests...)
}

func newPlainClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{}).DialContext}}
}

var testSecrets = map[string]string{
	"secret:slack_bot_token": slacktest.DefaultBotToken,
	"secret:slack_app_token": slacktest.DefaultAppToken,
}

type fixture struct {
	srv    *slacktest.Server
	a      *adapter
	egress *egressRecorder
	sink   *sink
}

func newFixture(t *testing.T, mutate ...func(*config.Slack, *core.AdapterDeps)) *fixture {
	t.Helper()
	srv := slacktest.New()
	t.Cleanup(srv.Close)
	f := &fixture{srv: srv, egress: &egressRecorder{}, sink: newSink()}
	cfg := config.Default().Channels.Slack
	cfg.Enabled = true
	cfg.APIBase = srv.URL()
	deps := core.AdapterDeps{
		Secret: func(_ context.Context, ref string) (core.Secret, error) {
			v, ok := testSecrets[ref]
			if !ok {
				return core.Secret{}, core.ErrNotFound
			}
			return core.NewSecret([]byte(v)), nil
		},
		HTTP:  f.egress.client,
		Clock: core.SystemClock{},
		Log:   slog.New(slog.DiscardHandler),
	}
	for _, m := range mutate {
		m(&cfg, &deps)
	}
	a, err := newAdapter(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	f.a = a
	return f
}

// start runs the adapter until the test ends and waits for want state.
func (f *fixture) start(t *testing.T, want string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.a.Run(ctx, f.sink) }()
	stop := sync.OnceFunc(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("Run did not return after cancel")
		}
	})
	t.Cleanup(stop)
	waitFor(t, 10*time.Second, func() bool { return f.a.Status().State == want }, "state "+want)
	return stop
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// sink records deliveries; while gate is held, OnMessage blocks as a busy
// hub would.
type sink struct {
	msgs chan core.Inbound
	acts chan core.ActionEvent
	gate sync.RWMutex
}

func newSink() *sink {
	return &sink{msgs: make(chan core.Inbound, 64), acts: make(chan core.ActionEvent, 64)}
}

func (s *sink) OnMessage(_ context.Context, in core.Inbound) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	s.msgs <- in
}

func (s *sink) OnAction(_ context.Context, ev core.ActionEvent) { s.acts <- ev }

func (s *sink) message(t *testing.T) core.Inbound {
	t.Helper()
	select {
	case in := <-s.msgs:
		return in
	case <-time.After(5 * time.Second):
		t.Fatal("no inbound message")
		return core.Inbound{}
	}
}

func (s *sink) action(t *testing.T) core.ActionEvent {
	t.Helper()
	select {
	case ev := <-s.acts:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no action event")
		return core.ActionEvent{}
	}
}

func isAuthErr(err error) bool {
	_, ok := errors.AsType[*authError](err)
	return ok
}
