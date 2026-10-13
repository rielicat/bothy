package channel

import (
	"cmp"
	"context"
	"fmt"
	"reflect"
	"runtime/debug"
	"slices"
	"sync/atomic"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// Reconfigurer is implemented by adapters whose settings can change without
// a new instance. cmd/bothy builds each adapter once at boot; when its
// channels.<name> section changes, the hub passes the new snapshot here and
// then restarts the adapter's Run. Adapters satisfy it structurally, since
// they never import this package.
type Reconfigurer interface {
	Reconfigure(c *config.Config) error
}

// ChatTitler is implemented by adapters that remember group titles from the
// updates they have seen; the hub names a newly sighted space with it.
type ChatTitler interface {
	ChatTitle(chatID string) string
}

const (
	inboundBuffer        = 256
	maxConcurrentActions = 16
	stopTimeout          = 5 * time.Second
	maxRestartBackoff    = 5 * time.Minute
)

type adapter struct {
	name    string
	ch      core.Channel
	queue   chan core.Inbound
	dropped atomic.Uint64

	// Guarded by Hub.mu.
	dispatching bool
	cancel      context.CancelFunc
	done        chan struct{}
	section     any
	sectionSet  bool
	published   core.ChannelStatus
}

func (h *Hub) Add(ch core.Channel) {
	if ch == nil {
		return
	}
	name := ch.Name()
	h.mu.Lock()
	if _, dup := h.adapters[name]; dup || name == "" {
		h.mu.Unlock()
		h.log.Warn("channel adapter ignored: empty or duplicate name", "channel", name)
		return
	}
	a := &adapter{name: name, ch: ch, queue: make(chan core.Inbound, inboundBuffer)}
	h.adapters[name] = a
	live := h.running && !h.stopped
	if live {
		h.startDispatcherLocked(a)
	}
	h.mu.Unlock()
	if live {
		h.requestReconcile()
	}
}

// Run starts the enabled adapters, delivers approval cards and follows config
// changes until ctx ends; then it stops every adapter and in-flight delivery.
func (h *Hub) Run(ctx context.Context) error {
	h.mu.Lock()
	if h.running || h.stopped {
		h.mu.Unlock()
		return errRunTwice
	}
	h.running, h.runCtx = true, ctx
	for _, a := range h.adapters {
		h.startDispatcherLocked(a)
	}
	h.mu.Unlock()

	sub := h.d.Bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvApprovalRequested, core.EvApprovalResolved}}, 256)
	stopWatch := h.d.Config.Watch(func(_, _ *config.Config) { h.requestReconcile() })
	h.reconcile()
	h.contain("approval card resync", h.resyncCards)
	h.contain("channel status", h.publishStatuses)

	tick := time.NewTicker(h.statusEvery)
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case e := <-sub.C():
			h.onBusEvent(e)
		case <-h.reconfig:
			h.reconcile()
		case <-tick.C:
			if sub.Dropped() > 0 {
				h.contain("approval card resync", h.resyncCards)
			}
			h.contain("channel status", h.publishStatuses)
			h.prune()
		}
	}
	tick.Stop()
	stopWatch()
	sub.Close()
	h.shutdown()
	return nil
}

func (h *Hub) requestReconcile() {
	select {
	case h.reconfig <- struct{}{}:
	default:
	}
}

func (h *Hub) startDispatcherLocked(a *adapter) {
	if a.dispatching {
		return
	}
	a.dispatching = true
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		h.dispatch(a)
	}()
}

// wantState reports whether config enables the adapter and the config section
// that, when it changes, requires a restart. known is false for adapters this
// hub has no config section for (they manage their own enablement).
func wantState(c *config.Config, a *adapter) (enabled bool, section any, known bool) {
	switch a.name {
	case "telegram":
		return c.Channels.Telegram.Enabled, c.Channels.Telegram, true
	case "discord":
		return c.Channels.Discord.Enabled, c.Channels.Discord, true
	case "slack":
		return c.Channels.Slack.Enabled, c.Channels.Slack, true
	}
	return a.ch.Status().Enabled, nil, false
}

func mentionOnlyDefault(c *config.Config, channel string) bool {
	switch channel {
	case "telegram":
		return c.Channels.Telegram.Groups.MentionOnly
	case "discord":
		return c.Channels.Discord.Groups.MentionOnly
	case "slack":
		return c.Channels.Slack.Groups.MentionOnly
	}
	return true
}

type adapterChange struct {
	a           *adapter
	reconfigure bool
	stop        context.CancelFunc
	done        chan struct{}
	start       bool
}

// reconcile brings running adapters in line with config: it stops disabled
// ones, restarts ones whose section changed and starts newly enabled ones.
func (h *Hub) reconcile() {
	cur := h.cfg()
	var changes []adapterChange
	h.mu.Lock()
	if !h.running || h.stopped {
		h.mu.Unlock()
		return
	}
	for _, a := range h.adapters {
		enabled, section, known := wantState(cur, a)
		c := adapterChange{a: a}
		differs := known && (!a.sectionSet || !reflect.DeepEqual(section, a.section))
		c.reconfigure = differs
		running := a.cancel != nil
		if running && (!enabled || differs) {
			c.stop, c.done = a.cancel, a.done
			a.cancel, a.done = nil, nil
			running = false
		}
		c.start = !running && enabled
		a.section, a.sectionSet = section, known
		if c.reconfigure || c.stop != nil || c.start {
			changes = append(changes, c)
		}
	}
	h.mu.Unlock()

	for _, c := range changes {
		if c.stop != nil {
			c.stop()
			h.waitStopped(c.a.name, c.done)
		}
		if rc, ok := c.a.ch.(Reconfigurer); ok && c.reconfigure {
			if err := rc.Reconfigure(cur); err != nil {
				h.log.Warn("channel reconfigure failed", "channel", c.a.name, "err", err)
			}
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return
	}
	for _, c := range changes {
		if c.start && c.a.cancel == nil {
			h.startAdapterLocked(c.a)
		}
	}
}

func (h *Hub) waitStopped(name string, done chan struct{}) {
	t := time.NewTimer(stopTimeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		h.log.Warn("channel adapter did not stop in time", "channel", name)
	}
}

func (h *Hub) startAdapterLocked(a *adapter) {
	actx, cancel := context.WithCancel(h.runCtx)
	done := make(chan struct{})
	a.cancel, a.done = cancel, done
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer close(done)
		h.supervise(actx, a)
	}()
}

// contain runs fn and logs a panic instead of letting it end the process.
// The hub calls into every other service on behalf of untrusted platform
// input; one bad message or a faulty service must not take the gateway down.
func (h *Hub) contain(what string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("channel: recovered from a panic", "in", what, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	fn()
}

// runAdapter runs the adapter once, turning a panic into an error so the
// supervisor restarts it with backoff.
func (h *Hub) runAdapter(ctx context.Context, a *adapter) (err error) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("channel adapter panicked", "channel", a.name, "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("channel %s: panic: %v", a.name, r)
		}
	}()
	return a.ch.Run(ctx, h)
}

// supervise keeps an adapter's Run going. Adapters reconnect internally, so a
// return before ctx ends is unusual and is retried with exponential backoff.
func (h *Hub) supervise(ctx context.Context, a *adapter) {
	backoff := time.Second
	for {
		started := time.Now()
		err := h.runAdapter(ctx, a)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		h.log.Warn("channel adapter exited; restarting", "channel", a.name, "err", err, "backoff", backoff)
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		backoff = min(backoff*2, maxRestartBackoff)
	}
}

func (h *Hub) shutdown() {
	h.mu.Lock()
	h.stopped = true
	type stopping struct {
		name string
		done chan struct{}
	}
	var wait []stopping
	for _, a := range h.adapters {
		if a.cancel != nil {
			a.cancel()
			wait = append(wait, stopping{a.name, a.done})
			a.cancel, a.done = nil, nil
		}
	}
	h.mu.Unlock()
	for _, s := range wait {
		h.waitStopped(s.name, s.done)
	}
	h.cancel()
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	t := time.NewTimer(stopTimeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		h.log.Warn("channel hub: background work still running at shutdown")
	}
}

func (h *Hub) adapter(name string) *adapter {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.adapters[name]
}

// live returns the adapter for name when config enables it.
func (h *Hub) live(name string) *adapter {
	a := h.adapter(name)
	if a == nil {
		return nil
	}
	if enabled, _, _ := wantState(h.cfg(), a); !enabled {
		return nil
	}
	return a
}

func (h *Hub) sortedAdapters() []*adapter {
	h.mu.Lock()
	out := make([]*adapter, 0, len(h.adapters))
	for _, a := range h.adapters {
		out = append(out, a)
	}
	h.mu.Unlock()
	slices.SortFunc(out, func(x, y *adapter) int { return cmp.Compare(x.name, y.name) })
	return out
}

func (h *Hub) Status() []core.ChannelStatus {
	cur := h.cfg()
	list := h.sortedAdapters()
	out := make([]core.ChannelStatus, 0, len(list))
	for _, a := range list {
		st := a.ch.Status()
		st.Name = a.name
		if enabled, _, known := wantState(cur, a); known {
			st.Enabled = enabled
			if !enabled {
				st.State, st.Detail = "disabled", ""
			}
		}
		out = append(out, st)
	}
	return out
}

func (h *Hub) Test(ctx context.Context, name string) (string, error) {
	a := h.adapter(name)
	if a == nil {
		return "", fmt.Errorf("channel %q: %w", name, core.ErrNotFound)
	}
	return a.ch.SelfTest(ctx)
}

func (h *Hub) DeepLink(channel, code string) string {
	if a := h.live(channel); a != nil {
		return a.ch.DeepLink(code)
	}
	return ""
}

// publishStatuses announces adapter state changes on the bus (admin
// broadcast). Adapters have no change callback, so the hub polls them.
func (h *Hub) publishStatuses() {
	for _, st := range h.Status() {
		h.mu.Lock()
		a := h.adapters[st.Name]
		changed := a != nil && (a.published.State != st.State || a.published.Detail != st.Detail ||
			a.published.Identity != st.Identity || a.published.Enabled != st.Enabled)
		if changed {
			a.published = st
		}
		h.mu.Unlock()
		if changed {
			h.d.Bus.Publish(core.Event{Type: core.EvChannelStatus, Data: core.ChannelStatusData{Status: st}})
		}
	}
}

// prune forgets dedup bookkeeping old enough not to matter any more.
func (h *Hub) prune() {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, at := range h.delivered {
		if now.Sub(at) > 48*time.Hour {
			delete(h.delivered, id)
		}
	}
	for id, at := range h.resolved {
		if now.Sub(at) > 48*time.Hour {
			delete(h.resolved, id)
		}
	}
	for k, at := range h.slowed {
		if now.After(at) {
			delete(h.slowed, k)
		}
	}
	for k, at := range h.replied {
		if now.Sub(at) > 6*time.Hour {
			delete(h.replied, k)
		}
	}
}
