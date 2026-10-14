package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// defaultDataDir is the image's volume; a var so tests can point it at an
// unusable path.
var defaultDataDir = "/data"

// dataDir is $BOTHY_STORAGE__DATA_DIR, else defaultDataDir.
func dataDir() (dir string, defaulted bool) {
	if v := os.Getenv("BOTHY_STORAGE__DATA_DIR"); v != "" {
		return v, false
	}
	return defaultDataDir, true
}

// dataDirErr names the setting when the default data dir fails outside a
// container: native installs rarely have a writable /data.
func dataDirErr(err error, dir string, defaulted bool) error {
	var pe *fs.PathError
	if err == nil || !defaulted || os.Getenv("BOTHY_DEPLOYMENT") == "container" || !errors.As(err, &pe) {
		return err
	}
	if rel, rerr := filepath.Rel(dir, pe.Path); rerr != nil || strings.HasPrefix(rel, "..") {
		return err
	}
	return fmt.Errorf("%w\n  No data directory is set and the default %s is not usable here. Choose one (created 0700), e.g.:\n"+
		"    BOTHY_STORAGE__DATA_DIR=~/bothy-data bothy serve", err, dir)
}

// defaultMemLimit is the soft heap limit the footprint budget assumes (SPEC
// §11.3). serve applies it unless GOMEMLIMIT is set, so `docker run`, native
// and Homebrew installs behave like the compose deployment.
const defaultMemLimit = 48 << 20

func setMemoryLimit() {
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(defaultMemLimit)
	}
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

// printSetupBanner prints the setup link and code. With a public URL the
// link works as printed; without one it names this server's own address,
// which only a browser on this server (or through a tunnel) can open.
func printSetupBanner(w io.Writer, c *config.Config, token string) {
	where := " on this server"
	if c.Server.PublicURL != "" {
		where = ""
	}
	fmt.Fprintf(w, "\n  Bothy is ready for setup.\n  Open %s/setup#t=%s%s\n  or paste the setup code %s\n  (also in %s/setup-token; valid for %s)\n\n",
		baseURL(c), token, where, token, c.Storage.DataDir, plainDuration(c.Auth.SetupTokenTTL.D()))
}

// printSetupPending is the boot notice while setup is unfinished and the
// code minted earlier is still valid; the code itself is not repeated.
func printSetupPending(w io.Writer, c *config.Config) {
	fmt.Fprintf(w, "\n  Bothy is waiting for setup.\n  Open %s/setup and paste the setup code from %s/setup-token\n  (or print a new one with: bothy setup-token)\n\n",
		baseURL(c), c.Storage.DataDir)
}

// plainDuration says 24h as "24 hours" and 30m as "30 minutes".
func plainDuration(d time.Duration) string {
	unit := func(n int64, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %ss", n, one)
	}
	switch {
	case d > 0 && d%time.Hour == 0:
		return unit(int64(d/time.Hour), "hour")
	case d > 0 && d%time.Minute == 0:
		return unit(int64(d/time.Minute), "minute")
	}
	return d.String()
}

func tlsOn(c *config.Config) bool { return c.Server.TLS.CertFile != "" && c.Server.TLS.KeyFile != "" }

// baseURL is public_url, else the listen address dialled from this host.
func baseURL(c *config.Config) string {
	if c.Server.PublicURL != "" {
		return strings.TrimRight(c.Server.PublicURL, "/")
	}
	return localURL(c.Server.Listen, tlsOn(c))
}

// localURL turns a listen address into a URL reachable from the same host;
// wildcard binds (container mode listens on 0.0.0.0) are dialled on loopback.
func localURL(listen string, useTLS bool) string {
	scheme := "http://"
	if useTLS {
		scheme = "https://"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return scheme + "127.0.0.1:8080"
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return scheme + net.JoinHostPort(host, port)
}

// healthcheck probes the listener serve opened: server.listen and server.tls
// come from the same layered config (bothy.yaml, env, UI overrides). `healthcheck
// sandboxd` probes the sidecar instead, which shares the image (and so its
// HEALTHCHECK) but serves no HTTP port.
func healthcheck(ctx context.Context, args []string) error {
	switch {
	case len(args) == 1 && args[0] == "sandboxd":
		// Connecting is the signal: sandboxd closes connections from any uid
		// but the gateway's, which a probe running as SANDBOXD_USER may not be.
		c, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", envOr("BOTHY_SBX_SOCKET", "/run/bothy/sandboxd.sock"))
		if err != nil {
			return err
		}
		return c.Close()
	case len(args) > 0:
		return fmt.Errorf("healthcheck takes no arguments, or sandboxd")
	}
	url, useTLS := probeTarget(ctx)
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	if useTLS {
		// The certificate names the public host, not the loopback address
		// probed here; the probe checks liveness, not identity.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // loopback probe of our own listener
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/healthz", nil) //nolint:gosec // loopback probe from server.listen
	resp, err := (&http.Client{Transport: tr}).Do(req)                             //nolint:gosec // loopback probe from server.listen
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}

// probeTarget is the loopback URL of the gateway's listener. Should the
// config not load, the env override and the built-in default still apply.
func probeTarget(ctx context.Context) (url string, useTLS bool) {
	ctx, cancel := context.WithCancel(ctx) // ends the config file watcher
	defer cancel()
	m, err := loadConfig(ctx, "")
	if err != nil {
		return localURL(envOr("BOTHY_SERVER__LISTEN", "127.0.0.1:8080"), false), false
	}
	c := m.Current()
	return localURL(c.Server.Listen, tlsOn(c)), tlsOn(c)
}
