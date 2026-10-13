package fakes

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/rielicat/bothy/internal/core"
)

var _ core.ToolRegistry = (*Registry)(nil)

// Registry is a map-backed core.ToolRegistry with the real filtering rules
// reduced to: trigger, principal kind (SpaceOK), sandbox (Exec), Gated.
//
// ForRun returns every tool passing those filters (core or lazy alike),
// sorted by name. Search matches the query as a case-insensitive substring of
// the name or description. Register rejects duplicate names (core.ErrConflict)
// and specs without a name or a valid side effect (core.ErrInvalid).
type Registry struct {
	core.ToolRegistry
	Tools map[string]core.Tool

	mu sync.Mutex
}

func (r *Registry) Register(t core.Tool) error {
	spec := t.Spec()
	name := spec.Def.Name
	if name == "" || !spec.Manifest.SideEffect.Valid() {
		return fmt.Errorf("tool %q: %w: name and side effect are required", name, core.ErrInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.Tools[name]; ok {
		return fmt.Errorf("tool %q: %w", name, core.ErrConflict)
	}
	if r.Tools == nil {
		r.Tools = map[string]core.Tool{}
	}
	r.Tools[name] = t
	return nil
}

func (r *Registry) UnregisterSource(source string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, t := range r.Tools {
		if t.Spec().Source == source {
			delete(r.Tools, name)
		}
	}
}

// ReplaceSource drops source's tools and registers the new ones under one
// lock; invalid or colliding tools are skipped and reported.
func (r *Registry) ReplaceSource(source string, tools []core.Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, t := range r.Tools {
		if t.Spec().Source == source {
			delete(r.Tools, name)
		}
	}
	if r.Tools == nil {
		r.Tools = map[string]core.Tool{}
	}
	var errs []error
	for _, t := range tools {
		spec := t.Spec()
		name := spec.Def.Name
		switch _, dup := r.Tools[name]; {
		case name == "" || !spec.Manifest.SideEffect.Valid() || spec.Source != source:
			errs = append(errs, fmt.Errorf("tool %q: %w: name, side effect and source %q are required", name, core.ErrInvalid, source))
		case dup:
			errs = append(errs, fmt.Errorf("tool %q: %w", name, core.ErrConflict))
		default:
			r.Tools[name] = t
		}
	}
	return errors.Join(errs...)
}

func (r *Registry) Get(name string) (core.Tool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.Tools[name]
	return t, ok
}

func (r *Registry) sorted() []core.Tool {
	r.mu.Lock()
	out := make([]core.Tool, 0, len(r.Tools))
	for _, t := range r.Tools {
		out = append(out, t)
	}
	r.mu.Unlock()
	slices.SortFunc(out, func(a, b core.Tool) int { return cmp.Compare(a.Spec().Def.Name, b.Spec().Def.Name) })
	return out
}

func allowed(t core.Tool, env *core.ToolEnv, sel core.Selection) bool {
	if env == nil {
		env = &core.ToolEnv{}
	}
	m := t.Spec().Manifest
	if len(m.Triggers) > 0 && !slices.Contains(m.Triggers, env.Trigger) {
		return false
	}
	if env.Principal.Restricted() && !m.SpaceOK {
		return false
	}
	if m.Exec && !sel.SandboxOK {
		return false
	}
	if g, ok := t.(core.Gated); ok && !g.Enabled(env) {
		return false
	}
	return true
}

func (r *Registry) ForRun(env *core.ToolEnv, sel core.Selection) []core.ToolSpec {
	var out []core.ToolSpec
	for _, t := range r.sorted() {
		if allowed(t, env, sel) {
			out = append(out, t.Spec())
		}
	}
	return out
}

func (r *Registry) Available(name string, env *core.ToolEnv, sel core.Selection) bool {
	t, ok := r.Get(name)
	return ok && allowed(t, env, sel)
}

// Search returns at most limit hits (all when limit <= 0).
func (r *Registry) Search(query string, env *core.ToolEnv, sel core.Selection, limit int) []core.ToolSpec {
	q := strings.ToLower(query)
	var out []core.ToolSpec
	for _, t := range r.sorted() {
		if limit > 0 && len(out) >= limit {
			break
		}
		spec := t.Spec()
		hit := strings.Contains(strings.ToLower(spec.Def.Name), q) || strings.Contains(strings.ToLower(spec.Def.Description), q)
		if hit && allowed(t, env, sel) {
			out = append(out, spec)
		}
	}
	return out
}

func (r *Registry) All() []core.ToolSpec {
	tools := r.sorted()
	out := make([]core.ToolSpec, len(tools))
	for i, t := range tools {
		out[i] = t.Spec()
	}
	return out
}
