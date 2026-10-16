package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

func TestTeamAccessReportAuditedOnceWithBanner(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	at := now.Add(-2 * 24 * time.Hour).UnixMilli()
	if err := st.Meta().Set(ctx, teamAccessKey, `{"at":`+strconv.FormatInt(at, 10)+`,"linked":3,"guests":1,"approvers":2}`); err != nil {
		t.Fatal(err)
	}
	au := &fakes.Auditor{}
	bus := events.New(events.Options{})
	log := slog.New(slog.DiscardHandler)
	reportTeamAccess(ctx, st, au, bus, now, log)
	reportTeamAccess(ctx, st, au, bus, now, log)
	if got := au.All(); len(got) != 1 || got[0].Action != "migration.team_access" || got[0].Actor != "system" {
		t.Fatalf("audit = %+v, want one migration.team_access row", got)
	}
	var detail map[string]any
	if err := json.Unmarshal(au.All()[0].Detail, &detail); err != nil || detail["linked"] != float64(3) {
		t.Fatalf("detail = %s", au.All()[0].Detail)
	}
	raw, _ := st.Meta().Get(ctx, teamAccessKey)
	var report map[string]any
	if err := json.Unmarshal([]byte(raw), &report); err != nil || report["audited"] != true {
		t.Fatalf("report after boot = %s", raw)
	}
	bs := bus.Banners(core.Filter{Admin: true})
	if len(bs) != 1 || bs[0].Data.(core.BannerData).Code != "team_access" {
		t.Fatalf("banners = %+v", bs)
	}

	// Older than a week: audited already, no banner.
	bus = events.New(events.Options{})
	reportTeamAccess(ctx, st, au, bus, now.Add(6*24*time.Hour), log)
	if len(bus.Banners(core.Filter{Admin: true})) != 0 || len(au.All()) != 1 {
		t.Fatal("a report over a week old still raised a banner or was audited again")
	}
}

// A report of a migration that moved nobody is dropped unseen.
func TestTeamAccessEmptyReportDropped(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	if err := st.Meta().Set(ctx, teamAccessKey, `{"at":`+strconv.FormatInt(now.UnixMilli(), 10)+
		`,"linked":0,"approvers":0,"disabled_accounts":0,"guests":0,"telegram_guests":0,"guests_blocked":0,"schedules_paused":0}`); err != nil {
		t.Fatal(err)
	}
	au := &fakes.Auditor{}
	bus := events.New(events.Options{})
	reportTeamAccess(ctx, st, au, bus, now, slog.New(slog.DiscardHandler))
	if len(au.All()) != 0 || len(bus.Banners(core.Filter{Admin: true})) != 0 {
		t.Fatalf("an empty report was audited (%d) or raised a banner", len(au.All()))
	}
	if _, err := st.Meta().Get(ctx, teamAccessKey); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("the empty report is still there: %v", err)
	}
}
