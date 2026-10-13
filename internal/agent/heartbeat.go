package agent

import (
	"strings"
	"sync"

	"github.com/rielicat/bothy/internal/core"
)

const (
	heartbeatOK = "HEARTBEAT_OK"
	// maxHeldEvents bounds what a heartbeat sink holds back; a run that long
	// is delivered as it goes.
	maxHeldEvents = 4096
)

// heartbeatSink suppresses delivery of a heartbeat run whose final answer
// starts with HEARTBEAT_OK (SPEC §5.16). The scheduler wraps the sink of a
// heartbeat run it starts the same way, but a resumed parked run gets a new
// sink from resumeSink, which would otherwise deliver the reply.
type heartbeatSink struct {
	mu   sync.Mutex
	next core.RunSink
	buf  []core.Event
	text strings.Builder // text of the current step; tool calls start a new one
	pass bool
	done bool
}

func (q *heartbeatSink) OnEvent(e core.Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	switch {
	case q.done:
		return
	case q.pass:
		q.next.OnEvent(e)
		return
	}
	switch e.Type {
	case core.EvRunDelta:
		if d, ok := e.Data.(core.DeltaData); ok {
			q.text.WriteString(d.Text)
		}
	case core.EvToolCall, core.EvToolResult:
		q.text.Reset()
	case core.EvRunFinished:
		if isHeartbeatOK(q.text.String()) {
			q.buf, q.done = nil, true
			return
		}
	}
	q.buf = append(q.buf, e)
	if e.Type == core.EvRunFinished || e.Type == core.EvRunParked || len(q.buf) >= maxHeldEvents {
		for _, b := range q.buf {
			q.next.OnEvent(b)
		}
		q.buf, q.pass = nil, true
	}
}

// isHeartbeatOK tolerates markdown emphasis or code ticks around the token,
// as the scheduler does.
func isHeartbeatOK(text string) bool {
	return strings.HasPrefix(strings.TrimLeft(strings.TrimSpace(text), "*_`> "), heartbeatOK)
}
