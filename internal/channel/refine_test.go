package channel

import (
	"errors"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func TestRefineCommand(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		p := core.UserPrincipal(h.owner.ID)
		chat := h.dmChat(ownerChat)
		setting := func() string {
			t.Helper()
			v, err := h.store.Meta().Get(h.ctx, core.RefineMetaKey(h.session(p, chat).ID))
			if errors.Is(err, core.ErrNotFound) {
				return ""
			}
			if err != nil {
				t.Fatal(err)
			}
			return v
		}

		h.dm(ownerChat, "/refine")
		h.wantVisible(ownerChat, "Refine is off for this conversation, as by default.")
		h.dm(ownerChat, "/refine ON")
		h.wantVisible(ownerChat, "I draft each answer 3 times and check each draft twice before I reply")
		h.wantVisible(ownerChat, "can cost up to 21 times as much")
		if setting() != core.RefineOn {
			t.Fatalf("setting = %q after /refine on", setting())
		}
		h.dm(ownerChat, "/refine off")
		h.wantVisible(ownerChat, "Refine is off for this conversation. /refine on turns it on.")
		if setting() != core.RefineOff {
			t.Fatalf("setting = %q after /refine off", setting())
		}
		h.cfg.Set(func(c *config.Config) { c.Agent.Refine.Enabled, c.Agent.Refine.Drafts = true, 1 })
		h.dm(ownerChat, "/refine default")
		h.wantVisible(ownerChat, "Refine is on for this conversation, as by default: I draft each answer once")
		if setting() != "" {
			t.Fatalf("setting = %q after /refine default", setting())
		}
		h.dm(ownerChat, "/refine sometimes")
		h.wantVisible(ownerChat, "Usage: /refine on, /refine off or /refine default.")
		if n := len(h.runner.Submitted()); n != 0 {
			t.Fatalf("commands reached the runner as messages: %d submits", n)
		}
	})
}
