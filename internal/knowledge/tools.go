package knowledge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	searchToolName  = "knowledge_search"
	notesToolName   = "notes"
	toolHitsDefault = 8
	toolHitsMax     = 20
	knownReposShown = 20
)

func makeSpec(def core.ToolDef, m core.Manifest) core.ToolSpec {
	canon, err := core.CanonicalJSON(def)
	if err != nil {
		panic(fmt.Sprintf("knowledge: tool %s: %v", def.Name, err))
	}
	sum := sha256.Sum256(canon)
	return core.ToolSpec{Def: def, Source: "builtin", Manifest: m, Hash: hex.EncodeToString(sum[:])}
}

// decodeArgs decodes a JSON object strictly: unknown fields and trailing
// data are errors the model can act on.
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
	return &core.Action{Tool: tool, Kind: kind, SideEffect: se, Payload: canon,
		Fingerprint: core.ActionFingerprint(tool, canon)}, nil
}

// toolError is an error result the model can act on (a stale plan, a repo
// removed meanwhile); the call itself did not fail.
func toolError(name string, err error) *core.ToolResult {
	return &core.ToolResult{Name: name, Content: err.Error(), IsError: true}
}

// ownViewer is the run's own principal only: a user's run never reaches the
// repos of spaces it owns.
func ownViewer(env *core.ToolEnv) core.Viewer {
	return core.Viewer{User: env.User, Principals: []core.PrincipalID{env.Principal.ID}}
}

// resolveRepo finds an indexed repo of the run's principal by name; the
// error lists the known ones so the model can retry.
func (s *Service) resolveRepo(ctx context.Context, env *core.ToolEnv, name string) (*core.IndexedRepo, error) {
	norm, err := normalizeRepo(name)
	if err == nil {
		r, gerr := s.d.Repo.ByName(ctx, ownViewer(env), env.Principal.ID, norm)
		if gerr == nil {
			return r, nil
		}
		if !errors.Is(gerr, core.ErrNotFound) {
			return nil, gerr
		}
	}
	repos, lerr := s.d.Repo.List(ctx, ownViewer(env), env.Principal.ID)
	if lerr != nil {
		return nil, lerr
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos[:min(len(repos), knownReposShown)] {
		names = append(names, r.Repo)
	}
	if len(names) == 0 {
		return nil, invalidf("no repository is indexed yet")
	}
	return nil, invalidf("%q is not an indexed repository; indexed: %s", name, strings.Join(names, ", "))
}

// ---- knowledge_search ----

type searchTool struct {
	s    *Service
	spec core.ToolSpec
}

type searchArgs struct {
	Query string `json:"query"`
	Repo  string `json:"repo,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

func newSearchTool(s *Service) *searchTool {
	def := core.ToolDef{
		Name: searchToolName,
		Description: "Search the indexed code and docs of your repositories (checked-out repos and indexed workspace folders). " +
			"Returns ranked snippets with line numbers and citations like owner/repo@sha:path#L10-L42; cite them when you use them. " +
			"Set repo (owner/name) to search one repository.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","minLength":1,"maxLength":200,"description":"words or identifiers to look for"},` +
			`"repo":{"type":"string","maxLength":201,"description":"owner/name: search only this repository"},` +
			`"limit":{"type":"integer","minimum":1,"maximum":20,"description":"maximum results (default 8)"}},` +
			`"required":["query"],"additionalProperties":false}`),
	}
	// Untrusted: anyone may have authored repository content. Private: the
	// index holds the principal's workspace, which fs_read reads as private,
	// so a search must not dodge the trifecta floor that a read would raise.
	return &searchTool{s: s, spec: makeSpec(def, core.Manifest{
		Title:       "Search indexed code and docs",
		SideEffect:  core.SERead,
		Taint:       core.TaintPrivate | core.TaintUntrusted,
		SpaceOK:     true,
		Core:        true,
		Timeout:     20 * time.Second,
		MaxOutputKB: 40,
	})}
}

func (t *searchTool) Spec() core.ToolSpec { return t.spec }

func (t *searchTool) Enabled(env *core.ToolEnv) bool {
	return t.s.cfg().Enabled && t.s.hasRepos(context.Background(), env.Principal.ID)
}

func (t *searchTool) Plan(ctx context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var a searchArgs
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	q, err := normalizeQuery(a.Query)
	if err != nil {
		return nil, err
	}
	a.Query = q
	if a.Limit < 0 || a.Limit > toolHitsMax {
		return nil, invalidf("limit must be between 1 and %d", toolHitsMax)
	}
	if a.Limit == 0 {
		a.Limit = toolHitsDefault
	}
	if a.Repo != "" {
		r, err := t.s.resolveRepo(ctx, env, a.Repo)
		if err != nil {
			return nil, err
		}
		a.Repo = r.Repo
	}
	return newAction(searchToolName, core.ActSearch, core.SERead, a)
}

func (t *searchTool) Exec(ctx context.Context, env *core.ToolEnv, act *core.Action) (*core.ToolResult, error) {
	var a searchArgs
	if err := json.Unmarshal(act.Payload, &a); err != nil {
		return nil, fmt.Errorf("knowledge_search: payload: %w", err)
	}
	q := core.KnowledgeQuery{Text: a.Query, Principal: env.Principal.ID, Limit: a.Limit}
	if a.Repo != "" {
		r, err := t.s.resolveRepo(ctx, env, a.Repo)
		if err != nil {
			return toolError(searchToolName, err), nil
		}
		q.Repo = r.ID
	}
	hits, err := t.s.Search(ctx, ownViewer(env), q)
	if err != nil {
		return nil, err
	}
	switch {
	case q.Repo != 0:
		t.s.touch(ctx, env.Session, q.Repo)
	case len(hits) > 0:
		t.s.touch(ctx, env.Session, hits[0].RepoID)
	}
	return &core.ToolResult{Name: searchToolName, Content: formatHits(a.Query, hits), Untrusted: true}, nil
}

// ---- notes ----

type notesTool struct {
	s    *Service
	spec core.ToolSpec
}

func newNotesTool(s *Service) *notesTool {
	def := core.ToolDef{
		Name: notesToolName,
		Description: "Read or update your own project notes for an indexed repository, kept outside the repository: " +
			"what it is, how it is built and tested, conventions and decisions. " +
			"op=read returns them; op=append adds text at the end of a section (created when missing); " +
			"op=replace swaps the exact text old for new (new may be empty). Keep entries short and factual.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"repo":{"type":"string","maxLength":201,"description":"owner/name"},` +
			`"op":{"type":"string","enum":["read","append","replace"]},` +
			`"section":{"type":"string","maxLength":80,"description":"append: the section heading, e.g. Conventions"},` +
			`"text":{"type":"string","maxLength":4096,"description":"append: the Markdown to add"},` +
			`"old":{"type":"string","maxLength":4096,"description":"replace: the exact text to change"},` +
			`"new":{"type":"string","maxLength":4096,"description":"replace: the replacement"}},` +
			`"required":["repo","op"],"additionalProperties":false}`),
	}
	// Reads are read; edits escalate to local_write with Action.Kind memory,
	// so policy applies the tainted-memory floor to them. Notes are the
	// principal's own knowledge, like MEMORY.md, hence the private leg.
	return &notesTool{s: s, spec: makeSpec(def, core.Manifest{
		Title:       "Project notes",
		SideEffect:  core.SERead,
		Taint:       core.TaintPrivate,
		SpaceOK:     true,
		Core:        true,
		Timeout:     10 * time.Second,
		MaxOutputKB: 24,
	})}
}

func (t *notesTool) Spec() core.ToolSpec { return t.spec }

func (t *notesTool) Enabled(env *core.ToolEnv) bool {
	return t.s.cfg().Enabled && t.s.hasRepos(context.Background(), env.Principal.ID)
}

func (t *notesTool) Plan(ctx context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var op notesOp
	if err := decodeArgs(args, &op); err != nil {
		return nil, err
	}
	if !slices.Contains([]string{opRead, opAppend, opReplace}, op.Op) {
		return nil, invalidf("op must be read, append or replace")
	}
	r, err := t.s.resolveRepo(ctx, env, op.Repo)
	if err != nil {
		return nil, err
	}
	op.Repo = r.Repo
	if op.Op == opRead {
		if op.Section != "" || op.Text != "" || op.Old != "" || op.New != "" {
			return nil, invalidf("op read takes only repo")
		}
		return newAction(notesToolName, core.ActGeneric, core.SERead, op)
	}
	before, err := t.s.readNotes(r)
	if err != nil {
		return nil, err
	}
	after, err := applyNotesOp(before, op)
	if err != nil {
		return nil, err
	}
	if c := t.s.cfg().NotesMaxBytes; len(after) > c && len(after) > len(before) {
		return nil, invalidf("the notes would exceed %d bytes (%d free); shorten or replace existing text", c, max(0, c-len(before)))
	}
	act, err := newAction(notesToolName, core.ActMemory, core.SELocalWrite, op)
	if err != nil {
		return nil, err
	}
	act.Diff = notesDiff(r.Repo, before, after)
	return act, nil
}

func (t *notesTool) Exec(ctx context.Context, env *core.ToolEnv, act *core.Action) (*core.ToolResult, error) {
	var op notesOp
	if err := json.Unmarshal(act.Payload, &op); err != nil {
		return nil, fmt.Errorf("notes: payload: %w", err)
	}
	r, err := t.s.resolveRepo(ctx, env, op.Repo)
	if err != nil {
		return toolError(notesToolName, err), nil
	}
	t.s.touch(ctx, env.Session, r.ID)
	if op.Op == opRead {
		content, err := t.s.readNotes(r)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(content) == "" {
			msg := "There are no notes for " + r.Repo + " yet."
			if r.Notes.Status == core.NotesDrafting {
				msg += " A first draft is being written."
			}
			return &core.ToolResult{Name: notesToolName, Content: msg}, nil
		}
		return &core.ToolResult{Name: notesToolName, Content: content, Untrusted: r.Notes.Untrusted}, nil
	}
	return t.s.agentEdit(ctx, env, r, op)
}

// agentEdit applies an approved (or auto-allowed) edit against the current
// notes; content written while untrusted text is in context keeps the notes
// marked untrusted until a user saves them.
func (s *Service) agentEdit(ctx context.Context, env *core.ToolEnv, r *core.IndexedRepo, op notesOp) (*core.ToolResult, error) {
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	cur, err := s.d.Repo.Get(ctx, core.SystemViewer(), r.ID)
	if err != nil {
		return &core.ToolResult{Name: notesToolName, Content: "The repository is no longer indexed.", IsError: true}, nil
	}
	before, err := s.readNotes(cur)
	if err != nil {
		return nil, err
	}
	after, err := applyNotesOp(before, op)
	if err != nil {
		return toolError(notesToolName, err), nil
	}
	capBytes := s.cfg().NotesMaxBytes
	if len(after) > capBytes && len(after) > len(before) {
		return &core.ToolResult{Name: notesToolName, Content: fmt.Sprintf("The notes would exceed %d bytes; shorten or replace existing text.", capBytes),
			IsError: true}, nil
	}
	if err := s.writeNotesFile(cur, after); err != nil {
		return nil, err
	}
	s.recordVersion(cur, core.NotesVersion{Actor: core.NotesByBothy, Session: env.Session, Run: env.Run, Before: before, After: after})
	cur.Notes = core.NotesState{Status: core.NotesReady, Untrusted: cur.Notes.Untrusted || env.Taint.Has(core.TaintUntrusted),
		UpdatedAt: s.clock.Now()}
	if after == "" {
		cur.Notes.Status = core.NotesNone
	}
	if err := s.d.Repo.UpdateNotes(ctx, cur.ID, cur.Notes); err != nil {
		return nil, err
	}
	s.publish(cur, false)
	s.auditRun(ctx, "agent:"+string(env.Run), "knowledge.notes_write", cur, "ok",
		map[string]any{"op": op.Op, "bytes": len(after), "untrusted": cur.Notes.Untrusted}, env.Session, env.Run)
	verb := map[string]string{opAppend: "Added to", opReplace: "Updated"}[op.Op]
	return &core.ToolResult{Name: notesToolName, Content: fmt.Sprintf("%s the notes for %s (%d/%d bytes used).",
		verb, cur.Repo, len(after), capBytes)}, nil
}
