package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// rediscover runs a discovery of server to its end, as Reload does in the
// background.
func (h *harness) rediscover(server string) {
	h.t.Helper()
	s := h.m.server(server)
	h.m.closeInstances(s)
	h.m.discover(context.Background(), s)
}

func (h *harness) pins(server string) map[string][]*core.MCPPin {
	h.t.Helper()
	ps, err := h.m.Pins(context.Background(), server)
	if err != nil {
		h.t.Fatal(err)
	}
	out := map[string][]*core.MCPPin{}
	for _, p := range ps {
		out[p.Tool] = append(out[p.Tool], p)
	}
	return out
}

// A server that rolls a tool back to an earlier definition puts it up for
// review again, instead of the tool vanishing with nothing to approve.
func TestRollbackComesBackForReview(t *testing.T) {
	f := newFakeServer()
	ts := f.httpServer(t, false)
	h := newHarness(t, []config.MCPServer{httpServerCfg("tickets", ts.URL)}, withClient(ts.Client()))
	h.start()
	h.approve("tickets", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}, "block": {SideEffect: core.SERead}})
	f.setDesc("Search the docs, v2.")
	h.rediscover("tickets")
	h.approve("tickets", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}})
	f.setDesc("Search the docs.")
	h.rediscover("tickets")
	if st := h.status("tickets"); st.State != stPinsPending || st.Tools != 1 {
		t.Fatalf("status after the rollback = %+v", st)
	}
	search := h.pins("tickets")["search"]
	if len(search) != 2 || search[0].Status != core.PinPending || strings.Contains(string(search[0].Definition), "v2") ||
		search[1].Status != core.PinApproved {
		t.Fatalf("pins of search = %+v", search)
	}
	h.approve("tickets", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead, Hash: search[0].Hash}})
	if st := h.status("tickets"); st.State != stReady || st.Tools != 2 {
		t.Fatalf("status after approving the rollback = %+v", st)
	}
	if d := h.tool("mcp__tickets__search").Spec().Def.Description; d != "Search the docs." {
		t.Fatalf("registered description = %q", d)
	}
}

// Review lists only what the server offers now: a change it reverted before
// anyone looked, or a tool it dropped, is nothing to approve, and approving
// never unregisters the tool it does offer.
func TestReviewListsOnlyWhatTheServerOffers(t *testing.T) {
	f := newFakeServer()
	ts := f.httpServer(t, false)
	h := newHarness(t, []config.MCPServer{httpServerCfg("tickets", ts.URL)}, withClient(ts.Client()))
	h.start()
	h.approve("tickets", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}, "block": {SideEffect: core.SERead}})
	f.setDesc("Search the docs, and more.")
	f.mu.Lock()
	f.extra = []*mcpsdk.Tool{{Name: "delete_ticket", Description: "Delete a ticket.", InputSchema: map[string]any{"type": "object"}}}
	f.mu.Unlock()
	h.rediscover("tickets")
	f.setDesc("Search the docs.")
	f.mu.Lock()
	f.extra = nil
	f.mu.Unlock()
	h.rediscover("tickets")
	if st := h.status("tickets"); st.Pending != 0 || st.Tools != 2 {
		t.Fatalf("status after the revert = %+v", st)
	}
	pins := h.pins("tickets")
	if pins["delete_ticket"] != nil || len(pins["search"]) != 1 || pins["search"][0].Status != core.PinApproved {
		t.Fatalf("pins after the revert = %+v", pins)
	}
	err := h.m.Approve(context.Background(), "tickets", map[string]core.MCPToolApproval{"delete_ticket": {SideEffect: core.SERead}}, admin)
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("approving a tool the server doesn't offer = %v", err)
	}
	h.approve("tickets", map[string]core.MCPToolApproval{"search": {SideEffect: core.SELocalWrite}})
	if st := h.status("tickets"); st.State != stReady || st.Tools != 2 {
		t.Fatalf("approving the offered definition unregistered it: %+v", st)
	}

	f.mu.Lock()
	f.hideSearch = true
	f.mu.Unlock()
	f.setDesc("Search the docs, v3.")
	h.rediscover("tickets")
	if pins := h.pins("tickets"); pins["search"] != nil {
		t.Fatalf("a tool the server dropped is still listed: %+v", pins["search"])
	}
}

// An action an admin turned off stays off when the server tweaks it,
// instead of coming back as a new action allowed by default.
func TestTurnedOffActionStaysOffWhenChanged(t *testing.T) {
	f := newFakeServer()
	f.extra = []*mcpsdk.Tool{{Name: "close_ticket", Description: "Close a ticket.", InputSchema: map[string]any{"type": "object"}}}
	ts := f.httpServer(t, false)
	h := newHarness(t, []config.MCPServer{httpServerCfg("qatix", ts.URL)}, withClient(ts.Client()))
	h.start()
	h.approve("qatix", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}, "block": {SideEffect: core.SERead}, "close_ticket": {Disable: true}})
	f.mu.Lock()
	f.extra = []*mcpsdk.Tool{{Name: "close_ticket", Description: "Close a ticket and notify the customer.", InputSchema: map[string]any{"type": "object"}}}
	f.mu.Unlock()
	h.rediscover("qatix")
	if st := h.status("qatix"); st.State != stReady || st.Pending != 0 {
		t.Fatalf("status = %+v", st)
	}
	if p := h.pins("qatix")["close_ticket"]; len(p) != 1 || p[0].Status != core.PinDisabled || !strings.Contains(string(p[0].Definition), "notify") {
		t.Fatalf("close_ticket pins = %+v", p)
	}
	if _, ok := h.reg.Get("mcp__qatix__close_ticket"); ok {
		t.Fatal("a turned-off action was registered")
	}
}

// A changed definition starts its review at the class the admin chose for
// the earlier one, not the configured default.
func TestChangedDefinitionKeepsTheChosenClass(t *testing.T) {
	f := newFakeServer()
	ts := f.httpServer(t, false)
	h := newHarness(t, []config.MCPServer{httpServerCfg("tickets", ts.URL)}, withClient(ts.Client()))
	h.start()
	if p := h.pins("tickets")["search"]; p[0].SideEffect != core.SEExternalSend {
		t.Fatalf("a new tool's suggestion = %s, want the default external_send", p[0].SideEffect)
	}
	h.approve("tickets", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}})
	f.setDesc("Search the docs, reworded.")
	h.rediscover("tickets")
	if p := h.pins("tickets")["search"]; p[0].Status != core.PinPending || p[0].SideEffect != core.SERead {
		t.Fatalf("the changed definition's suggestion = %+v", p[0])
	}
	err := h.m.Approve(context.Background(), "tickets", map[string]core.MCPToolApproval{"search": {SideEffect: core.SELocalWrite, Hash: h.pins("tickets")["search"][1].Hash}}, admin)
	if !errors.Is(err, core.ErrConflict) || !strings.Contains(err.Error(), "Review its change first") {
		t.Fatalf("changing the class of the old definition = %v", err)
	}
}

// A server removed and added again under its name is reviewed afresh.
func TestRemovedServerIsReviewedAfresh(t *testing.T) {
	f := newFakeServer()
	ts := f.httpServer(t, false)
	cfg := httpServerCfg("tickets", ts.URL)
	h := newHarness(t, []config.MCPServer{cfg}, withClient(ts.Client()))
	h.start()
	h.approve("tickets", map[string]core.MCPToolApproval{"search": {SideEffect: core.SERead}, "block": {Disable: true}})
	h.m.applyConfig(nil, false)
	if hist, _ := h.store.MCP().History(context.Background(), "tickets"); len(hist) != 0 {
		t.Fatalf("a removed server's pins were kept: %d", len(hist))
	}
	h.m.applyConfig([]config.MCPServer{cfg}, true)
	h.waitFor("rediscovery", func() bool { return h.status("tickets").State == stPinsPending })
	if st := h.status("tickets"); st.Pending != 2 || st.Tools != 0 {
		t.Fatalf("re-added server = %+v", st)
	}
}
