package channel

import (
	"context"

	"github.com/rielicat/bothy/internal/core"
)

// authFailedCheck is the doctor check channels.auth_failed: an enabled
// channel whose platform rejected its credentials.
type authFailedCheck struct{ h *Hub }

func (authFailedCheck) ID() string { return "channels.auth_failed" }

func (c authFailedCheck) Run(context.Context) []core.Finding {
	var out []core.Finding
	for _, st := range c.h.Status() {
		if !st.Enabled || st.State != "auth_failed" {
			continue
		}
		title := channelTitle(st.Name)
		detail := title + " rejected the configured credentials."
		if st.Detail != "" {
			detail += " " + st.Detail
		}
		out = append(out, core.Finding{
			ID: "channels.auth_failed", Area: "channels", Severity: core.SevMedium,
			Title:  title + " cannot sign in",
			Detail: detail,
			Fix:    "Settings → Channels → " + title + ": store a valid token in the vault, then test the channel.",
		})
	}
	return out
}

func (authFailedCheck) Fix(context.Context, core.Finding) error { return core.ErrNotImplemented }
