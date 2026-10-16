package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	teamAccessKey = "team_access_report"
	// teamAccessBanner is how long admins are reminded to review People
	// after the 0013 migration moved chat apps off linked accounts.
	teamAccessBanner = 7 * 24 * time.Hour
)

// reportTeamAccess audits what the 0013 migration changed, once, and
// reminds admins to review People while the report is recent. Any change
// an admin makes in People deletes the report (httpapi).
func reportTeamAccess(ctx context.Context, st core.Store, au core.Auditor, bus core.Bus, now time.Time, log *slog.Logger) {
	raw, err := st.Meta().Get(ctx, teamAccessKey)
	if errors.Is(err, core.ErrNotFound) {
		return
	}
	if err != nil {
		log.Warn("reading the team access report failed", "err", err)
		return
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		log.Warn("the team access report is not JSON", "err", err)
		return
	}
	if report["audited"] != true {
		if au != nil {
			detail, _ := json.Marshal(report)
			if err := au.Record(ctx, core.AuditEntry{At: now, Actor: "system", Action: "migration.team_access", Target: "people",
				Outcome: "ok", Detail: detail}); err != nil {
				log.Warn("auditing the team access migration failed", "err", err)
				return
			}
		}
		report["audited"] = true
		if b, err := json.Marshal(report); err == nil {
			if err := st.Meta().Set(ctx, teamAccessKey, string(b)); err != nil {
				log.Warn("saving the team access report failed", "err", err)
			}
		}
	}
	at, _ := report["at"].(float64)
	if bus == nil || now.Sub(time.UnixMilli(int64(at))) >= teamAccessBanner {
		return
	}
	bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{Level: "info", Code: "team_access", Action: "/settings/people",
		Text: "People in chat apps no longer link to Bothy accounts. Check who can use Bothy and who can approve in People."}})
}
