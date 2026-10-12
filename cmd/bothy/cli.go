package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func newLogHandler(l config.Log) slog.Handler {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(l.Level))
	o := &slog.HandlerOptions{Level: lvl}
	if l.Format == "json" {
		return slog.NewJSONHandler(os.Stderr, o)
	}
	return slog.NewTextHandler(os.Stderr, o)
}

func auditConfig(ctx context.Context, w core.Auditor, r config.ChangeRecord) {
	d, _ := json.Marshal(r)
	_ = w.Record(ctx, core.AuditEntry{At: time.Now(), Actor: r.By, Action: "config." + r.Action, Target: r.Path, Outcome: "ok", Detail: d})
}

func publishConfigChanged(bus core.Bus, cfg config.Admin, old, cur *config.Config) {
	eff := cfg.Effective()
	bus.Publish(core.Event{Type: core.EvConfigChanged, Data: core.ConfigChangedData{Paths: config.Diff(old, cur), RestartRequired: eff.Restart}})
	if len(eff.Restart) > 0 {
		bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{Level: "warn", Code: "restart_required",
			Text: "Some changes need a restart.", Action: "/settings/general"}})
	}
	publishConfigBanners(bus, eff)
}

func publishConfigBanners(bus core.Bus, eff config.Effective) {
	b := core.BannerData{Level: "error", Code: "config_error", Action: "/settings/advanced",
		Text: "Some config values are invalid and fell back to the next layer.", Clear: len(eff.Errors) == 0}
	bus.Publish(core.Event{Type: core.EvBanner, Data: b})
	if eff.SafeMode {
		bus.Publish(core.Event{Type: core.EvBanner, Data: core.BannerData{Level: "error", Code: "safe_mode", Action: "/settings/advanced",
			Text: "bothy.yaml could not be parsed: safe mode (chat, channels and schedules are off until a restart)."}})
	}
}

func printSetupBanner(c *config.Config, token string) {
	url := c.Server.PublicURL
	if url == "" {
		url = localURL(c.Server.Listen)
	}
	fmt.Fprintf(os.Stderr, "\n  Bothy is ready for setup.\n  Open %s/setup#t=%s\n  (token also in %s/setup-token; valid %s)\n\n",
		url, token, c.Storage.DataDir, c.Auth.SetupTokenTTL.D())
}

// localURL turns a listen address into a URL reachable from the same host;
// wildcard binds (container mode listens on 0.0.0.0) are dialled on loopback.
func localURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://127.0.0.1:8080"
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// healthcheck runs inside the container without loading config, so it only
// honours the env override of server.listen.
func healthcheck(ctx context.Context) error {
	listen := os.Getenv("BOTHY_SERVER__LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, localURL(listen)+"/healthz", nil) //nolint:gosec // see below
	resp, err := http.DefaultClient.Do(req)                                                     //nolint:gosec // loopback probe built from server.listen, not user input
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}

// doctorCmd builds the app without starting loops, runs checks, prints them.
func doctorCmd(ctx context.Context, args []string, security bool) (int, error) {
	return 0, core.ErrNotImplemented // lead, phase 3: build() + doctor.Runner.Run/Fix + doctor.Print
}

// adminCmd is filled by the lead in phase 3 on module APIs: setup-token
// (Auth.RotateSetupToken), user (Auth.ResetPassword, Users().List), vault
// (Vault.RotateKEK, Vault.Status), config (config.Load/Effective), audit
// (Auditor.Verify, audit.Archive), backup (Store.Backup).
func adminCmd(ctx context.Context, cmd string, args []string) error {
	return core.ErrNotImplemented
}
