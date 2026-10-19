package scheduler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

// A change made over HTTP is audited once, here, with the request's actor,
// address and detail: the handler no longer writes a second row.
func TestChangesCarryTheirRequestOrigin(t *testing.T) {
	h := newHarness(t)
	ctx := core.WithAuditOrigin(context.Background(), core.AuditOrigin{Actor: "token:tok_x", IP: "10.0.0.7", Detail: map[string]any{"grants": 2}})
	sc := &core.Schedule{Principal: h.p, Name: "Weekly digest", Kind: core.SchedCron, Spec: "0 8 * * 1", Prompt: "Summarize", Enabled: true}
	if err := h.svc.Upsert(ctx, h.viewer(), sc, "user:"+string(h.owner)); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Delete(ctx, h.viewer(), sc.ID, h.owner); err != nil {
		t.Fatal(err)
	}
	all := h.audit.All()
	if len(all) != 2 {
		t.Fatalf("audit = %v", h.audit.Actions())
	}
	for _, e := range all {
		var d map[string]any
		_ = json.Unmarshal(e.Detail, &d)
		if e.Actor != "token:tok_x" || e.IP != "10.0.0.7" || d["grants"] != float64(2) || d["name"] != "Weekly digest" {
			t.Fatalf("%s = %+v %s", e.Action, e, e.Detail)
		}
	}
}
