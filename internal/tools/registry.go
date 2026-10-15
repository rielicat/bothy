package tools

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/rielicat/bothy/internal/core"
)

const (
	toolSearchName = "tool_search"
	useToolName    = "use_tool"
)

var (
	toolNameRE     = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	knownTriggers  = []core.Trigger{core.TrigMessage, core.TrigSchedule, core.TrigHeartbeat, core.TrigAPI}
	knownRoles     = []core.Role{"", core.RoleOwner, core.RoleAdmin, core.RoleMember}
	schemaVersions = []string{"", "http://json-schema.org/draft-07/schema#", "https://json-schema.org/draft-07/schema#", "https://json-schema.org/draft/2020-12/schema"}
)

// Model profiles (SPEC §5.6). A nil names list means "every tool whose
// manifest says Core"; meta adds tool_search/use_tool when lazy tools exist.
type profile struct {
	names []string
	meta  bool
}

var (
	standardProfile = profile{meta: true}
	compactProfile  = profile{names: []string{"fs_read", "fs_write", "memory", "shell", "web_fetch", "web_search"}, meta: true}
	degradedProfile = profile{names: []string{"fs_read", "fs_write", "memory", "shell", "web_search"}}
)

func profileOf(sel core.Selection) profile {
	switch {
	case sel.Degraded:
		return degradedProfile
	case sel.SmallModel:
		return compactProfile
	}
	return standardProfile
}

func (p profile) inBlock(e *entry) bool {
	if p.names == nil {
		return e.spec.Manifest.Core
	}
	return slices.Contains(p.names, e.spec.Def.Name)
}

type registryState struct {
	mu    sync.RWMutex
	tools map[string]*entry
	names []string // sorted
}

type entry struct {
	tool    core.Tool
	spec    core.ToolSpec // normalised copy
	schema  *jsonschema.Resolved
	meta    bool
	wrapped *validated
}

func (r *Registry) add(t core.Tool, meta bool) error {
	if t == nil {
		return fmt.Errorf("%w: nil tool", core.ErrInvalid)
	}
	e, err := newEntry(t, meta)
	if err != nil {
		return err
	}
	name := e.spec.Def.Name
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if _, ok := r.state.tools[name]; ok {
		return fmt.Errorf("tool %q: %w: already registered", name, core.ErrConflict)
	}
	r.state.tools[name] = e
	i, _ := slices.BinarySearch(r.state.names, name)
	r.state.names = slices.Insert(r.state.names, i, name)
	return nil
}

func newEntry(t core.Tool, meta bool) (*entry, error) {
	spec := cloneSpec(t.Spec())
	name := spec.Def.Name
	bad := func(format string, args ...any) error {
		return fmt.Errorf("tool %q: %w: %s", name, core.ErrInvalid, fmt.Sprintf(format, args...))
	}
	m := spec.Manifest
	switch {
	case !toolNameRE.MatchString(name):
		return nil, bad("name must match %s", toolNameRE)
	case strings.TrimSpace(spec.Def.Description) == "":
		return nil, bad("description is empty")
	case !validSource(spec.Source):
		return nil, bad(`source %q is not "builtin", "mcp:<server>" or "skill:<name>"`, spec.Source)
	case strings.TrimSpace(m.Title) == "":
		return nil, bad("manifest title is empty")
	case !m.SideEffect.Valid():
		return nil, bad("manifest side effect %d is invalid", m.SideEffect)
	case !slices.Contains(knownRoles, m.MinRole):
		return nil, bad("unknown min role %q", m.MinRole)
	case m.Timeout < 0 || m.MaxOutputKB < 0:
		return nil, bad("negative timeout or output cap")
	case m.Taint&^(core.TaintPrivate|core.TaintUntrusted|core.TaintSend) != 0:
		return nil, bad("unknown taint bits %d", m.Taint)
	}
	for _, tr := range m.Triggers {
		if !slices.Contains(knownTriggers, tr) {
			return nil, bad("unknown trigger %q", tr)
		}
	}
	canon, err := core.CanonicalJSON(spec.Def.Schema)
	if err != nil {
		return nil, bad("schema is not JSON: %v", err)
	}
	spec.Def.Schema = canon
	schema, err := compileSchema(canon)
	if err != nil {
		return nil, bad("%v", err)
	}
	def, err := core.CanonicalJSON(spec.Def)
	if err != nil {
		return nil, bad("definition does not encode: %v", err)
	}
	sum := sha256.Sum256(def)
	spec.Hash = hex.EncodeToString(sum[:])
	e := &entry{tool: t, spec: spec, schema: schema, meta: meta}
	e.wrapped = &validated{e: e}
	return e, nil
}

func validSource(s string) bool {
	if s == "builtin" {
		return true
	}
	for _, p := range []string{"mcp:", "skill:"} {
		if rest, ok := strings.CutPrefix(s, p); ok && rest != "" {
			return true
		}
	}
	return false
}

func compileSchema(raw json.RawMessage) (*jsonschema.Resolved, error) {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("schema is not a JSON object: %w", err)
	}
	if top["type"] != "object" {
		return nil, errors.New(`schema must have "type": "object"`)
	}
	if err := checkTypes(top); err != nil {
		return nil, err
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("schema does not parse: %w", err)
	}
	if !slices.Contains(schemaVersions, s.Schema) {
		return nil, fmt.Errorf("unsupported $schema %q", s.Schema)
	}
	res, err := s.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("schema does not compile: %w", err)
	}
	return res, nil
}

var jsonTypes = []string{"array", "boolean", "integer", "null", "number", "object", "string"}

// checkTypes rejects unknown "type" names anywhere in a schema, which the
// validator would otherwise accept and then fail every call against.
func checkTypes(v any) error {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			switch k {
			case "const", "enum", "default", "examples":
				continue
			case "type":
				if sub, isMap := e.(map[string]any); isMap { // a property named "type"
					if err := checkTypes(sub); err != nil {
						return err
					}
					continue
				}
				if err := checkTypeNames(e); err != nil {
					return err
				}
			default:
				if err := checkTypes(e); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, e := range x {
			if err := checkTypes(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkTypeNames(v any) error {
	names, ok := v.([]any)
	if !ok {
		names = []any{v}
	}
	for _, n := range names {
		if s, isStr := n.(string); !isStr || !slices.Contains(jsonTypes, s) {
			return fmt.Errorf("schema has unknown type %v", n)
		}
	}
	return nil
}

func cloneSpec(s core.ToolSpec) core.ToolSpec {
	s.Def.Schema = bytes.Clone(s.Def.Schema)
	s.Manifest.FSPaths = slices.Clone(s.Manifest.FSPaths)
	s.Manifest.Domains = slices.Clone(s.Manifest.Domains)
	s.Manifest.Secrets = slices.Clone(s.Manifest.Secrets)
	s.Manifest.Triggers = slices.Clone(s.Manifest.Triggers)
	return s
}

// UnregisterSource implements core.ToolRegistry. The registry's own
// tool_search and use_tool are never removed.
func (r *Registry) UnregisterSource(source string) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.unregisterLocked(source)
}

func (r *Registry) unregisterLocked(source string) {
	r.state.names = slices.DeleteFunc(r.state.names, func(name string) bool {
		e := r.state.tools[name]
		if e.meta || e.spec.Source != source {
			return false
		}
		delete(r.state.tools, name)
		return true
	})
}

// ReplaceSource implements core.ToolRegistry. The old tools of source go and
// the valid new ones arrive under one lock, so readers see one set or the
// other. A tool that fails validation, belongs to another source or collides
// with another source's name is skipped and reported in the joined error.
func (r *Registry) ReplaceSource(source string, tools []core.Tool) error {
	var errs []error
	entries := make([]*entry, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			errs = append(errs, fmt.Errorf("%w: nil tool", core.ErrInvalid))
			continue
		}
		e, err := newEntry(t, false)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if e.spec.Source != source {
			errs = append(errs, fmt.Errorf("tool %q: %w: source %q, want %q", e.spec.Def.Name, core.ErrInvalid, e.spec.Source, source))
			continue
		}
		entries = append(entries, e)
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.unregisterLocked(source)
	for _, e := range entries {
		name := e.spec.Def.Name
		if _, ok := r.state.tools[name]; ok {
			errs = append(errs, fmt.Errorf("tool %q: %w: already registered", name, core.ErrConflict))
			continue
		}
		r.state.tools[name] = e
		i, _ := slices.BinarySearch(r.state.names, name)
		r.state.names = slices.Insert(r.state.names, i, name)
	}
	return errors.Join(errs...)
}

// Get implements core.ToolRegistry. The returned tool validates arguments
// against the compiled JSON Schema before Plan (§5.7 b) and normalises the
// planned Action (canonical payload, fingerprint, side effect never below
// the manifest), so callers need no schema validator of their own.
func (r *Registry) Get(name string) (core.Tool, bool) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	e, ok := r.state.tools[name]
	if !ok {
		return nil, false
	}
	return e.wrapped, true
}

// ForRun implements core.ToolRegistry.
func (r *Registry) ForRun(env *core.ToolEnv, sel core.Selection) []core.ToolSpec {
	prof := profileOf(sel)
	var out, meta []core.ToolSpec
	lazy := false
	for _, e := range r.snapshot() {
		if !available(e, env, sel) {
			continue
		}
		switch {
		case e.meta:
			meta = append(meta, cloneSpec(e.spec))
		case prof.inBlock(e):
			out = append(out, cloneSpec(e.spec))
		default:
			lazy = true
		}
	}
	if lazy && prof.meta && len(meta) > 0 {
		out = append(out, meta...)
		slices.SortFunc(out, func(a, b core.ToolSpec) int { return cmp.Compare(a.Def.Name, b.Def.Name) })
	}
	return out
}

// Available implements core.ToolRegistry.
func (r *Registry) Available(name string, env *core.ToolEnv, sel core.Selection) bool {
	r.state.mu.RLock()
	e, ok := r.state.tools[name]
	r.state.mu.RUnlock()
	return ok && available(e, env, sel)
}

// snapshot returns the entries sorted by name. Filters run on it outside the
// lock because a tool's Gated check may call back into other services.
func (r *Registry) snapshot() []*entry {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]*entry, len(r.state.names))
	for i, name := range r.state.names {
		out[i] = r.state.tools[name]
	}
	return out
}

// All implements core.ToolRegistry.
func (r *Registry) All() []core.ToolSpec {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]core.ToolSpec, 0, len(r.state.names))
	for _, name := range r.state.names {
		out = append(out, cloneSpec(r.state.tools[name].spec))
	}
	return out
}

// available applies the run filters: trigger, role, principal kind
// (restricted spaces get SpaceOK read tools only), sandbox, kill switch and
// the tool's own Gated check.
func available(e *entry, env *core.ToolEnv, sel core.Selection) bool {
	if env == nil {
		env = &core.ToolEnv{}
	}
	m := e.spec.Manifest
	switch {
	case len(m.Triggers) > 0 && !slices.Contains(m.Triggers, env.Trigger):
		return false
	case m.MinRole != "" && roleRank(env.Role) < roleRank(m.MinRole):
		return false
	case env.Principal.Restricted() && (!m.SpaceOK || m.SideEffect != core.SERead):
		return false
	case m.Exec && !sel.SandboxOK:
		return false
	case sel.Paused && m.SideEffect != core.SERead:
		return false
	}
	if g, ok := e.tool.(core.Gated); ok && !g.Enabled(env) {
		return false
	}
	return true
}

func roleRank(r core.Role) int {
	switch r {
	case core.RoleOwner:
		return 3
	case core.RoleAdmin:
		return 2
	case core.RoleMember:
		return 1
	}
	return 0
}

// validated is what Get hands out: the registered tool behind schema
// validation and Action normalisation.
type validated struct{ e *entry }

func (v *validated) Spec() core.ToolSpec { return cloneSpec(v.e.spec) }

func (v *validated) Plan(ctx context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	args, err := v.e.validate(args)
	if err != nil {
		return nil, err
	}
	a, err := v.e.tool.Plan(ctx, env, args)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, fmt.Errorf("tool %q: Plan returned no action", v.e.spec.Def.Name)
	}
	return normalizeAction(v.e.spec, a)
}

func (v *validated) Exec(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	if a == nil || a.Tool != v.e.spec.Def.Name {
		return nil, fmt.Errorf("%w: action is not for tool %q", core.ErrInvalid, v.e.spec.Def.Name)
	}
	return v.e.tool.Exec(ctx, env, a)
}

func (v *validated) Enabled(env *core.ToolEnv) bool {
	g, ok := v.e.tool.(core.Gated)
	return !ok || g.Enabled(env)
}

// validate checks args against the schema and returns them, with empty args
// read as {}.
func (e *entry) validate(args json.RawMessage) (json.RawMessage, error) {
	name := e.spec.Def.Name
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return nil, fmt.Errorf("%w: arguments for %s are not valid JSON: %w", core.ErrInvalid, name, err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: arguments for %s must be a JSON object", core.ErrInvalid, name)
	}
	if unquoteJSONArgs(e.schema.Schema(), obj) {
		b, err := json.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("%w: arguments for %s: %w", core.ErrInvalid, name, err)
		}
		args = b
	}
	if err := e.schema.Validate(v); err != nil {
		return nil, fmt.Errorf("%w: arguments for %s: %w", core.ErrInvalid, name, err)
	}
	return args, nil
}

// unquoteJSONArgs replaces a top-level argument the schema types as an
// array or object, but that the model sent as a string holding exactly that
// JSON ("paths": "[\"a.ts\"]"), with the decoded value. Models do this often
// enough that refusing costs a round trip. It reports whether it changed obj.
func unquoteJSONArgs(s *jsonschema.Schema, obj map[string]any) bool {
	if s == nil {
		return false
	}
	changed := false
	for key, prop := range s.Properties {
		str, ok := obj[key].(string)
		if !ok || prop == nil || (prop.Type != "array" && prop.Type != "object") {
			continue
		}
		var dec any
		if json.Unmarshal([]byte(str), &dec) != nil {
			continue
		}
		_, isArr := dec.([]any)
		_, isObj := dec.(map[string]any)
		if prop.Type == "array" && isArr || prop.Type == "object" && isObj {
			obj[key], changed = dec, true
		}
	}
	return changed
}

func normalizeAction(spec core.ToolSpec, a *core.Action) (*core.Action, error) {
	if a.Tool != spec.Def.Name {
		return nil, fmt.Errorf("tool %q: Plan returned an action for %q", spec.Def.Name, a.Tool)
	}
	if a.Kind == "" {
		a.Kind = core.ActGeneric
	}
	if !a.SideEffect.Valid() || a.SideEffect < spec.Manifest.SideEffect {
		a.SideEffect = spec.Manifest.SideEffect
	}
	payload := a.Payload
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = json.RawMessage(`{}`)
	}
	canon, err := core.CanonicalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("tool %q: action payload is not JSON: %w", spec.Def.Name, err)
	}
	a.Payload = canon
	a.Fingerprint = core.ActionFingerprint(a.Tool, canon)
	return a, nil
}
