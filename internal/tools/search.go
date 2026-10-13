package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/rielicat/bothy/internal/core"
)

const (
	searchLimit    = 8
	maxSearchQuery = 200
)

var stopWords = []string{"a", "an", "and", "for", "in", "of", "on", "or", "the", "to", "tool", "tools", "with"}

// Search implements core.ToolRegistry: available tools (tool_search and
// use_tool excluded) ranked by keyword hits, name hits weighing more than
// description hits; ties sort by name. An empty query lists every tool.
// limit <= 0 means no limit.
func (r *Registry) Search(query string, env *core.ToolEnv, sel core.Selection, limit int) []core.ToolSpec {
	q := strings.ToLower(strings.TrimSpace(query))
	terms := tokenize(q)
	type hit struct {
		score int
		spec  core.ToolSpec
	}
	var hits []hit
	for _, e := range r.snapshot() {
		if e.meta || !available(e, env, sel) {
			continue
		}
		s := score(e.spec, q, terms)
		if len(terms) > 0 && s == 0 {
			continue
		}
		hits = append(hits, hit{s, e.spec})
	}
	slices.SortStableFunc(hits, func(a, b hit) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.spec.Def.Name, b.spec.Def.Name))
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]core.ToolSpec, len(hits))
	for i, h := range hits {
		out[i] = cloneSpec(h.spec)
	}
	return out
}

func tokenize(s string) []string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := words[:0]
	for _, w := range words {
		if len(w) > 1 && !slices.Contains(stopWords, w) && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

func score(spec core.ToolSpec, query string, terms []string) int {
	name := strings.ToLower(spec.Def.Name)
	nameWords := tokenize(name)
	descWords := tokenize(spec.Def.Description + " " + spec.Manifest.Title)
	desc := strings.ToLower(spec.Def.Description + " " + spec.Manifest.Title)
	s := 0
	if query != "" && query == name {
		s += 100
	}
	for _, t := range terms {
		switch {
		case slices.Contains(nameWords, t):
			s += 10
		case strings.Contains(name, t):
			s += 6
		}
		switch {
		case slices.Contains(descWords, t):
			s += 3
		case strings.Contains(desc, t):
			s++
		}
	}
	return s
}

// toolSearch finds lazy tools; its Exec reads the registry with the run's
// Selection so results match what use_tool can call.
type toolSearch struct{ reg *Registry }

func (t *toolSearch) Spec() core.ToolSpec {
	return core.ToolSpec{
		Def: core.ToolDef{
			Name:        toolSearchName,
			Description: "Find tools that are not in your tool list. Returns each match's name, description and argument schema; call one with use_tool.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"What you need to do, in a few keywords"}},"required":["query"],"additionalProperties":false}`),
		},
		Source: "builtin",
		Manifest: core.Manifest{
			Title: "Search tools", SideEffect: core.SERead, SpaceOK: true, Core: true,
			Timeout: 10 * time.Second, MaxOutputKB: 32,
		},
	}
}

func (t *toolSearch) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var in struct {
		Query string `json:"query"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(in.Query)
	if len(q) > maxSearchQuery {
		return nil, fmt.Errorf("%w: query is longer than %d bytes", core.ErrInvalid, maxSearchQuery)
	}
	return newAction(toolSearchName, core.ActSearch, core.SERead, map[string]any{"query": q})
}

type searchHit struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

func (t *toolSearch) Exec(_ context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	var in struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(a.Payload, &in); err != nil {
		return nil, fmt.Errorf("%w: tool_search payload: %w", core.ErrInvalid, err)
	}
	var sel core.Selection
	if env != nil {
		sel = env.Selection
	}
	specs := t.reg.Search(in.Query, env, sel, searchLimit)
	if len(specs) == 0 {
		return &core.ToolResult{Content: fmt.Sprintf("No tools matched %q.", in.Query)}, nil
	}
	hits := make([]searchHit, len(specs))
	for i, s := range specs {
		hits[i] = searchHit{Name: s.Def.Name, Description: s.Def.Description, Schema: s.Def.Schema}
	}
	b, err := json.Marshal(hits)
	if err != nil {
		return nil, fmt.Errorf("encode tool_search hits: %w", err)
	}
	return &core.ToolResult{Content: fmt.Sprintf("Found %d tool(s). Call one with use_tool {\"name\": \"<tool>\", \"args\": {…}}.\n%s", len(hits), b)}, nil
}

// useTool only contributes its schema to the tools block: the runner unwraps
// use_tool{name,args} into a call of the named tool before validation, so
// planning or executing it directly is always an error.
type useTool struct{}

var errUseTool = fmt.Errorf("%w: use_tool is unwrapped by the runner and never runs itself", core.ErrInvalid)

func (useTool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Def: core.ToolDef{
			Name:        useToolName,
			Description: "Call a tool found with tool_search.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"Tool name from tool_search"},"args":{"type":"object","description":"Arguments matching that tool's schema"}},"required":["name"],"additionalProperties":false}`),
		},
		Source: "builtin",
		Manifest: core.Manifest{
			Title: "Use a found tool", SideEffect: core.SERead, SpaceOK: true, Core: true, Timeout: 10 * time.Second,
		},
	}
}

func (useTool) Plan(context.Context, *core.ToolEnv, json.RawMessage) (*core.Action, error) {
	return nil, errUseTool
}

func (useTool) Exec(context.Context, *core.ToolEnv, *core.Action) (*core.ToolResult, error) {
	return nil, errUseTool
}
