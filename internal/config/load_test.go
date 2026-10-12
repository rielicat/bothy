package config

import (
	"bufio"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Server.Listen != "127.0.0.1:8080" || c.Storage.DataDir != "/data" || c.Auth.SetupTokenTTL.D() != 24*time.Hour ||
		c.Storage.BackupsKeep != 3 || !c.Policy.Trifecta.Enforce || c.Agent.CoalesceWindow.D() != 1200*time.Millisecond {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if Default() == Default() {
		t.Fatal("Default must return a fresh value")
	}
}

func TestLoadEnvOverlay(t *testing.T) {
	env := []string{
		"BOTHY_SERVER__LISTEN=127.0.0.1:18080",
		"BOTHY_BUDGETS__DAILY_USD=2",
		"BOTHY_AGENT__COALESCE_WINDOW=2s",
		"BOTHY_LOG__LEVEL=debug",
		"BOTHY_AUTH__ARGON2__ITERATIONS=3",
		"BOTHY_SCHEDULER__ENABLED=false",
		"BOTHY_AGENT__QUEUE_DEPTH=lots", // unparsable: ignored
		"BOTHY_MODE=multi",
	}
	m, err := Load(context.Background(), Options{DataDir: "/srv/bothy", Env: func() []string { return env }})
	if err != nil {
		t.Fatal(err)
	}
	c := m.Current()
	checks := []struct {
		name string
		ok   bool
	}{
		{"listen", c.Server.Listen == "127.0.0.1:18080"},
		{"daily usd", c.Budgets.DailyUSD == 2},
		{"duration", c.Agent.CoalesceWindow.D() == 2*time.Second},
		{"log level", c.Log.Level == "debug"},
		{"nested uint", c.Auth.Argon2.Iterations == 3},
		{"bool", !c.Scheduler.Enabled},
		{"bad value keeps default", c.Agent.QueueDepth == 8},
		{"top level", c.Mode == "multi"},
		{"data dir", c.Storage.DataDir == "/srv/bothy"},
	}
	for _, ck := range checks {
		if !ck.ok {
			t.Errorf("%s not applied: %+v", ck.name, c)
		}
	}
}

func TestLoadContainerDefaults(t *testing.T) {
	m, err := Load(context.Background(), Options{
		Env:      func() []string { return []string{"BOTHY_DEPLOYMENT=container"} },
		Defaults: func(c *Config) { c.Timezone = "Europe/London" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if c := m.Current(); c.Server.Listen != "0.0.0.0:8080" || c.Timezone != "Europe/London" || c.Storage.DataDir != "/data" {
		t.Fatalf("got listen=%q tz=%q data=%q", c.Server.Listen, c.Timezone, c.Storage.DataDir)
	}
}

func TestManagerMinimalSurface(t *testing.T) {
	m, err := Load(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	cancel := m.Watch(func(old, cur *Config) {})
	cancel()
	m.SetOnSet(func(ChangeRecord) {})
	if _, err := m.Set(context.Background(), nil, "user:x"); err == nil {
		t.Error("Set should report not implemented")
	}
	if err := m.Reload(context.Background()); err != nil {
		t.Error(err)
	}
	if eff := m.Effective(); eff.SafeMode || len(eff.Errors) != 0 {
		t.Errorf("Effective = %+v", eff)
	}
	if len(m.Schema()) == 0 {
		t.Error("empty schema")
	}
	var _ Admin = m
	c := Default()
	if Static(c).Current() != c {
		t.Error("Static lost its config")
	}
}

func TestResolveDirs(t *testing.T) {
	none := func(string) bool { return false }
	all := func(string) bool { return true }
	c := Default()
	c.Storage.DataDir = "/srv/data"
	d := resolveDirs(c, none)
	if d.Data != "/srv/data" || d.Work != filepath.Join("/srv/data", "work") || d.Run != "/run/bothy" ||
		d.Keyfile != filepath.Join("/srv/data", "keys", "kek") || !d.KeyfileColocated {
		t.Fatalf("no mounts: %+v", d)
	}
	d = resolveDirs(c, all)
	if d.Work != "/work" || d.Keyfile != "/keys/kek" || d.KeyfileColocated {
		t.Fatalf("mounted: %+v", d)
	}
	c.Storage.WorkDir, c.Vault.Keyfile = "/w", "/k/kek"
	if d = resolveDirs(c, none); d.Work != "/w" || d.Keyfile != "/k/kek" {
		t.Fatalf("explicit: %+v", d)
	}
}

func TestMountinfo(t *testing.T) {
	info := `22 1 0:21 / / rw,relatime - overlay overlay rw
35 22 8:1 /var/lib/docker/volumes/bothy_work/_data /work rw,relatime - ext4 /dev/sda1 rw
36 22 8:1 /x /my\040dir rw - ext4 /dev/sda1 rw
`
	has := func(dir string) bool { return mountinfoHas(bufio.NewScanner(strings.NewReader(info)), dir) }
	if !has("/work") || !has("/my dir") || has("/keys") || has("/wor") {
		t.Fatal("mountinfo parsing")
	}
}
