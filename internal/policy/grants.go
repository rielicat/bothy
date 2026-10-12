package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// interpreters run arbitrary code given in their arguments, so an exec grant
// for them pins the whole argv instead of just the program (§5.8).
var interpreters = []string{"bash", "sh", "zsh", "python", "python3", "node", "env", "xargs", "sudo", "uv", "npx"}

// NarrowGrant implements core.Policy.
func (e *Engine) NarrowGrant(a *core.Action, p core.PrincipalID, scope core.GrantScope, scopeID string, by core.UserID) (core.Grant, bool) {
	g, ok := narrowGrant(e.cfg.Current(), a, p, scope, scopeID, by, e.clock.Now())
	if ok {
		e.narrowed.add(grantKey(g))
	}
	return g, ok
}

// narrowGrant builds the narrowest grant that still lets the same kind of
// call through again. It never yields a whole-tool grant.
func narrowGrant(c *config.Config, a *core.Action, p core.PrincipalID, scope core.GrantScope, scopeID string, by core.UserID, now time.Time) (core.Grant, bool) {
	if a == nil || p == "" || a.Tool == "" || !a.SideEffect.Valid() || a.SideEffect >= core.SEFinancial {
		return core.Grant{}, false
	}
	switch scope {
	case core.GrantPrincipal:
		if scopeID != "" || a.SideEffect == core.SEIrreversible {
			return core.Grant{}, false
		}
	case core.GrantSession, core.GrantSchedule:
		if scopeID == "" {
			return core.Grant{}, false
		}
	default:
		return core.Grant{}, false
	}
	g := core.Grant{Principal: p, Scope: scope, ScopeID: scopeID, MaxSE: a.SideEffect, CreatedBy: by, CreatedAt: now}
	doc, _ := decodePayload(a.Payload)
	tool := func(pattern map[string]string) (core.Grant, bool) {
		g.Kind, g.Tool, g.Pattern = core.GrantTool, a.Tool, pattern
		return g, true
	}
	switch a.Kind {
	case core.ActExec:
		argv, ok := stringList(mustPointer(doc, "/argv"))
		if !ok || len(argv) == 0 {
			argv = a.Argv
		}
		if len(argv) == 0 || argv[0] == "" {
			return core.Grant{}, false
		}
		pattern := map[string]string{"/argv/0": globEscape(argv[0])}
		if slices.Contains(interpreters, path.Base(argv[0])) {
			whole, err := core.CanonicalJSON(argv)
			if err != nil {
				return core.Grant{}, false
			}
			pattern["/argv"] = globEscape(string(whole))
		}
		// python carries its code next to an argv that only names the file
		// the code is written to; the grant is for that code alone.
		if code, ok := mustPointer(doc, "/code").(string); ok {
			pattern["/code"] = globEscape(code)
		}
		return tool(pattern)
	case core.ActFSWrite, core.ActFSDelete:
		target, ok := mustPointer(doc, "/path").(string)
		if !ok || target == "" {
			return core.Grant{}, false
		}
		target = cleanRel(target)
		if strings.HasPrefix(target, "..") {
			return core.Grant{}, false
		}
		// A file at the workspace root has no parent narrower than the
		// whole workspace, so it is pinned exactly.
		if dir := path.Dir(target); dir != "." {
			return tool(map[string]string{"/path": globEscape(dir) + "/*"})
		}
		return tool(map[string]string{"/path": globEscape(target)})
	case core.ActHTTP, core.ActNet:
		hosts := actionHosts(a)
		if len(hosts) != 1 || hosts[0].host == "" || hosts[0].port == 0 {
			return core.Grant{}, false
		}
		if scope == core.GrantPrincipal && underSuffix(hosts[0].host, c.Egress.SharedTenantSuffixes) {
			return core.Grant{}, false
		}
		g.Kind, g.Value = core.GrantDomain, hosts[0].String()
		return g, true
	case core.ActSend:
		if a.Recipient == "" {
			return core.Grant{}, false
		}
		g.Kind, g.Value = core.GrantRecipient, a.Recipient
		return g, true
	case core.ActMCP:
		if a.Recipient != "" {
			if ptrs := pointersTo(doc, a.Recipient); len(ptrs) == 1 && ptrs[0] != "" {
				return tool(map[string]string{ptrs[0]: globEscape(a.Recipient)})
			}
		}
	}
	canon, err := core.CanonicalJSON(a.Payload)
	if err != nil || isEmptyPayload(canon) {
		return core.Grant{}, false
	}
	return tool(map[string]string{"": globEscape(string(canon))})
}

func mustPointer(doc any, ptr string) any {
	v, _ := pointer(doc, ptr)
	return v
}

func isEmptyPayload(canon []byte) bool {
	s := string(canon)
	return s == "{}" || s == "null" || s == "[]" || s == `""`
}

// grantMatches reports whether g covers the action (side-effect ceiling aside).
func grantMatches(g *core.Grant, x *input) bool {
	switch g.Kind {
	case core.GrantTool:
		if g.Tool != x.a.Tool || len(g.Pattern) == 0 {
			return false
		}
		for ptr, glob := range g.Pattern {
			v, ok := pointerText(x.payload, ptr)
			if !ok || !globMatch(glob, v) {
				return false
			}
		}
		return true
	case core.GrantDomain:
		if x.a.Kind != core.ActHTTP && x.a.Kind != core.ActNet || len(x.hosts) == 0 {
			return false
		}
		p, ok := parseHostPattern(g.Value)
		if !ok || p.port == 0 {
			return false
		}
		for _, h := range x.hosts {
			if !p.match(h) {
				return false
			}
		}
		return true
	case core.GrantRecipient:
		return x.a.Recipient != "" && g.Value == x.a.Recipient
	}
	return false
}

// Grant implements core.Policy: it validates the grant (never whole-tool,
// never admin or financial, wildcard and shared-tenant domains only from
// admins at principal scope), stores it and audits grant.create.
func (e *Engine) Grant(ctx context.Context, g core.Grant) (core.GrantID, error) {
	if err := e.validateGrant(ctx, &g); err != nil {
		return "", err
	}
	key := grantKey(g)
	if g.ID == "" {
		g.ID = core.GrantID(core.NewID(core.PrefixGrant))
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = e.clock.Now()
	}
	if err := e.store.Grants().Add(ctx, &g); err != nil {
		return "", fmt.Errorf("store grant: %w", err)
	}
	via := "api"
	if e.narrowed.take(key) {
		via = "card"
	}
	e.record(ctx, core.AuditEntry{
		Actor: "user:" + string(g.CreatedBy), Action: "grant.create", Target: string(g.ID), Outcome: "ok",
		Session: sessionOf(g),
		Detail: auditDetail(map[string]any{
			"principal_id": g.Principal, "scope": g.Scope, "scope_id": g.ScopeID, "kind": g.Kind,
			"tool": g.Tool, "pattern": g.Pattern, "value": g.Value, "max_se": g.MaxSE.String(),
			"allow_when_tainted": g.AllowWhenTainted, "via": via,
		}),
	})
	return g.ID, nil
}

func sessionOf(g core.Grant) core.SessionID {
	if g.Scope == core.GrantSession {
		return core.SessionID(g.ScopeID)
	}
	return ""
}

func (e *Engine) validateGrant(ctx context.Context, g *core.Grant) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: grant: %s", core.ErrInvalid, fmt.Sprintf(format, args...))
	}
	switch {
	case g.Principal == "":
		return bad("principal is required")
	case g.CreatedBy == "":
		return bad("created_by is required")
	case g.Uses < 0 || g.MaxUses < 0:
		return bad("negative use counts")
	case !g.MaxSE.Valid():
		return bad("invalid max side effect")
	case g.MaxSE >= core.SEFinancial:
		return bad("grants never cover %s actions", g.MaxSE)
	case g.AllowWhenTainted && g.Kind != core.GrantRecipient:
		return bad("allow_when_tainted is only valid for recipient grants")
	}
	switch g.Scope {
	case core.GrantPrincipal:
		if g.ScopeID != "" {
			return bad("principal grants have no scope id")
		}
	case core.GrantSession, core.GrantSchedule:
		if g.ScopeID == "" {
			return bad("%s grants need a scope id", g.Scope)
		}
	default:
		return bad("unknown scope %q", g.Scope)
	}
	switch g.Kind {
	case core.GrantTool:
		if g.Tool == "" || len(g.Pattern) == 0 {
			return bad("tool grants need a tool and a pattern; whole-tool grants are not allowed")
		}
		trivial := true
		for ptr, glob := range g.Pattern {
			if ptr != "" && !strings.HasPrefix(ptr, "/") {
				return bad("pattern key %q is not a JSON pointer", ptr)
			}
			if !validGlob(glob) {
				return bad("invalid glob %q", glob)
			}
			trivial = trivial && trivialGlob(glob)
		}
		if trivial {
			return bad("a pattern that matches everything is a whole-tool grant")
		}
		g.Value = ""
	case core.GrantDomain:
		p, ok := parseHostPattern(g.Value)
		if !ok || p.port == 0 {
			return bad("domain grants need a host:port value, got %q", g.Value)
		}
		// Wildcards exist only as admin principal grants. A shared-tenant
		// host may be granted per session (the proxy ignores such grants),
		// but persistently only by an admin.
		shared := underSuffix(p.host, e.cfg.Current().Egress.SharedTenantSuffixes)
		if p.wildcard && g.Scope != core.GrantPrincipal ||
			(p.wildcard || shared) && g.Scope == core.GrantPrincipal && !e.isAdmin(ctx, g.CreatedBy) {
			return bad("%s needs a principal grant created by an admin", g.Value)
		}
		g.Value, g.Tool, g.Pattern = p.String(), "", nil
	case core.GrantRecipient:
		if g.Value == "" {
			return bad("recipient grants need a value")
		}
		if g.AllowWhenTainted && hasGlobMeta(g.Value) {
			return bad("allow_when_tainted needs an exact recipient")
		}
		g.Tool, g.Pattern = "", nil
	default:
		return bad("unknown kind %q", g.Kind)
	}
	return nil
}

func (e *Engine) isAdmin(ctx context.Context, u core.UserID) bool {
	usr, err := e.store.Users().Get(ctx, u)
	return err == nil && !usr.Disabled && usr.Role.IsAdmin()
}

// Revoke implements core.Policy.
func (e *Engine) Revoke(ctx context.Context, v core.Viewer, id core.GrantID, by core.UserID) error {
	if err := e.store.Grants().Revoke(ctx, v, id, e.clock.Now()); err != nil {
		return fmt.Errorf("revoke grant %s: %w", id, err)
	}
	e.record(ctx, core.AuditEntry{Actor: "user:" + string(by), Action: "grant.revoke", Target: string(id), Outcome: "ok"})
	return nil
}

// grantKey identifies a grant by content, ignoring id and timestamps.
func grantKey(g core.Grant) string {
	b, err := core.CanonicalJSON(map[string]any{
		"p": g.Principal, "s": g.Scope, "sid": g.ScopeID, "k": g.Kind, "t": g.Tool,
		"pat": g.Pattern, "v": g.Value, "se": int(g.MaxSE), "by": g.CreatedBy,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// recentSet remembers grants NarrowGrant built recently, so Grant can audit
// them as coming from an approval card. Bounded and short-lived.
type recentSet struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]time.Time
}

const (
	recentMax = 512
	recentTTL = time.Hour
)

func newRecentSet(now func() time.Time) *recentSet {
	return &recentSet{now: now, m: map[string]time.Time{}}
}

func (r *recentSet) add(k string) {
	if k == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for key, at := range r.m {
		if now.Sub(at) > recentTTL {
			delete(r.m, key)
		}
	}
	if len(r.m) >= recentMax {
		var oldest string
		var at time.Time
		for key, t := range r.m {
			if oldest == "" || t.Before(at) {
				oldest, at = key, t
			}
		}
		delete(r.m, oldest)
	}
	r.m[k] = now
}

func (r *recentSet) take(k string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.m[k]
	delete(r.m, k)
	return ok && r.now().Sub(at) <= recentTTL
}

func auditDetail(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
