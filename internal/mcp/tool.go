package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bothyhq/bothy/internal/core"
)

const maxResultBytes = 1 << 20

// tool adapts one approved MCP tool definition to core.Tool. It is always
// lazy (reached through tool_search/use_tool), never offered to restricted
// spaces, and gated on its server being ready.
type tool struct {
	m        *Manager
	server   string
	name     string // the MCP tool name
	hash     string // approved definition hash
	class    core.SideEffect
	fieldMap map[string]string
	trusted  bool
	spec     core.ToolSpec
}

var (
	_ core.Tool  = (*tool)(nil)
	_ core.Gated = (*tool)(nil)
)

func newTool(m *Manager, s *serverSpec, p *core.MCPPin) (*tool, error) {
	def, err := parseDefinition(p.Definition)
	if err != nil {
		return nil, fmt.Errorf("stored definition: %w", err)
	}
	class := max(p.SideEffect, def.annotationFloor())
	if !class.Valid() {
		class = core.SEExternalSend
	}
	var taint core.Taint
	if !s.trustedOutput {
		taint |= core.TaintUntrusted
	}
	if p.Private || s.privateOutput(p.Tool) {
		taint |= core.TaintPrivate
	}
	title := def.Title
	if title == "" && def.Annotations != nil {
		title = def.Annotations.Title
	}
	if title == "" {
		title = p.Tool
	}
	desc := def.Description
	if desc == "" {
		desc = fmt.Sprintf("Tool %s of the MCP server %s.", p.Tool, s.name)
	}
	td := core.ToolDef{Name: toolName(s.name, p.Tool), Description: desc, Schema: def.InputSchema}
	canon, err := core.CanonicalJSON(td)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canon)
	return &tool{
		m: m, server: s.name, name: p.Tool, hash: p.Hash, class: class, fieldMap: maps.Clone(p.FieldMap), trusted: s.trustedOutput,
		spec: core.ToolSpec{Def: td, Source: source(s.name), Hash: hex.EncodeToString(sum[:]), Manifest: core.Manifest{
			Title: "MCP " + s.name + ": " + title, SideEffect: class, Secrets: secretNames(s.headers), Taint: taint,
		}},
	}, nil
}

// secretNames lists the vault names a server's header references use.
func secretNames(headers map[string]string) []string {
	var out []string
	for _, v := range headers {
		_, rest, ok := strings.Cut(v, "secret:")
		if !ok {
			continue
		}
		end := strings.IndexFunc(rest, func(r rune) bool { return !secretNameRune(r) })
		if end < 0 {
			end = len(rest)
		}
		if end > 0 && !slices.Contains(out, rest[:end]) {
			out = append(out, rest[:end])
		}
	}
	slices.Sort(out)
	return out
}

func secretNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-'
}

func (t *tool) Spec() core.ToolSpec { return t.spec }

func (t *tool) Enabled(env *core.ToolEnv) bool {
	if env != nil && env.Principal.Restricted() {
		return false
	}
	s := t.m.server(t.server)
	if s == nil || s.spec == nil || !s.exposes(t.name, t.hash) {
		return false
	}
	return s.spec.transport != "stdio" || t.m.sandboxOK.Load()
}

// Plan normalises the arguments (a JSON object) into an mcp Action of the
// approved class. Fields named by the approval's field_map are copied into
// Recipient, URL and Host so first-contact checks and cards see them.
func (t *tool) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	raw := bytes.TrimSpace(args)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: arguments: %w", core.ErrInvalid, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: arguments: trailing data after the JSON object", core.ErrInvalid)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: arguments must be a JSON object", core.ErrInvalid)
	}
	payload, err := core.CanonicalJSON(json.RawMessage(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: arguments: %w", core.ErrInvalid, err)
	}
	a := &core.Action{Tool: t.spec.Def.Name, Kind: core.ActMCP, SideEffect: t.class, MCPServer: t.server, MCPTool: t.name,
		Payload: payload, Fingerprint: core.ActionFingerprint(t.spec.Def.Name, payload)}
	if err := applyFieldMap(a, obj, t.fieldMap); err != nil {
		return nil, err
	}
	return a, nil
}

func applyFieldMap(a *core.Action, args map[string]any, fm map[string]string) error {
	for _, key := range slices.Sorted(maps.Keys(fm)) {
		ptr := fm[key]
		v, ok, twin := pointer(args, ptr)
		if twin != "" {
			return fmt.Errorf("%w: argument %q is ambiguous next to %s (field_map %s)", core.ErrInvalid, twin, ptr, key)
		}
		if !ok || v == nil {
			continue
		}
		// A mapped value the Action cannot carry is refused rather than
		// skipped: skipping would hide a recipient or URL from first-contact
		// checks and from the approval card.
		s, err := fieldValue(key, v)
		if err != nil {
			return fmt.Errorf("%w: argument %s (field_map %s): %w", core.ErrInvalid, ptr, key, err)
		}
		if s == "" {
			continue
		}
		switch key {
		case "recipient":
			a.Recipient = s
		case "host":
			host, port := s, 0
			if h, p, err := net.SplitHostPort(s); err == nil {
				host = h
				port, _ = strconv.Atoi(p)
			}
			ascii, err := toASCII(host)
			if err != nil {
				return fmt.Errorf("%w: argument %s: %w", core.ErrInvalid, ptr, err)
			}
			a.Host, a.Port = ascii, port
		case "url":
			u, err := url.Parse(s)
			if err != nil || u.Host == "" {
				return fmt.Errorf("%w: argument %s is not an absolute URL", core.ErrInvalid, ptr)
			}
			ascii, err := toASCII(u.Hostname())
			if err != nil {
				return fmt.Errorf("%w: argument %s: %w", core.ErrInvalid, ptr, err)
			}
			port, _ := strconv.Atoi(u.Port())
			if port == 0 {
				port = map[string]int{"https": 443, "http": 80}[strings.ToLower(u.Scheme)]
			}
			a.URL, a.Host, a.Port = s, ascii, port
		}
	}
	return nil
}

// fieldValue renders a mapped argument as the Action field's text: strings
// and numbers as they are, and for recipient a list of them joined with ", "
// (a new combination is then a new recipient). URLs and hosts must be single
// values, since the Action holds only one.
func fieldValue(key string, v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case []any:
		if key != "recipient" {
			break
		}
		parts := make([]string, 0, len(x))
		for _, e := range x {
			s, err := fieldValue("", e)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ", "), nil
	}
	return "", fmt.Errorf("want a string, got %T", v)
}

// pointer resolves an RFC 6901 JSON pointer. twin is a key on the way that
// differs from the pointer's token only in case: servers that match keys
// case-insensitively (Go's encoding/json, for one) may read it instead of
// the value the Action shows, so callers refuse such arguments.
func pointer(doc any, ptr string) (v any, ok bool, twin string) {
	if ptr == "" {
		return doc, true, ""
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, false, ""
	}
	for _, tok := range strings.Split(ptr[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch v := doc.(type) {
		case map[string]any:
			for k := range v {
				if k != tok && strings.EqualFold(k, tok) {
					return nil, false, k
				}
			}
			x, ok := v[tok]
			if !ok {
				return nil, false, ""
			}
			doc = x
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(v) || len(tok) > 1 && tok[0] == '0' {
				return nil, false, ""
			}
			doc = v[i]
		default:
			return nil, false, ""
		}
	}
	return doc, true, ""
}

// Exec calls the tool through the caller's runtime session. Cancelling ctx
// (a /stop, a timeout) makes the SDK send notifications/cancelled for the
// request. A session whose server now reports a different definition for the
// tool refuses the call and records the change for review.
func (t *tool) Exec(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	s := t.m.server(t.server)
	if s == nil || s.spec == nil {
		return nil, fmt.Errorf("mcp server %s: %w", t.server, core.ErrNotFound)
	}
	if env.Principal.Restricted() {
		return nil, fmt.Errorf("mcp tools are not available to restricted spaces: %w", core.ErrForbidden)
	}
	if s.spec.transport == "stdio" && !t.m.sandboxOK.Load() {
		return nil, fmt.Errorf("mcp server %s: %w", t.server, core.ErrNoSandbox)
	}
	if !s.exposes(t.name, t.hash) {
		return &core.ToolResult{Name: t.spec.Def.Name, IsError: true, Content: fmt.Sprintf(
			"The MCP tool %s is not available right now: it was disabled or changed, or the server %s is not ready.", t.name, t.server)}, nil
	}
	c, inst, release, err := t.m.acquire(ctx, s, env)
	if err != nil {
		return nil, err
	}
	defer release()
	d, ok := c.tools[t.name]
	if !ok {
		return &core.ToolResult{Name: t.spec.Def.Name, IsError: true,
			Content: fmt.Sprintf("The MCP server %s no longer offers the tool %s.", t.server, t.name)}, nil
	}
	if d.hash != t.hash {
		t.m.observe(context.WithoutCancel(ctx), s, map[string]discovered{t.name: d}, false)
		return &core.ToolResult{Name: t.spec.Def.Name, IsError: true,
			Content: "The server changed this tool's definition, so it is disabled until an admin reviews the change."}, nil
	}
	res, err := c.sess.CallTool(ctx, &mcpsdk.CallToolParams{Name: t.name, Arguments: a.Payload})
	if err != nil {
		if errors.Is(err, mcpsdk.ErrConnectionClosed) || errors.Is(err, mcpsdk.ErrSessionMissing) {
			t.m.drop(inst, c)
		}
		return nil, fmt.Errorf("mcp %s/%s: %w", t.server, t.name, err)
	}
	return &core.ToolResult{Name: t.spec.Def.Name, Content: resultText(res), IsError: res.IsError, Untrusted: !t.trusted}, nil
}

// resultText flattens a tool result to text. Binary content is described,
// not inlined; structured content is used when there is no other content.
func resultText(res *mcpsdk.CallToolResult) string {
	var b strings.Builder
	for i, c := range res.Content {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch v := c.(type) {
		case *mcpsdk.TextContent:
			b.WriteString(v.Text)
		case *mcpsdk.ImageContent:
			fmt.Fprintf(&b, "[image %s, %d bytes, not shown]", v.MIMEType, len(v.Data))
		case *mcpsdk.AudioContent:
			fmt.Fprintf(&b, "[audio %s, %d bytes, not shown]", v.MIMEType, len(v.Data))
		case *mcpsdk.ResourceLink:
			fmt.Fprintf(&b, "[resource %s %s]", v.Name, v.URI)
		case *mcpsdk.EmbeddedResource:
			switch r := v.Resource; {
			case r == nil:
				b.WriteString("[empty resource]")
			case r.Text != "":
				b.WriteString(r.Text)
			default:
				fmt.Fprintf(&b, "[resource %s %s, %d bytes, not shown]", r.URI, r.MIMEType, len(r.Blob))
			}
		default:
			fmt.Fprintf(&b, "[%T content not shown]", c)
		}
		if b.Len() > maxResultBytes {
			break
		}
	}
	if len(res.Content) == 0 && res.StructuredContent != nil {
		if j, err := json.Marshal(res.StructuredContent); err == nil {
			b.Write(j)
		}
	}
	out := b.String()
	if len(out) > maxResultBytes {
		out = out[:maxResultBytes] + "\n[… output truncated]"
	}
	return out
}
