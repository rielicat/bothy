package slack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/rielicat/bothy/internal/core"
)

const (
	inboxSize   = 256
	minBackoff  = time.Second
	maxBackoff  = 5 * time.Minute
	restTimeout = 15 * time.Second
)

// job is one normalized platform event waiting for the sink.
type job func(ctx context.Context, sink core.InboundSink)

func (a *adapter) Run(ctx context.Context, sink core.InboundSink) error {
	if !a.conf().cfg.Enabled {
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
			a.forgetTokens()
			a.setState(stateAuthFailed, ae.hint)
		} else {
			a.setState(stateBackoff, err.Error())
		}
		a.log.Warn("slack socket mode session ended", "err", err, "retry_in", delay)
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

// session runs one Socket Mode client until ctx ends or slack-go gives up.
// slack-go reconnects by itself on a disconnect request and on network
// errors; it returns only for errors it deems fatal (token rejections).
func (a *adapter) session(ctx context.Context, jobs chan<- job) (connected bool, err error) {
	a.setState(stateConnecting, "")
	api, err := a.client(ctx)
	if err != nil {
		return false, err
	}
	bot, err := a.secret(ctx, &a.bot, a.conf().cfg.BotToken, botKey, "xoxb-")
	if err != nil {
		return false, err
	}
	app, err := a.secret(ctx, &a.app, a.conf().cfg.AppToken, appKey, "xapp-")
	if err != nil {
		return false, err
	}
	rctx, cancel := context.WithTimeout(ctx, restTimeout)
	auth, err := api.AuthTestContext(rctx)
	cancel()
	if err != nil {
		return false, a.apiErr("auth.test", botKey, err)
	}
	a.setSelf(auth)

	sctx, stop := context.WithCancel(ctx)
	defer stop()
	sock := a.newSocket(a.newClient(bot, app), a.socketDialer())
	done := make(chan error, 1)
	go func() { done <- sock.Run(sctx) }()
	for {
		select {
		case err := <-done:
			return connected, socketErr(err)
		case ev := <-sock.Events():
			if a.onEvent(sctx, sock, jobs, ev) {
				connected = true
			}
		}
	}
}

func socketErr(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	if authCodes[err.Error()] {
		return &authError{hint: "Slack rejected " + appKey + " (" + err.Error() + ")", err: err}
	}
	if se, ok := errors.AsType[slack.StatusCodeError](err); ok && se.Code == http.StatusNotFound {
		return &authError{hint: "Slack rejected " + appKey, err: err}
	}
	return fmt.Errorf("slack: socket mode: %w", err)
}

// onEvent handles one Socket Mode event and reports whether the connection
// is up. Requests are acknowledged as soon as their event is queued, well
// inside Slack's 3 s, however long the hub takes; one that cannot be queued
// is left unacknowledged so that Slack redelivers it.
func (a *adapter) onEvent(ctx context.Context, sock socket, jobs chan<- job, ev socketmode.Event) bool {
	var j job
	switch ev.Type {
	case socketmode.EventTypeConnecting:
		a.setState(stateConnecting, "")
		return false
	case socketmode.EventTypeConnectionError:
		detail := "connection error"
		if ce, ok := ev.Data.(*slack.ConnectionErrorEvent); ok && ce.ErrorObj != nil {
			detail = ce.ErrorObj.Error()
		}
		a.setState(stateBackoff, detail)
		return false
	case socketmode.EventTypeInvalidAuth:
		a.setState(stateAuthFailed, "Slack rejected "+appKey)
		return false
	case socketmode.EventTypeHello:
		a.setState(stateConnected, "")
		return true
	case socketmode.EventTypeEventsAPI:
		if e, ok := ev.Data.(slackevents.EventsAPIEvent); ok {
			j = a.eventJob(e)
		}
	case socketmode.EventTypeInteractive:
		if cb, ok := ev.Data.(slack.InteractionCallback); ok {
			j = a.actionJob(cb)
		}
	case socketmode.EventTypeSlashCommand:
		if c, ok := ev.Data.(slack.SlashCommand); ok {
			j = a.slashJob(c)
		}
	case socketmode.EventTypeIncomingError, socketmode.EventTypeErrorBadMessage, socketmode.EventTypeErrorWriteFailed:
		a.log.Warn("slack: socket mode error", "type", string(ev.Type), "err", eventErr(ev.Data))
		return false
	default:
		return false
	}
	if j != nil {
		select {
		case jobs <- j:
		default:
			a.log.Warn("slack: inbound queue full, leaving the request for Slack to retry", "dropped", a.dropped.Add(1))
			return false
		}
	}
	if ev.Request != nil && ev.Request.EnvelopeID != "" {
		if err := sock.Ack(ctx, ev.Request.EnvelopeID); err != nil {
			a.log.Warn("slack: cannot acknowledge request", "err", err)
		}
	}
	return false
}

// eventErr extracts the cause of an error event; the raw frames it carries
// may hold message content and stay out of the log.
func eventErr(data any) error {
	switch e := data.(type) {
	case *socketmode.ErrorBadMessage:
		return e.Cause
	case *socketmode.ErrorWriteFailed:
		return e.Cause
	case *slack.IncomingEventError:
		return e.ErrorObj
	}
	return nil
}
