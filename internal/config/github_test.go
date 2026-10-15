package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubConfig(t *testing.T) {
	c := Default()
	if c.GitHub.APIBase != "https://api.github.com" || c.GitHub.Checkout.MaxMB != 256 || c.GitHub.Checkout.MaxFiles != 20000 {
		t.Fatalf("defaults = %+v", c.GitHub)
	}
	for _, tc := range []struct {
		mut  func(*Config)
		path string
	}{
		{func(c *Config) { c.GitHub.APIBase = "" }, "github.api_base"},
		{func(c *Config) { c.GitHub.APIBase = "ftp://ghe.example.com" }, "github.api_base"},
		{func(c *Config) { c.GitHub.APIBase = "https://user:pw@ghe.example.com/api/v3" }, "github.api_base"},
		// Plain http would carry the token in the clear past the loopback.
		{func(c *Config) { c.GitHub.APIBase = "http://ghe.example.com/api/v3" }, "github.api_base"},
		{func(c *Config) { c.GitHub.Checkout.MaxMB = 0 }, "github.checkout.max_mb"},
		{func(c *Config) { c.GitHub.Checkout.MaxFiles = -1 }, "github.checkout.max_files"},
	} {
		c := Default()
		tc.mut(c)
		v := &vctx{layerOf: func(string) int { return 0 }}
		validate(c, v)
		if len(v.errs) != 1 || v.errs[0].path != tc.path {
			t.Errorf("%s: errors %+v", tc.path, v.errs)
		}
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bothy.yaml"), "github:\n  api_base: https://ghe.example.com/api/v3\n  checkout: {max_mb: 64}\n")
	m := load(t, dir, "BOTHY_GITHUB__CHECKOUT__MAX_FILES=500")
	g := m.Current().GitHub
	if g.APIBase != "https://ghe.example.com/api/v3" || g.Checkout.MaxMB != 64 || g.Checkout.MaxFiles != 500 {
		t.Fatalf("loaded = %+v", g)
	}
	// The UI may change the caps but never the API base.
	_, err := set(t, m, map[string]string{"github.api_base": `"http://169.254.169.254"`})
	if fe := fieldErrors(t, err); !strings.Contains(fe.Error(), "locked") {
		t.Fatalf("api_base from the UI: %v", fe)
	}
	mustSet(t, m, map[string]string{"github.checkout.max_mb": "128"})
	if m.Current().GitHub.Checkout.MaxMB != 128 {
		t.Fatalf("max_mb after set = %d", m.Current().GitHub.Checkout.MaxMB)
	}
}

func TestGitHubAPIBaseLoopbackHTTP(t *testing.T) {
	c := Default()
	c.GitHub.APIBase = "http://127.0.0.1:8081"
	v := &vctx{layerOf: func(string) int { return 0 }}
	validate(c, v)
	for _, e := range v.errs {
		if e.path == "github.api_base" {
			t.Fatalf("loopback http refused: %+v", e)
		}
	}
}
