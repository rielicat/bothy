package main

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/rielicat/bothy/internal/config"
)

// unsetenv removes k for the test and restores it afterwards; an empty value
// would still be a (bad) env layer entry.
func unsetenv(t *testing.T, k string) {
	t.Helper()
	t.Setenv(k, "")
	os.Unsetenv(k)
}

func TestRunHelpExitsZero(t *testing.T) {
	for _, argv := range [][]string{{"--help"}, {"-h"}, {"help"}, {"vault", "--help"}} {
		var out, errb bytes.Buffer
		if code := run(context.Background(), argv, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "usage: bothy") {
			t.Errorf("bothy %v = %d, stdout %q", argv, code, out.String())
		}
	}
}

func TestUsageErrorsCreateNoState(t *testing.T) {
	data := filepath.Join(t.TempDir(), "bothy-dat")
	t.Setenv("BOTHY_STORAGE__DATA_DIR", data)
	cases := []struct {
		argv []string
		code int
	}{
		{[]string{"vault", "stauts"}, 2},
		{[]string{"user", "lst"}, 2},
		{[]string{"user"}, 2},
		{[]string{"setup-token", "extra"}, 2},
		{[]string{"backup", "now"}, 2},
		{[]string{"doctor", "extra"}, 2},
		{[]string{"serve", "extra"}, 2},
		{[]string{"serve", "--help"}, 0},
		{[]string{"serve", "--bogus"}, 1},
		{[]string{"frobnicate"}, 2},
	}
	for _, c := range cases {
		if code := run(context.Background(), c.argv, io.Discard, io.Discard); code != c.code {
			t.Errorf("bothy %v = %d, want %d", c.argv, code, c.code)
		}
		if _, err := os.Stat(data); err == nil {
			t.Fatalf("bothy %v created %s", c.argv, data)
		}
	}
}

func TestDefaultDataDirHint(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can create the directory anyway")
	}
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	old := defaultDataDir
	defaultDataDir = filepath.Join(ro, "data")
	t.Cleanup(func() { defaultDataDir = old })
	unsetenv(t, "BOTHY_STORAGE__DATA_DIR")
	unsetenv(t, "BOTHY_DEPLOYMENT")
	unsetenv(t, "BOTHY_CONFIG")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := buildBase(ctx)
	if err == nil {
		a.close()
		t.Fatal("built on an unwritable default data dir")
	}
	if !strings.Contains(err.Error(), "BOTHY_STORAGE__DATA_DIR") {
		t.Fatalf("error does not name the setting: %v", err)
	}

	t.Setenv("BOTHY_DEPLOYMENT", "container") // the image's /data is a volume: no native hint
	if _, err := buildBase(ctx); err == nil || strings.Contains(err.Error(), "BOTHY_STORAGE__DATA_DIR") {
		t.Fatalf("container error = %v", err)
	}
}

func TestSetMemoryLimit(t *testing.T) {
	old := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(old) })

	unsetenv(t, "GOMEMLIMIT")
	debug.SetMemoryLimit(math.MaxInt64)
	setMemoryLimit()
	if got := debug.SetMemoryLimit(-1); got != 48<<20 {
		t.Fatalf("limit without GOMEMLIMIT = %d, want 48 MiB", got)
	}

	t.Setenv("GOMEMLIMIT", "1GiB") // the runtime applied it at start; serve must not override it
	debug.SetMemoryLimit(1 << 30)
	setMemoryLimit()
	if got := debug.SetMemoryLimit(-1); got != 1<<30 {
		t.Fatalf("limit with GOMEMLIMIT = %d, want it kept", got)
	}
}

// writeConfig points the data dir at a fresh directory holding bothy.yaml.
func writeConfig(t *testing.T, yml string) string {
	t.Helper()
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "bothy.yaml"), []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOTHY_STORAGE__DATA_DIR", data)
	unsetenv(t, "BOTHY_SERVER__LISTEN")
	unsetenv(t, "BOTHY_CONFIG")
	return data
}

func healthz() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
		}
	})
}

func TestHealthcheckFollowsConfig(t *testing.T) {
	ctx := context.Background()

	plain := httptest.NewServer(healthz())
	defer plain.Close()
	writeConfig(t, "server:\n  listen: "+plain.Listener.Addr().String()+"\n")
	if err := healthcheck(ctx, nil); err != nil {
		t.Fatalf("server.listen from bothy.yaml: %v", err)
	}

	tlsSrv := httptest.NewTLSServer(healthz())
	defer tlsSrv.Close()
	writeConfig(t, "server:\n  listen: "+tlsSrv.Listener.Addr().String()+"\n  tls:\n    cert_file: /certs/fullchain.pem\n    key_file: /certs/privkey.pem\n")
	if err := healthcheck(ctx, nil); err != nil {
		t.Fatalf("TLS listener: %v", err)
	}

	writeConfig(t, "")
	t.Setenv("BOTHY_SERVER__LISTEN", plain.Listener.Addr().String())
	if err := healthcheck(ctx, nil); err != nil {
		t.Fatalf("env override: %v", err)
	}
}

func TestSetupURLs(t *testing.T) {
	c := config.Default()
	c.Server.Listen = "0.0.0.0:8443"
	c.Server.TLS = config.TLS{CertFile: "/c.pem", KeyFile: "/k.pem"}
	c.Storage.DataDir = "/data"
	var b bytes.Buffer
	printSetupBanner(&b, c, "tok123")
	if out := b.String(); !strings.Contains(out, "https://127.0.0.1:8443/setup#t=tok123 on this server") ||
		!strings.Contains(out, "setup code tok123") || !strings.Contains(out, "valid for 24 hours") {
		t.Fatalf("TLS banner: %q", out)
	}
	// A known public URL makes the link usable from anywhere.
	pub := *c
	pub.Server.PublicURL = "https://bothy.acme.com"
	b.Reset()
	printSetupBanner(&b, &pub, "tok123")
	if out := b.String(); !strings.Contains(out, "Open https://bothy.acme.com/setup#t=tok123\n") {
		t.Fatalf("public banner: %q", out)
	}
	if plainDuration(30*time.Minute) != "30 minutes" || plainDuration(time.Hour) != "1 hour" || plainDuration(90*time.Second) != "1m30s" {
		t.Fatal("plainDuration")
	}

	c.Server.TLS = config.TLS{}
	b.Reset()
	printSetupPending(&b, c)
	if out := b.String(); !strings.Contains(out, "http://127.0.0.1:8443/setup") || !strings.Contains(out, "/data/setup-token") ||
		!strings.Contains(out, "bothy setup-token") || strings.Contains(out, "#t=") {
		t.Fatalf("pending notice: %q", out)
	}

	c.Server.PublicURL = "https://bothy.example.com/"
	if got := baseURL(c); got != "https://bothy.example.com" {
		t.Fatalf("baseURL = %q", got)
	}
}

func TestConfigCheckPrintsLocationOnce(t *testing.T) {
	writeConfig(t, "")
	bad := filepath.Join(t.TempDir(), "bad1.yaml")
	if err := os.WriteFile(bad, []byte("providers:\n  - id: anthropic\n    type: anthropic\n    api_key: sk-ant-api03-abcdefabcdefabcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"config", "check", bad}, &out, &errb); code != 1 {
		t.Fatalf("check = %d", code)
	}
	line, _, _ := strings.Cut(errb.String(), "\n")
	if strings.Count(line, "bad1.yaml:4") != 1 || strings.Count(line, "providers.anthropic.api_key") != 1 {
		t.Fatalf("location repeated or missing: %q", line)
	}
}

func TestConfigEffectiveIsAnnotatedYAML(t *testing.T) {
	data := writeConfig(t, "timezone: Europe/London\npricing:\n  openai/gpt-5.1:\n    in: 1.25\n")
	t.Setenv("BOTHY_BUDGETS__DAILY_USD", "7")
	var out bytes.Buffer
	if code := run(context.Background(), []string{"config", "effective"}, &out, io.Discard); code != 0 {
		t.Fatalf("effective = %d", code)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, out.String())
	}
	if parsed["timezone"] != "Europe/London" {
		t.Fatalf("timezone = %v", parsed["timezone"])
	}
	if p, _ := parsed["pricing"].(map[string]any); p == nil || p["openai/gpt-5.1"] == nil {
		t.Fatalf("dotted pricing key split: %v", parsed["pricing"])
	}
	text := out.String()
	for _, want := range []string{
		`timezone: "Europe/London"  # file ` + filepath.Join(data, "bothy.yaml") + ":1",
		"daily_usd: 7  # env BOTHY_BUDGETS__DAILY_USD",
		`listen: "127.0.0.1:8080"  # default, locked`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		if !strings.HasSuffix(strings.TrimSpace(l), ":") && !strings.Contains(l, "  # ") && !strings.Contains(l, "- # ") {
			t.Errorf("value without a source comment: %q", l)
		}
	}
	if n := strings.Count(text, "\n"); n > 1000 {
		t.Errorf("effective printed %d lines; one per value expected", n)
	}
}

func TestConfigDefaultsInContainer(t *testing.T) {
	t.Setenv("BOTHY_DEPLOYMENT", "container")
	var out bytes.Buffer
	if code := run(context.Background(), []string{"config", "defaults"}, &out, io.Discard); code != 0 {
		t.Fatalf("defaults = %d", code)
	}
	if !strings.Contains(out.String(), "listen: 0.0.0.0:8080") {
		t.Fatalf("container listen default missing:\n%s", out.String())
	}
}

func TestMain(m *testing.M) {
	if marker := os.Getenv(respawnMarker); marker != "" { // the child TestRespawn starts
		_ = os.WriteFile(marker, []byte(strings.Join(os.Args, "\x00")), 0o600)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const respawnMarker = "BOTHY_TEST_RESPAWN_MARKER"

// TestRespawn checks the mechanism reexec uses where there is no exec(2): the
// test binary starts itself and the child reports its argv through a file.
func TestRespawn(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child")
	t.Setenv(respawnMarker, marker)
	if err := respawn(); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(marker); err == nil && len(b) > 0 {
			if string(b) != strings.Join(os.Args, "\x00") {
				t.Fatalf("child argv %q, want %q", b, os.Args)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("respawned process never ran")
		}
	}
}
