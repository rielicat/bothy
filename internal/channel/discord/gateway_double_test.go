//go:build race

package discord

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/disgoorg/disgo/gateway"

	"github.com/bothyhq/bothy/internal/channel/discord/discordtest"
)

// realGateway: disgo v0.19.6's gateway writes heartbeatCancel, the heartbeat
// timestamps and the sequence number from different goroutines without
// synchronization, so the race detector fails every test that connects it.
// Under -race the adapter gets this in-process gateway through its
// newGateway seam instead; the frames still come from the fake and are
// decoded by disgo, and the WebSocket path runs in the plain go test pass.
const realGateway = false

func useGateway(a *adapter, srv *discordtest.Server) {
	a.newGateway = func(_ string, handler gateway.EventHandlerFunc, _ ...gateway.ConfigOpt) gateway.Gateway {
		return &pipeGateway{srv: srv, handler: handler}
	}
}

type pipeGateway struct {
	gateway.Gateway // methods the adapter never calls panic
	srv             *discordtest.Server
	handler         gateway.EventHandlerFunc

	mu   sync.Mutex
	stop func()
}

func (g *pipeGateway) Open(context.Context) error {
	stop := g.srv.Subscribe(func(frame []byte) {
		var m struct {
			S int               `json:"s"`
			T gateway.EventType `json:"t"`
			D json.RawMessage   `json:"d"`
		}
		if json.Unmarshal(frame, &m) != nil {
			return
		}
		if ev, err := gateway.UnmarshalEventData(m.D, m.T); err == nil {
			g.handler(g, m.T, m.S, ev)
		}
	})
	g.mu.Lock()
	g.stop = stop
	g.mu.Unlock()
	return nil
}

func (g *pipeGateway) Close(ctx context.Context) { g.CloseWithCode(ctx, 1000, "") }

func (g *pipeGateway) CloseWithCode(context.Context, int, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stop != nil {
		g.stop()
		g.stop = nil
	}
}

func (g *pipeGateway) Status() gateway.Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stop != nil {
		return gateway.StatusReady
	}
	return gateway.StatusDisconnected
}

func (g *pipeGateway) Latency() time.Duration { return 0 }
