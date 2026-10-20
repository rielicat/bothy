package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// The gateway, not only the key picker, refuses an MCP server that would
// send one of Bothy's own keys to its address.
func TestConfigRefusesSendingBothysOwnKeyToAnMCPServer(t *testing.T) {
	h := newHarness(t).setup()
	m, err := config.Load(t.Context(), config.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.mu.Lock()
	h.cfg.mgr = m
	h.cfg.mu.Unlock()
	server := func(header string) map[string]any {
		return map[string]any{"set": map[string]any{"mcp.servers.docs": map[string]any{
			"transport": "http", "url": "https://mcp.example.com/mcp", "headers": map[string]string{"Authorization": header},
		}}}
	}

	rec := h.do("PATCH", "/api/config", server("Bearer secret:github_ghc_01m4wa97e4s0p3qgcng95bcyhf"), h.admin)
	expect(t, rec, http.StatusBadRequest)
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || len(env.Fields) != 1 ||
		env.Fields[0].Path != "mcp.servers.docs.headers.Authorization" || !strings.Contains(env.Fields[0].Message, config.SystemKeyMessage) {
		t.Fatalf("refusal = %s", rec.Body.String())
	}
	if len(m.Current().MCP.Servers) != 0 {
		t.Fatal("the refused server was saved")
	}
	expect(t, h.do("PATCH", "/api/config", server("Bearer secret:docs_key"), h.admin), http.StatusOK)
}

// The global key list marks Bothy's own keys, which the MCP key picker
// leaves out.
func TestVaultListMarksBothysOwnKeys(t *testing.T) {
	h := newHarness(t).setup()
	for _, name := range []string{"telegram_bot_token", "linear_key"} {
		expect(t, h.do("PUT", "/api/vault/global/"+name, dto.VaultPutReq{Value: "qa-value-0001"}, h.admin), http.StatusOK)
	}
	system := map[string]bool{}
	for _, k := range decodeAs[[]core.SecretMeta](t, h.do("GET", "/api/vault?scope=global", nil, h.admin)) {
		system[k.Name] = k.System
	}
	if len(system) != 2 || !system["telegram_bot_token"] || system["linear_key"] {
		t.Fatalf("system = %v", system)
	}
}

// connRefsGitHub is a GitHub service whose connections use refs as their keys.
type connRefsGitHub struct {
	core.GitHub
	refs []string
}

func (g connRefsGitHub) Connections(context.Context) ([]*core.GitHubConnection, error) {
	var out []*core.GitHubConnection
	for _, r := range g.refs {
		out = append(out, &core.GitHubConnection{SecretRef: r})
	}
	return out, nil
}

// A key a chat app or a GitHub connection uses is Bothy's own under any
// name: the key list marks it and config refuses sending it to an MCP server.
func TestKeysBothysSettingsUseAreItsOwn(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Channels.Telegram.Token = "secret:tg_main" }).setup()
	h.srv.d.GitHub = connRefsGitHub{refs: []string{"secret:gh_pat"}}
	for _, name := range []string{"tg_main", "gh_pat", "linear_key"} {
		expect(t, h.do("PUT", "/api/vault/global/"+name, dto.VaultPutReq{Value: "qa-value-0001"}, h.admin), http.StatusOK)
	}
	system := map[string]bool{}
	for _, k := range decodeAs[[]core.SecretMeta](t, h.do("GET", "/api/vault?scope=global", nil, h.admin)) {
		system[k.Name] = k.System
	}
	if !system["tg_main"] || !system["gh_pat"] || system["linear_key"] {
		t.Fatalf("system = %v", system)
	}

	m, err := config.Load(t.Context(), config.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h.cfg.mu.Lock()
	h.cfg.mgr = m
	h.cfg.mu.Unlock()
	rec := h.do("PATCH", "/api/config", map[string]any{"set": map[string]any{"mcp.servers.docs": map[string]any{
		"transport": "http", "url": "https://mcp.example.com/mcp", "headers": map[string]string{"Authorization": "secret:gh_pat"},
	}}}, h.admin)
	expect(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), config.SystemKeyMessage) {
		t.Fatalf("refusal = %s", rec.Body.String())
	}
}
