package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/gorilla/websocket"

	"github.com/rielicat/bothy/internal/core"
)

const (
	inboxSize        = 256
	minBackoff       = time.Second
	maxBackoff       = 5 * time.Minute
	restTimeout      = 15 * time.Second
	closeTimeout     = 2 * time.Second
	handshakeTimeout = 20 * time.Second
	// closeResumable is sent on shutdown instead of 1000, which would make
	// Discord invalidate the session; a restart then resumes and receives the
	// events it missed (SPEC §4.6: resume cursors live in Meta).
	closeResumable = 4000
	metaResume     = "channel." + name + ".resume"

	tokenHint  = "Discord rejected the bot token (channels.discord.token)"
	intentHint = "Discord refused the Message Content intent: enable it under Bot → Privileged Gateway Intents in the developer portal"
)

// errSessionOver stops disgo's detached reconnect loop for a torn-down
// session: that loop runs on a background context, and an already-connected
// error is the only dial failure that makes it give up.
var errSessionOver = fmt.Errorf("discord session closed: %w", discord.ErrGatewayAlreadyConnected)

// job is one normalized platform event waiting for the sink.
type job func(ctx context.Context, sink core.InboundSink)

func (a *adapter) Run(ctx context.Context, sink core.InboundSink) error {
	if !a.cfg.Enabled {
		<-ctx.Done()
		return nil
	}
	jobs := make(chan job, inboxSize)
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case j := <-jobs:
				j(ctx, sink)
			}
		}
	})
	defer wg.Wait()

	delay := minBackoff
	for {
		connected, err := a.session(ctx, jobs)
		if ctx.Err() != nil {
			return nil
		}
		if connected {
			delay = minBackoff
		}
		if ae, ok := errors.AsType[*authError](err); ok {
			a.forgetToken()
			a.setState(stateAuthFailed, ae.hint)
		} else {
			a.setState(stateBackoff, err.Error())
		}
		a.log.Warn("discord gateway session ended", "err", err, "retry_in", delay)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		delay = min(2*delay, maxBackoff)
	}
}

// session runs one gateway session: it returns when ctx ends or disgo gives
// up on the connection (a close code that forbids reconnecting).
func (a *adapter) session(ctx context.Context, jobs chan<- job) (connected bool, err error) {
	a.setState(stateConnecting, "")
	api := a.client()
	tok, err := a.token(ctx)
	if err != nil {
		return false, err
	}
	rctx, cancel := context.WithTimeout(ctx, restTimeout)
	defer cancel()
	me, err := api.CurrentUser(rctx)
	if err != nil {
		return false, a.apiErr("get current user", err)
	}
	a.setSelf(me.ID, me.Username)
	wsURL := a.cfg.GatewayURL
	if wsURL == "" {
		if wsURL, err = api.GatewayURL(rctx); err != nil {
			return false, a.apiErr("get gateway", err)
		}
	}
	cancel()

	var stopped atomic.Bool
	fatal := make(chan error, 1)
	opts := []gateway.ConfigOpt{
		gateway.WithURL(wsURL),
		gateway.WithIntents(intents),
		// Uncompressed JSON: zstd's decoder window costs more idle memory
		// than the few events these intents deliver.
		gateway.WithCompression(gateway.CompressionNone),
		a.gatewayDialer(&stopped),
		gateway.WithLogger(a.libLog),
		gateway.WithBrowser("bothy"),
		gateway.WithDevice("bothy"),
		// disgo calls this only once it has stopped reconnecting by itself.
		gateway.WithCloseHandler(func(_ gateway.Gateway, err error, _ bool) {
			select {
			case fatal <- err:
			default:
			}
		}),
	}
	opts = append(opts, a.takeResume(ctx)...)
	gw := a.newGateway(tok, a.handler(ctx, jobs), opts...)
	a.setGateway(gw)
	defer a.setGateway(nil)
	if err := gw.Open(ctx); err != nil {
		stopped.Store(true)
		return false, gatewayErr(err)
	}
	a.setState(stateConnected, "")

	select {
	case <-ctx.Done():
		a.saveResume(ctx)
		stopped.Store(true)
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		gw.CloseWithCode(cctx, closeResumable, "shutting down")
		return true, ctx.Err()
	case err := <-fatal:
		stopped.Store(true)
		cctx, cancel := context.WithTimeout(ctx, closeTimeout)
		defer cancel()
		gw.Close(cctx)
		return true, gatewayErr(err)
	}
}

// gatewayDialer routes every gateway dial, resume URLs included, through the
// egress system dialer for that exact host:port.
func (a *adapter) gatewayDialer(stopped *atomic.Bool) gateway.ConfigOpt {
	d := &websocket.Dialer{HandshakeTimeout: handshakeTimeout}
	dial := func(secure bool) dialFunc {
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			if stopped.Load() {
				return nil, errSessionOver
			}
			d, err := systemDialer(a.deps, a.dest(addr), secure)
			if err != nil {
				return nil, err
			}
			conn, err := d(ctx, network, addr)
			if err != nil {
				a.setState(stateBackoff, err.Error())
				return nil, err
			}
			a.setState(stateConnecting, "")
			return conn, nil
		}
	}
	d.NetDialContext = dial(false)
	d.NetDialTLSContext = dial(true)
	return gateway.WithDialer(d)
}

func gatewayErr(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	if ce, ok := errors.AsType[*websocket.CloseError](err); ok {
		switch ce.Code {
		case gateway.CloseEventCodeAuthenticationFailed.Code:
			return &authError{hint: tokenHint, err: err}
		case gateway.CloseEventCodeDisallowedIntent.Code:
			return &authError{hint: intentHint, err: err}
		}
	}
	return fmt.Errorf("discord: gateway: %w", err)
}

// resumeState is what a later session needs to resume this one.
type resumeState struct {
	SessionID string `json:"session_id"`
	Seq       int    `json:"seq"`
	URL       string `json:"url"`
}

// takeResume consumes the cursor of the previous session, in memory when
// this process ran it, else from Meta. The stored copy is deleted either way:
// a cursor that fails to resume must not be retried forever.
func (a *adapter) takeResume(ctx context.Context) []gateway.ConfigOpt {
	a.mu.Lock()
	rs := a.resume
	a.resume = resumeState{}
	a.mu.Unlock()
	if a.deps.Meta != nil {
		if v, err := a.deps.Meta.Get(ctx, metaResume); err == nil {
			if rs.SessionID == "" {
				if err := json.Unmarshal([]byte(v), &rs); err != nil {
					a.log.Warn("discord: ignoring unreadable resume cursor", "err", err)
					rs = resumeState{}
				}
			}
			if err := a.deps.Meta.Delete(ctx, metaResume); err != nil {
				a.log.Warn("discord: cannot delete resume cursor", "err", err)
			}
		}
	}
	if rs.SessionID == "" || rs.Seq <= 0 {
		return nil
	}
	opts := []gateway.ConfigOpt{gateway.WithSessionID(rs.SessionID), gateway.WithSequence(rs.Seq)}
	if u, err := url.Parse(rs.URL); err == nil && (u.Scheme == "wss" || u.Scheme == "ws") {
		opts = append(opts, gateway.WithResumeURL(rs.URL))
	}
	return opts
}

func (a *adapter) saveResume(ctx context.Context) {
	a.mu.Lock()
	rs := a.resume
	a.mu.Unlock()
	if rs.SessionID == "" || rs.Seq <= 0 || a.deps.Meta == nil {
		return
	}
	b, err := json.Marshal(rs)
	if err != nil {
		return
	}
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	if err := a.deps.Meta.Set(mctx, metaResume, string(b)); err != nil {
		a.log.Warn("discord: cannot store resume cursor", "err", err)
	}
}

// track records the session cursor from the events disgo hands over. Doing
// it here, on disgo's reader goroutine, avoids reading disgo's own copy,
// which it updates without synchronization.
func (a *adapter) track(seq int, ready *gateway.EventReady) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ready != nil {
		a.resume = resumeState{SessionID: ready.SessionID, URL: ready.ResumeGatewayURL}
	}
	if seq > a.resume.Seq {
		a.resume.Seq = seq
	}
}

func (a *adapter) handler(ctx context.Context, jobs chan<- job) gateway.EventHandlerFunc {
	push := func(j job) {
		select {
		case jobs <- j:
		default:
			a.log.Warn("discord: inbound queue full, dropping event", "dropped", a.dropped.Add(1))
		}
	}
	return func(_ gateway.Gateway, _ gateway.EventType, seq int, ev gateway.EventData) {
		switch e := ev.(type) {
		case gateway.EventHeartbeatAck:
		case gateway.EventReady:
			a.track(seq, &e)
			a.setSelf(e.User.ID, e.User.Username)
			a.setState(stateConnected, "")
		case gateway.EventResumed:
			a.track(seq, nil)
			a.setState(stateConnected, "")
		case gateway.EventMessageCreate:
			a.track(seq, nil)
			m := e.Message
			push(func(ctx context.Context, sink core.InboundSink) { a.onMessage(ctx, sink, m) })
		case gateway.EventInteractionCreate:
			a.track(seq, nil)
			if ci, ok := e.Interaction.(discord.ComponentInteraction); ok {
				go a.onPress(ctx, push, ci)
			}
		default:
			a.track(seq, nil)
		}
	}
}
