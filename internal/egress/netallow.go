package egress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

const netAllowName = "net_allow"

var netAllowSchema = json.RawMessage(`{"type":"object","properties":{` +
	`"host":{"type":"string","description":"Host name or IP address (no scheme or path)"},` +
	`"port":{"type":"integer","minimum":1,"maximum":65535,"description":"Default 443"}},` +
	`"required":["host"],"additionalProperties":false}`)

func netAllowSpec() core.ToolSpec {
	return builtinSpec(core.ToolDef{
		Name:        netAllowName,
		Description: "Ask the user to let the sandbox reach host:port for this session. Use after a command fails with \"bothy: egress to … denied\".",
		Schema:      netAllowSchema,
	}, core.Manifest{Title: "Allow network access", SideEffect: core.SEExternalSend, Taint: core.TaintSend})
}

func builtinSpec(def core.ToolDef, m core.Manifest) core.ToolSpec {
	return core.ToolSpec{Def: def, Source: "builtin", Manifest: m, Hash: defHash(def)}
}

func defHash(def core.ToolDef) string {
	b, err := core.CanonicalJSON(def)
	if err != nil {
		panic(fmt.Sprintf("egress: tool definition %s is not JSON: %v", def.Name, err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// decodeArgs decodes a JSON object strictly into v.
func decodeArgs(args json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: arguments: %w", core.ErrInvalid, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: arguments: trailing data", core.ErrInvalid)
	}
	return nil
}

// netAction is the Action of net_allow and of a held proxy CONNECT.
func netAction(h hostname, port int) (*core.Action, error) {
	payload, err := core.CanonicalJSON(map[string]any{"host": h.name, "port": port})
	if err != nil {
		return nil, err
	}
	return &core.Action{
		Tool: netAllowName, Kind: core.ActNet, SideEffect: core.SEExternalSend,
		Host: h.name, Port: port, Domains: []string{h.hostport(port)},
		Payload: payload, Fingerprint: core.ActionFingerprint(netAllowName, payload),
	}, nil
}

type netAllow struct{ s *Service }

var (
	_ core.Tool  = (*netAllow)(nil)
	_ core.Gated = (*netAllow)(nil)
)

func (t *netAllow) Spec() core.ToolSpec { return netAllowSpec() }

// Enabled hides net_allow without a sandbox: grants only matter to boxes.
func (t *netAllow) Enabled(env *core.ToolEnv) bool { return env.Selection.SandboxOK }

func (t *netAllow) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var in struct {
		Host string `json:"host"`
		Port *int   `json:"port"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	raw := in.Host
	if strings.Contains(raw, "/") {
		return nil, fmt.Errorf("%w: host must be a bare host name, without scheme or path", core.ErrInvalid)
	}
	port := 443
	if in.Port != nil {
		port = *in.Port
	}
	if hp, p, err := net.SplitHostPort(raw); err == nil {
		n, err := parsePort(p)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", core.ErrInvalid, err)
		}
		if in.Port != nil && *in.Port != n {
			return nil, fmt.Errorf("%w: host and port disagree", core.ErrInvalid)
		}
		raw, port = hp, n
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("%w: invalid port %d", core.ErrInvalid, port)
	}
	h, err := parseHost(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", core.ErrInvalid, err)
	}
	c := t.s.cfg().Egress
	hp := h.hostport(port)
	switch {
	case !portAllowed(&c, port):
		return nil, fmt.Errorf("%w: port %d is closed to the sandbox (egress.ports); only an admin can open it", core.ErrForbidden, port)
	case h.isIP() && addrProblem(h.ip, false) != "":
		return nil, fmt.Errorf("%w: %s is not a public address", core.ErrForbidden, h.name)
	case sharedTenant(h, c.SharedTenantSuffixes):
		return nil, fmt.Errorf("%w: %s is on a shared hosting domain; only an admin can allow it (egress.allow)", core.ErrForbidden, h.name)
	}
	if e, ok := matchAny(c.Deny, h, port, true, true); ok {
		return nil, fmt.Errorf("%w: %s is blocked by egress.deny (%s)", core.ErrForbidden, hp, e)
	}
	if _, ok := matchAny(c.Allow, h, port, true, false); ok {
		return nil, fmt.Errorf("%w: %s is already allowed by egress.allow; retry the command", core.ErrConflict, hp)
	}
	return netAction(h, port)
}

func (t *netAllow) Exec(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	h, err := parseHost(a.Host)
	if err != nil || a.Port < 1 || a.Port > 65535 {
		return nil, fmt.Errorf("%w: net_allow action without a valid host:port", core.ErrInvalid)
	}
	hp := h.hostport(a.Port)
	res := &core.ToolResult{Name: netAllowName}
	scope, err := t.activeGrant(ctx, env, h, a.Port)
	switch {
	case err != nil:
		return nil, err
	case scope != "" && env.Taint.Exposed():
		res.Content = "A grant for " + hp + " exists, but it is suspended while this session holds both private and untrusted data. " +
			"Start a clean session (Fork clean) to use it."
		res.IsError = true
	case scope == core.GrantPrincipal:
		res.Content = "The sandbox may now reach " + hp + " in every session. Retry the command."
	case scope != "":
		res.Content = "The sandbox may now reach " + hp + " for the rest of this session. Retry the command."
	default:
		res.Content, res.IsError = "No grant for "+hp+" is active, so the sandbox still cannot reach it.", true
	}
	return res, nil
}

// activeGrant finds the grant the box proxy would honour for host:port.
func (t *netAllow) activeGrant(ctx context.Context, env *core.ToolEnv, h hostname, port int) (core.GrantScope, error) {
	gs, err := t.s.store.Grants().Active(ctx, env.Principal.ID, env.Session, env.Schedule, t.s.clock.Now())
	if err != nil {
		return "", fmt.Errorf("net_allow: reading grants: %w", err)
	}
	var found core.GrantScope
	for _, g := range gs {
		if g.Kind != core.GrantDomain || g.MaxSE < core.SEExternalSend {
			continue
		}
		if p, ok := parsePattern(g.Value); ok && !p.wildcard && p.port == port && p.matches(h, port, false) {
			if g.Scope == core.GrantPrincipal {
				return core.GrantPrincipal, nil
			}
			found = g.Scope
		}
	}
	return found, nil
}
