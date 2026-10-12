package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/conformance"
)

func stdioServer(mut func(*config.MCPServer)) config.MCPServer {
	s := config.MCPServer{Name: "notes", Transport: "stdio",
		Package: &config.MCPPackage{Ecosystem: "npm", Name: "@acme/notes", Version: "1.2.3", Integrity: npmIntegrity},
		Command: []string{"node", "dist/index.js"}}
	mut(&s)
	return s
}

// The config loader rejects unpinned packages and run-time fetchers, and the
// manager refuses them again if one ever reaches it.
func TestUnpinnedPackagesAndFetchersRejected(t *testing.T) {
	bad := map[string]config.MCPServer{
		"latest":       stdioServer(func(s *config.MCPServer) { s.Package.Version = "latest" }),
		"range":        stdioServer(func(s *config.MCPServer) { s.Package.Version = "^1.2.3" }),
		"git ref":      stdioServer(func(s *config.MCPServer) { s.Package.Version = "github:acme/notes#main" }),
		"no integrity": stdioServer(func(s *config.MCPServer) { s.Package.Integrity = "" }),
		"bad integ":    stdioServer(func(s *config.MCPServer) { s.Package.Integrity = "md5-abc" }),
		"no package":   stdioServer(func(s *config.MCPServer) { s.Package = nil }),
		"flag name":    stdioServer(func(s *config.MCPServer) { s.Package.Name = "--global" }),
		"npx":          stdioServer(func(s *config.MCPServer) { s.Command = []string{"npx", "-y", "@acme/notes"} }),
		"npx path":     stdioServer(func(s *config.MCPServer) { s.Command = []string{"/usr/bin/npx", "@acme/notes"} }),
		"uvx":          stdioServer(func(s *config.MCPServer) { s.Command = []string{"uvx", "notes"} }),
		"bunx":         stdioServer(func(s *config.MCPServer) { s.Command = []string{"bunx", "notes"} }),
		"pipx run":     stdioServer(func(s *config.MCPServer) { s.Command = []string{"pipx", "run", "notes"} }),
		"no command":   stdioServer(func(s *config.MCPServer) { s.Command = nil }),
		"pypi range": stdioServer(func(s *config.MCPServer) {
			s.Package = &config.MCPPackage{Ecosystem: "pypi", Name: "n", Version: ">=1", Integrity: pypiIntegrity}
		}),
		"pypi integ": stdioServer(func(s *config.MCPServer) {
			s.Package = &config.MCPPackage{Ecosystem: "pypi", Name: "n", Version: "1.0", Integrity: npmIntegrity}
		}),
		"bad ecosys":  stdioServer(func(s *config.MCPServer) { s.Package.Ecosystem = "cargo" }),
		"bad egress":  stdioServer(func(s *config.MCPServer) { s.Egress = []string{"https://x.example/path"} }),
		"bad class":   stdioServer(func(s *config.MCPServer) { s.SideEffects = map[string]string{"default": "harmless"} }),
		"bad name":    stdioServer(func(s *config.MCPServer) { s.Name = "Notes!" }),
		"http no url": {Name: "docs", Transport: "http"},
		"http creds":  {Name: "docs", Transport: "http", URL: "https://user:pw@mcp.example.com/mcp"},
		"ftp url":     {Name: "docs", Transport: "http", URL: "ftp://mcp.example.com/"},
		"transport":   {Name: "docs", Transport: "ws"},
	}
	for name, c := range bad {
		if _, err := newServerSpec(c); err == nil {
			t.Errorf("%s: newServerSpec accepted %+v", name, c)
		}
	}
	if _, err := newServerSpec(stdioServer(func(s *config.MCPServer) { s.Egress = []string{"*.example.com"} })); err == nil {
		t.Error("wildcard egress accepted for a box")
	}
	good := stdioServer(func(s *config.MCPServer) { s.Egress = []string{"API.Example.com", "x.example:8443", "[2001:db8::1]"} })
	spec, err := newServerSpec(good)
	if err != nil {
		t.Fatal(err)
	}
	if spec.profile != core.ProfileNode || strings.Join(spec.egress, ",") != "api.example.com:443,x.example:8443,[2001:db8::1]:443" {
		t.Fatalf("spec = %+v", spec)
	}

	dir := t.TempDir()
	yaml := `mcp:
  servers:
    - name: latest
      transport: stdio
      package: {ecosystem: npm, name: notes, version: latest, integrity: "sha512-AA=="}
      command: [node, index.js]
    - name: fetcher
      transport: stdio
      package: {ecosystem: npm, name: notes, version: 1.0.0, integrity: "sha512-AA=="}
      command: [npx, notes]
`
	if err := os.WriteFile(filepath.Join(dir, "bothy.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cm, err := config.Load(ctx, config.Options{DataDir: dir, Env: func() []string { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, e := range cm.Effective().Errors {
		msgs = append(msgs, e.Path+": "+e.Message)
	}
	joined := strings.Join(msgs, "\n")
	if !strings.Contains(joined, "exact version") || !strings.Contains(joined, "npx fetches packages at run time") {
		t.Fatalf("config errors = %s", joined)
	}
	for _, s := range cm.Current().MCP.Servers {
		if s.Package != nil && s.Package.Version == "latest" || len(s.Command) > 0 && s.Command[0] == "npx" {
			t.Fatalf("config let %+v through", s)
		}
	}
}

func TestToolName(t *testing.T) {
	if got := toolName("docs", "search"); got != "mcp__docs__search" {
		t.Fatalf("toolName = %q", got)
	}
	long := strings.Repeat("a", 70)
	got := toolName("docs", long)
	full := "mcp__docs__" + long
	if len(got) != 64 || got[:55] != full[:55] || got[55] != '_' || got[56:] != sha256hex([]byte(full))[:8] {
		t.Fatalf("long toolName = %q", got)
	}
	if got := toolName("docs", strings.Repeat("b", 53)); len(got) != 64 || !strings.HasSuffix(got, strings.Repeat("b", 53)) {
		t.Fatalf("64-char name changed: %q", got)
	}
	a, b := toolName("docs", "a.b"), toolName("docs", "a_b")
	if a == b || !strings.HasPrefix(a, "mcp__docs__a_b_") || b != "mcp__docs__a_b" {
		t.Fatalf("sanitised names collide or are malformed: %q %q", a, b)
	}
}

func TestPunycode(t *testing.T) {
	for in, want := range map[string]string{
		"example.com":        "example.com",
		"Bücher.example":     "xn--bcher-kva.example",
		"münchen.de":         "xn--mnchen-3ya.de",
		"аpple.com":          "xn--pple-43d.com", // Cyrillic а
		"例え.テスト":             "xn--r8jz45g.xn--zckzah",
		"xn--bcher-kva.test": "xn--bcher-kva.test",
	} {
		got, err := toASCII(in)
		if err != nil || got != want {
			t.Errorf("toASCII(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := toASCII("bad\xff"); err == nil {
		t.Error("invalid UTF-8 accepted")
	}
}

func TestDefinitionHash(t *testing.T) {
	yes := true
	base := &mcpsdk.Tool{Name: "t", Description: "d", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"b": 1, "a": 2}}}
	_, canon, h1, err := definitionOf(base)
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != `{"description":"d","inputSchema":{"properties":{"a":2,"b":1},"type":"object"},"name":"t"}` {
		t.Fatalf("canonical = %s", canon)
	}
	for name, mut := range map[string]func(*mcpsdk.Tool){
		"title":        func(t *mcpsdk.Tool) { t.Title = "T" },
		"description":  func(t *mcpsdk.Tool) { t.Description = "d2" },
		"schema":       func(t *mcpsdk.Tool) { t.InputSchema = map[string]any{"type": "object"} },
		"outputSchema": func(t *mcpsdk.Tool) { t.OutputSchema = map[string]any{"type": "object"} },
		"annotations":  func(t *mcpsdk.Tool) { t.Annotations = &mcpsdk.ToolAnnotations{DestructiveHint: &yes} },
	} {
		c := *base
		mut(&c)
		if _, _, h2, _ := definitionOf(&c); h2 == h1 {
			t.Errorf("changing %s kept the hash", name)
		}
	}
	c := *base
	c.Meta = mcpsdk.Meta{"x": 1}
	c.Icons = []mcpsdk.Icon{{Source: "https://example.com/i.png"}}
	if _, _, h2, _ := definitionOf(&c); h2 != h1 {
		t.Error("fields outside the pinned set changed the hash")
	}
	d, _, _, _ := definitionOf(&mcpsdk.Tool{Name: "n"})
	if string(d.InputSchema) != `{"type":"object"}` {
		t.Fatalf("missing schema = %s", d.InputSchema)
	}

	ro := false
	for _, tc := range []struct {
		a    *mcpsdk.ToolAnnotations
		want core.SideEffect
	}{
		{nil, core.SERead},
		{&mcpsdk.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &yes}, core.SERead},
		{&mcpsdk.ToolAnnotations{DestructiveHint: &yes}, core.SEIrreversible},
		{&mcpsdk.ToolAnnotations{OpenWorldHint: &yes}, core.SEExternalSend},
		{&mcpsdk.ToolAnnotations{DestructiveHint: &ro, OpenWorldHint: &ro}, core.SERead},
	} {
		d, _, _, _ := definitionOf(&mcpsdk.Tool{Name: "n", Annotations: tc.a})
		if got := d.annotationFloor(); got != tc.want {
			t.Errorf("annotationFloor(%+v) = %s, want %s", tc.a, got, tc.want)
		}
	}
}

func TestDiffDefinitions(t *testing.T) {
	old := json.RawMessage(`{"description":"a","name":"t","title":"x"}`)
	cur := json.RawMessage(`{"description":"b","name":"t","annotations":{"readOnlyHint":true}}`)
	got := diffDefinitions(old, cur)
	if len(got) != 3 || got[0].Field != "annotations" || got[1].Field != "description" || got[2].Field != "title" || got[2].New != nil {
		t.Fatalf("diff = %+v", got)
	}
	big := json.RawMessage(`"` + strings.Repeat("x", 5000) + `"`)
	if c := clip(big); len(c) > maxDiffValue+16 || !json.Valid(c) {
		t.Fatalf("clip = %d bytes, valid %v", len(c), json.Valid(c))
	}
}

func TestSuggestedClass(t *testing.T) {
	spec := &serverSpec{sideEffects: map[string]string{"default": "local_write", "read_*": "read", "read_secret*": "external_send", "read_all": "irreversible"}}
	def := &toolDefinition{}
	for tool, want := range map[string]core.SideEffect{
		"write_file":   core.SELocalWrite,
		"read_file":    core.SERead,
		"read_secrets": core.SEExternalSend,
		"read_all":     core.SEIrreversible,
	} {
		if got := spec.suggestedClass(tool, def); got != want {
			t.Errorf("suggestedClass(%s) = %s, want %s", tool, got, want)
		}
	}
	if got := (&serverSpec{}).suggestedClass("x", def); got != core.SEExternalSend {
		t.Errorf("no config: %s, want external_send", got)
	}
}

func TestLockChecks(t *testing.T) {
	lock := []byte(`{"packages":{"node_modules/@a/b":{"version":"1.0.0","integrity":"sha512-x"}}}`)
	if err := checkNPMLock(lock, "@a/b", "1.0.0", "sha512-x"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][3]string{{"@a/c", "1.0.0", "sha512-x"}, {"@a/b", "1.0.1", "sha512-x"}, {"@a/b", "1.0.0", "sha512-y"}} {
		if err := checkNPMLock(lock, args[0], args[1], args[2]); err == nil {
			t.Errorf("checkNPMLock(%v) passed", args)
		}
	}
	req := []byte("# header\nMCP_Notes[cli]==0.4.0 ; python_version >= '3.10' \\\n    --hash=sha256:aa \\\n    --hash=sha256:bb\nother==1 \\\n    --hash=sha256:cc\n")
	if err := checkPyPILock(req, "mcp-notes", "0.4.0", "sha256:bb"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][3]string{{"mcp-notes", "0.4.0", "sha256:cc"}, {"mcp-notes", "0.5.0", "sha256:aa"}, {"absent", "1", "sha256:aa"}} {
		if err := checkPyPILock(req, args[0], args[1], args[2]); err == nil {
			t.Errorf("checkPyPILock(%v) passed", args)
		}
	}
}

func TestToolConformance(t *testing.T) {
	spec, err := newServerSpec(config.MCPServer{Name: "mail", Transport: "http", URL: "https://mail.example/mcp",
		Headers: map[string]string{"Authorization": "Bearer secret:mail_token", "X-Org": "secret:org.id"}})
	if err != nil {
		t.Fatal(err)
	}
	def := json.RawMessage(`{"description":"Send mail.","inputSchema":{"properties":{"to":{"type":"string"}},"type":"object"},"name":"send"}`)
	h := newHarness(t, nil)
	tool, err := newTool(h.m, spec, &core.MCPPin{Server: "mail", Tool: "send", Hash: "h", Definition: def, Status: core.PinApproved,
		SideEffect: core.SEExternalSend, FieldMap: map[string]string{"recipient": "/to"}})
	if err != nil {
		t.Fatal(err)
	}
	if s := tool.Spec(); strings.Join(s.Manifest.Secrets, ",") != "mail_token,org.id" || s.Manifest.Title != "MCP mail: send" {
		t.Fatalf("spec = %+v", s)
	}
	conformance.Tool(t, tool, []conformance.ToolCase{
		{Name: "send", Args: json.RawMessage(`{"to":"bob@example.com"}`)},
		{Name: "no arguments", Args: json.RawMessage(`{}`)},
		{Name: "not an object", Args: json.RawMessage(`["x"]`), WantErr: true},
		{Name: "trailing data", Args: json.RawMessage(`{} {}`), WantErr: true},
	})
	a, err := tool.Plan(context.Background(), nil, json.RawMessage(`{"to":"bob@example.com","n":1.50}`))
	if err != nil || a.Recipient != "bob@example.com" || string(a.Payload) != `{"n":1.50,"to":"bob@example.com"}` {
		t.Fatalf("Plan = %+v, %v", a, err)
	}
}

func TestPointer(t *testing.T) {
	var doc any
	_ = json.Unmarshal([]byte(`{"a":{"b/c":[{"~k":"v"}]},"":"root"}`), &doc)
	for ptr, want := range map[string]any{"/a/b~1c/0/~0k": "v", "/": "root", "/a/b~1c/1": nil, "/a/b~1c/00": nil, "x": nil} {
		got, ok, _ := pointer(doc, ptr)
		if want == nil && ok || want != nil && got != want {
			t.Errorf("pointer(%q) = %v, %v", ptr, got, ok)
		}
	}
	_ = json.Unmarshal([]byte(`{"a":{"To":"x","to":"y"},"b":{"TO":"z"}}`), &doc)
	for ptr, want := range map[string]string{"/a/to": "To", "/b/to": "TO", "/a/To": "to"} {
		if _, ok, twin := pointer(doc, ptr); ok || twin != want {
			t.Errorf("pointer(%q) twin = %q, %v; want %q", ptr, twin, ok, want)
		}
	}
}
