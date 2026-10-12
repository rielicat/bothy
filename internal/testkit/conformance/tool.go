package conformance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/bothyhq/bothy/internal/core"
)

var toolNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

var (
	knownTriggers = []core.Trigger{core.TrigMessage, core.TrigSchedule, core.TrigHeartbeat, core.TrigAPI}
	knownRoles    = []core.Role{"", core.RoleOwner, core.RoleAdmin, core.RoleMember}
)

func runTool(r reporter, tool core.Tool, cases []ToolCase) {
	spec := tool.Spec()
	var schema *jsonschema.Resolved
	check(r, "manifest", func(r reporter) { schema = checkManifest(r, tool, spec) })
	check(r, "plan", func(r reporter) {
		ok := 0
		for _, c := range cases {
			if !c.WantErr {
				ok++
			}
			check(r, c.Name, func(r reporter) { checkCase(r, tool, spec, schema, c) })
		}
		if ok == 0 {
			r.Errorf("tool %q: no case expects Plan to succeed, so determinism is unchecked", spec.Def.Name)
		}
	})
}

func checkManifest(r reporter, tool core.Tool, spec core.ToolSpec) *jsonschema.Resolved {
	name, m := spec.Def.Name, spec.Manifest
	if !toolNameRE.MatchString(name) {
		r.Errorf("tool name %q does not match %s", name, toolNameRE)
	}
	if m.Title == "" {
		r.Errorf("tool %q: Manifest.Title is empty", name)
	}
	if !m.SideEffect.Valid() {
		r.Errorf("tool %q: Manifest.SideEffect %d is not a valid side effect", name, m.SideEffect)
	}
	for _, tr := range m.Triggers {
		if !slices.Contains(knownTriggers, tr) {
			r.Errorf("tool %q: unknown trigger %q in Manifest.Triggers", name, tr)
		}
	}
	if !slices.Contains(knownRoles, m.MinRole) {
		r.Errorf("tool %q: unknown Manifest.MinRole %q", name, m.MinRole)
	}
	if m.Timeout < 0 || m.MaxOutputKB < 0 {
		r.Errorf("tool %q: negative Timeout or MaxOutputKB", name)
	}
	a, errA := json.Marshal(spec)
	b, errB := json.Marshal(tool.Spec())
	if errA != nil || errB != nil {
		r.Errorf("tool %q: spec does not marshal: %v", name, errors.Join(errA, errB))
	} else if !bytes.Equal(a, b) {
		r.Errorf("tool %q: Spec() differs between calls", name)
	}
	return compileSchema(r, name, spec.Def.Schema)
}

func compileSchema(r reporter, name string, raw json.RawMessage) *jsonschema.Resolved {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		r.Errorf("tool %q: schema is not a JSON object: %v", name, err)
		return nil
	}
	if top["type"] != "object" {
		r.Errorf(`tool %q: schema must have "type": "object", got %v`, name, top["type"])
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		r.Errorf("tool %q: schema does not parse: %v", name, err)
		return nil
	}
	res, err := s.Resolve(nil)
	if err != nil {
		r.Errorf("tool %q: schema does not compile: %v", name, err)
		return nil
	}
	return res
}

func validateArgs(schema *jsonschema.Resolved, args json.RawMessage) error {
	if schema == nil {
		return nil
	}
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return err
	}
	return schema.Validate(v)
}

func checkCase(r reporter, tool core.Tool, spec core.ToolSpec, schema *jsonschema.Resolved, c ToolCase) {
	name := spec.Def.Name
	base := c.Env
	if base == nil {
		base = defaultEnv(r.TempDir())
	}
	schemaErr := validateArgs(schema, c.Args)
	if !c.WantErr && schemaErr != nil {
		r.Errorf("tool %q case %q: args do not match the tool's schema: %v", name, c.Name, schemaErr)
	}

	tr := &trap{}
	before, snapErr := snapshot(base.Workspace)
	p1 := plan(r, tool, tr.env(base), c.Args)
	p2 := plan(r, tool, tr.env(base), c.Args)
	if calls := tr.list(); len(calls) > 0 {
		r.Errorf("tool %q case %q: Plan performed side effects: %s", name, c.Name, strings.Join(calls, "; "))
	}
	if snapErr == nil {
		after, err := snapshot(base.Workspace)
		if err != nil {
			r.Errorf("tool %q case %q: workspace unreadable after Plan: %v", name, c.Name, err)
		} else if diff := diffTrees(before, after); diff != "" {
			r.Errorf("tool %q case %q: Plan changed the workspace: %s", name, c.Name, diff)
		}
	}
	if p1.panicked || p2.panicked {
		return
	}
	a1, a2, err1, err2 := p1.a, p2.a, p1.err, p2.err

	if c.WantErr {
		if schemaErr == nil && (err1 == nil || err2 == nil) {
			r.Errorf("tool %q case %q: Plan accepted args the case expects to fail", name, c.Name)
		}
		return
	}
	if err1 != nil || err2 != nil {
		r.Errorf("tool %q case %q: Plan failed: %v", name, c.Name, errors.Join(err1, err2))
		return
	}
	if a1 == nil || a2 == nil {
		r.Errorf("tool %q case %q: Plan returned a nil Action without an error", name, c.Name)
		return
	}
	j1, errA := json.Marshal(a1)
	j2, errB := json.Marshal(a2)
	switch {
	case errA != nil || errB != nil:
		r.Errorf("tool %q case %q: Action does not marshal: %v", name, c.Name, errors.Join(errA, errB))
	case !bytes.Equal(j1, j2):
		r.Errorf("tool %q case %q: Plan is not deterministic:\n first:  %s\n second: %s", name, c.Name, j1, j2)
	}
	checkAction(r, spec, c.Name, a1)
}

func checkAction(r reporter, spec core.ToolSpec, caseName string, a *core.Action) {
	name := spec.Def.Name
	if a.Tool != name {
		r.Errorf("tool %q case %q: Action.Tool = %q, want the spec name", name, caseName, a.Tool)
	}
	if a.Kind == "" {
		r.Errorf("tool %q case %q: Action.Kind is empty", name, caseName)
	}
	if !a.SideEffect.Valid() || a.SideEffect < spec.Manifest.SideEffect {
		r.Errorf("tool %q case %q: Action.SideEffect %s is below Manifest.SideEffect %s", name, caseName, a.SideEffect, spec.Manifest.SideEffect)
	}
	canon, err := core.CanonicalJSON(a.Payload)
	switch {
	case err != nil:
		r.Errorf("tool %q case %q: Action.Payload is not JSON: %v", name, caseName, err)
	case !bytes.Equal(canon, a.Payload):
		r.Errorf("tool %q case %q: Action.Payload is not canonical JSON:\n got:  %s\n want: %s", name, caseName, a.Payload, canon)
	}
	if want := core.ActionFingerprint(a.Tool, a.Payload); a.Fingerprint != want {
		r.Errorf("tool %q case %q: Action.Fingerprint = %q, want ActionFingerprint(Tool, Payload) = %q", name, caseName, a.Fingerprint, want)
	}
}

type planResult struct {
	a        *core.Action
	err      error
	panicked bool
}

// plan calls Plan on a private copy of args.
func plan(r reporter, tool core.Tool, env *core.ToolEnv, args json.RawMessage) (res planResult) {
	defer func() {
		if p := recover(); p != nil {
			r.Errorf("tool %q: Plan panicked: %v\n%s", tool.Spec().Def.Name, p, debug.Stack())
			res.panicked = true
		}
	}()
	res.a, res.err = tool.Plan(context.Background(), env, bytes.Clone(args))
	return res
}

func defaultEnv(workspace string) *core.ToolEnv {
	u := core.UserID(core.NewID(core.PrefixUser))
	s := core.SessionID(core.NewID(core.PrefixSession))
	return &core.ToolEnv{
		User: u, Role: core.RoleOwner,
		Principal: core.Principal{ID: core.UserPrincipal(u), Kind: core.PrincipalUser, User: u,
			Workspace: core.WorkspaceID(core.NewID(core.PrefixWorkspace)), ToolProfile: "standard"},
		Session: s, Run: core.RunID(core.NewID(core.PrefixRun)), CallID: core.NewID(core.PrefixToolCall),
		Trigger: core.TrigMessage, Workspace: workspace, BoxKey: core.DerivedID(core.PrefixBox, "session:"+string(s)),
		Interactive: true, Selection: core.Selection{SandboxOK: true},
	}
}

var errTrapped = errors.New("conformance: side effect attempted during Plan")

// trap records every forbidden call Plan makes through its env.
type trap struct {
	mu    sync.Mutex
	calls []string
}

func (t *trap) hit(call string) {
	t.mu.Lock()
	t.calls = append(t.calls, call)
	t.mu.Unlock()
}

func (t *trap) list() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.calls)
}

func (t *trap) env(base *core.ToolEnv) *core.ToolEnv {
	e := *base
	e.Sandbox = trapSandbox{inner: base.Sandbox, t: t}
	e.HTTP = &http.Client{Transport: trapTransport{t}}
	e.Emit = func(d core.EventData) { t.hit(fmt.Sprintf("Emit(%T)", d)) }
	return &e
}

type trapSandbox struct {
	inner core.Sandbox
	t     *trap
}

func (s trapSandbox) Status(ctx context.Context) core.SandboxStatus {
	if s.inner != nil {
		return s.inner.Status(ctx)
	}
	return core.SandboxStatus{Backend: "none", Reason: "conformance"}
}

func (s trapSandbox) Usage(ctx context.Context, ws core.WorkspaceID) (int64, int64, error) {
	if s.inner != nil {
		return s.inner.Usage(ctx, ws)
	}
	return 0, 0, nil
}

func (s trapSandbox) Ensure(context.Context, core.BoxSpec) error {
	s.t.hit("Sandbox.Ensure")
	return errTrapped
}

func (s trapSandbox) Exec(_ context.Context, _ string, req core.ExecRequest, _ func(int, []byte)) (*core.ExecResult, error) {
	s.t.hit(fmt.Sprintf("Sandbox.Exec(%q)", req.Argv))
	return nil, errTrapped
}

func (s trapSandbox) Stdio(context.Context, string, []string, map[string]string) (io.ReadWriteCloser, error) {
	s.t.hit("Sandbox.Stdio")
	return nil, errTrapped
}

func (s trapSandbox) Kill(context.Context, string) error {
	s.t.hit("Sandbox.Kill")
	return errTrapped
}

func (s trapSandbox) Destroy(context.Context, string) error {
	s.t.hit("Sandbox.Destroy")
	return errTrapped
}

func (s trapSandbox) Watch(func(core.SandboxStatus)) { s.t.hit("Sandbox.Watch") }

type trapTransport struct{ t *trap }

func (tt trapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	tt.t.hit("HTTP " + req.Method + " " + req.URL.Redacted())
	return nil, errTrapped
}

// snapshot fingerprints every entry under dir.
func snapshot(dir string) (map[string]string, error) {
	if dir == "" {
		return nil, errors.New("no workspace")
	}
	tree := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		desc := info.Mode().String()
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			desc += " -> " + target
		case info.Mode().IsRegular():
			b, err := os.ReadFile(path) //nolint:gosec // snapshot of a test-owned temp workspace
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			desc += " " + hex.EncodeToString(sum[:])
		}
		tree[rel] = desc
		return nil
	})
	return tree, err
}

func diffTrees(before, after map[string]string) string {
	var diffs []string
	for p, d := range before {
		switch a, ok := after[p]; {
		case !ok:
			diffs = append(diffs, "removed "+p)
		case a != d:
			diffs = append(diffs, "modified "+p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			diffs = append(diffs, "created "+p)
		}
	}
	slices.Sort(diffs)
	return strings.Join(diffs, ", ")
}
