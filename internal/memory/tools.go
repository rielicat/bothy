package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	memoryToolName        = "memory"
	sessionSearchToolName = "session_search"

	searchToolDefault = 8
	searchToolMax     = 20
	searchQueryMax    = 200
)

func makeSpec(def core.ToolDef, m core.Manifest) core.ToolSpec {
	canon, err := core.CanonicalJSON(def)
	if err != nil {
		panic(fmt.Sprintf("memory: tool %s: %v", def.Name, err))
	}
	sum := sha256.Sum256(canon)
	return core.ToolSpec{Def: def, Source: "builtin", Manifest: m, Hash: hex.EncodeToString(sum[:])}
}

// decodeArgs decodes a JSON object strictly: unknown fields and trailing data
// are errors the model can act on.
func decodeArgs(args json.RawMessage, v any) error {
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalidf("invalid arguments: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return invalidf("invalid arguments: trailing data after the JSON object")
	}
	return nil
}

func newAction(tool string, kind core.ActionKind, se core.SideEffect, payload any) (*core.Action, error) {
	canon, err := core.CanonicalJSON(payload)
	if err != nil {
		return nil, fmt.Errorf("%s: payload: %w", tool, err)
	}
	return &core.Action{
		Tool: tool, Kind: kind, SideEffect: se,
		Payload: canon, Fingerprint: core.ActionFingerprint(tool, canon),
	}, nil
}

// userOnly hides both tools from channels, including "standard" ones that
// registry filtering alone would let through: channels have no memory and
// no history of their own. Accounts and teammates have both.
func userOnly(env *core.ToolEnv) bool {
	return env.Principal.Kind == core.PrincipalUser || env.Principal.Kind == core.PrincipalPerson
}

// ---- memory ----

type memoryTool struct {
	s    *Service
	spec core.ToolSpec
}

func newMemoryTool(s *Service) *memoryTool {
	def := core.ToolDef{
		Name: memoryToolName,
		Description: "Save, change or delete a durable note that persists across conversations. " +
			"MEMORY: facts about projects, environment and the world. USER: facts about the user (preferences, background). " +
			"HEARTBEAT: the checklist the periodic heartbeat reviews. Each entry is one line. " +
			"op=add appends new; op=replace swaps the entry exactly equal to old for new; op=remove deletes the entry exactly equal to old. " +
			"Files have byte caps, so keep entries short.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"file":{"type":"string","enum":["MEMORY","USER","HEARTBEAT"]},` +
			`"op":{"type":"string","enum":["add","replace","remove"]},` +
			`"old":{"type":"string","description":"replace/remove: the exact existing entry"},` +
			`"new":{"type":"string","description":"add/replace: the new single-line entry"}},` +
			`"required":["file","op"],"additionalProperties":false}`),
	}
	return &memoryTool{s: s, spec: makeSpec(def, core.Manifest{
		Title:       "Update memory",
		SideEffect:  core.SELocalWrite,
		Core:        true,
		Timeout:     10 * time.Second,
		MaxOutputKB: 8,
	})}
}

func (t *memoryTool) Spec() core.ToolSpec            { return t.spec }
func (t *memoryTool) Enabled(env *core.ToolEnv) bool { return userOnly(env) }

// Plan dry-runs the op against the current file so a missing entry or a full
// file fails before any approval card is shown; Exec re-checks under the lock.
func (t *memoryTool) Plan(ctx context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var a core.MemoryOp
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	op, err := normalizeOp(a)
	if err != nil {
		return nil, err
	}
	p := env.Principal.ID
	if _, err := t.s.checkPrincipal(ctx, p); err != nil {
		return nil, err
	}
	if env.Principal.Kind == core.PrincipalPerson && op.File == core.FileHeartbeat {
		return nil, errTeammateHeartbeat
	}
	before, err := t.s.readFile(p, op.File)
	if err != nil {
		return nil, err
	}
	after, err := applyOp(before, op, env.Taint.Has(core.TaintPrivate))
	if err != nil {
		return nil, err
	}
	if err := t.s.checkCap(op.File, before, after); err != nil {
		return nil, err
	}
	act, err := newAction(memoryToolName, core.ActMemory, core.SELocalWrite, op)
	if err != nil {
		return nil, err
	}
	act.Diff = opDiff(op)
	return act, nil
}

func opDiff(op core.MemoryOp) string {
	name := fileName(op.File)
	var b strings.Builder
	b.WriteString("--- " + name + "\n+++ " + name + "\n")
	if op.Old != "" {
		b.WriteString("-" + op.Old + "\n")
	}
	if op.New != "" {
		b.WriteString("+" + op.New + "\n")
	}
	return b.String()
}

func (t *memoryTool) Exec(ctx context.Context, env *core.ToolEnv, act *core.Action) (*core.ToolResult, error) {
	var a core.MemoryOp
	if err := json.Unmarshal(act.Payload, &a); err != nil {
		return nil, fmt.Errorf("memory: payload: %w", err)
	}
	// Content written while untrusted text is in context keeps that label in
	// the history, whatever the approval outcome under F7.
	prov := core.ProvUser
	if env.Taint.Has(core.TaintUntrusted) {
		prov = core.ProvUntrusted
	}
	e, err := t.s.Apply(ctx, env.Principal.ID, env.Run, a, prov)
	if err != nil {
		var ue *userError
		if errors.As(err, &ue) || errors.Is(err, core.ErrForbidden) {
			return &core.ToolResult{Name: memoryToolName, Content: err.Error(), IsError: true}, nil
		}
		return nil, err
	}
	verb := map[string]string{"add": "Saved to", "replace": "Updated", "remove": "Removed from"}[e.Op]
	return &core.ToolResult{Name: memoryToolName, Content: fmt.Sprintf("%s %s (%d/%d bytes used).",
		verb, fileName(e.File), len(e.After), t.s.capOf(e.File))}, nil
}

// ---- session_search ----

type sessionSearchTool struct {
	s    *Service
	spec core.ToolSpec
}

type searchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

func newSessionSearchTool(s *Service) *sessionSearchTool {
	def := core.ToolDef{
		Name: sessionSearchToolName,
		Description: "Full-text search over your past conversations. " +
			"Returns matching snippets with the session title, date and message number.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","minLength":1,"maxLength":200,"description":"words to look for"},` +
			`"limit":{"type":"integer","minimum":1,"maximum":20,"description":"maximum hits (default 8)"}},` +
			`"required":["query"],"additionalProperties":false}`),
	}
	return &sessionSearchTool{s: s, spec: makeSpec(def, core.Manifest{
		Title:       "Search past conversations",
		SideEffect:  core.SERead,
		Taint:       core.TaintPrivate,
		Core:        true,
		Timeout:     15 * time.Second,
		MaxOutputKB: 16,
	})}
}

func (t *sessionSearchTool) Spec() core.ToolSpec            { return t.spec }
func (t *sessionSearchTool) Enabled(env *core.ToolEnv) bool { return userOnly(env) }

func (t *sessionSearchTool) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var a searchArgs
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	a.Query = strings.Join(strings.Fields(a.Query), " ")
	switch {
	case a.Query == "":
		return nil, invalidf("query is empty")
	case !utf8.ValidString(a.Query):
		return nil, invalidf("query is not valid UTF-8")
	case utf8.RuneCountInString(a.Query) > searchQueryMax:
		return nil, invalidf("query is longer than %d characters", searchQueryMax)
	case a.Limit < 0 || a.Limit > searchToolMax:
		return nil, invalidf("limit must be between 1 and %d", searchToolMax)
	}
	if a.Limit == 0 {
		a.Limit = searchToolDefault
	}
	return newAction(sessionSearchToolName, core.ActSearch, core.SERead, a)
}

// Exec searches only the run's own principal: a user's run never pulls in the
// group chats of spaces it owns.
func (t *sessionSearchTool) Exec(ctx context.Context, env *core.ToolEnv, act *core.Action) (*core.ToolResult, error) {
	var a searchArgs
	if err := json.Unmarshal(act.Payload, &a); err != nil {
		return nil, fmt.Errorf("session_search: payload: %w", err)
	}
	v := core.Viewer{User: env.User, Principals: []core.PrincipalID{env.Principal.ID}}
	hits, err := t.s.Search(ctx, v, a.Query, a.Limit)
	if err != nil {
		return nil, err
	}
	res := &core.ToolResult{Name: sessionSearchToolName}
	if len(hits) == 0 {
		res.Content = fmt.Sprintf("No past messages match %q.", a.Query)
		return res, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d matches for %q:\n", len(hits), a.Query)
	for i, h := range hits {
		title := h.Title
		if title == "" {
			title = "Untitled"
		}
		trust := ""
		if !h.Trusted {
			trust = " · untrusted"
			res.Untrusted = true
		}
		fmt.Fprintf(&b, "%d. [%s · %s · #%d%s] %s\n", i+1, title, h.At.UTC().Format(time.DateOnly), h.Seq, trust,
			strings.Join(strings.Fields(h.Snippet), " "))
	}
	res.Content = b.String()
	return res, nil
}
