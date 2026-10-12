package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/bothyhq/bothy/internal/core"
)

const echoSchema = `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`

// echo is a well-behaved tool; mutate and spec tweaks break it on purpose.
type echo struct {
	spec   core.ToolSpec
	mutate func(env *core.ToolEnv, a *core.Action)
	specFn func(core.ToolSpec) core.ToolSpec
}

func newEcho() *echo {
	return &echo{spec: core.ToolSpec{
		Def:      core.ToolDef{Name: "echo", Description: "Echo text back", Schema: json.RawMessage(echoSchema)},
		Source:   "builtin",
		Manifest: core.Manifest{Title: "Echo text", SideEffect: core.SELocalWrite, Triggers: []core.Trigger{core.TrigMessage}},
	}}
}

func (e *echo) Spec() core.ToolSpec {
	if e.specFn != nil {
		return e.specFn(e.spec)
	}
	return e.spec
}

func (e *echo) Plan(_ context.Context, env *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("%w: %w", core.ErrInvalid, err)
	}
	if in.Text == "" {
		return nil, fmt.Errorf("%w: text is required", core.ErrInvalid)
	}
	payload, err := core.CanonicalJSON(map[string]string{"text": in.Text})
	if err != nil {
		return nil, err
	}
	a := &core.Action{Tool: e.spec.Def.Name, Kind: core.ActGeneric, SideEffect: e.spec.Manifest.SideEffect,
		Payload: payload, Fingerprint: core.ActionFingerprint(e.spec.Def.Name, payload)}
	if e.mutate != nil {
		e.mutate(env, a)
	}
	return a, nil
}

func (e *echo) Exec(_ context.Context, _ *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	return &core.ToolResult{Content: string(a.Payload)}, nil
}

var echoCases = []ToolCase{
	{Name: "ok", Args: json.RawMessage(`{"text":"hi"}`)},
	{Name: "unicode", Args: json.RawMessage(`{"text":"héllo <b>"}`)},
	{Name: "missing text", Args: json.RawMessage(`{}`), WantErr: true},
	{Name: "wrong type", Args: json.RawMessage(`{"text":5}`), WantErr: true},
}

func TestToolPassesForGoodTool(t *testing.T) {
	Tool(t, newEcho(), echoCases)
	synctest.Test(t, func(t *testing.T) { Tool(t, newEcho(), echoCases) })

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := newEcho()
	reader.mutate = func(env *core.ToolEnv, _ *core.Action) {
		_, _ = os.ReadFile(filepath.Join(env.Workspace, "notes.txt"))
		env.Sandbox.Status(context.Background())
		_, _, _ = env.Sandbox.Usage(context.Background(), env.Principal.Workspace)
	}
	env := defaultEnv(ws)
	Tool(t, reader, []ToolCase{{Name: "reads are fine", Env: env, Args: json.RawMessage(`{"text":"hi"}`)}})
}

func TestToolCatchesBrokenTools(t *testing.T) {
	ok := []ToolCase{{Name: "ok", Args: json.RawMessage(`{"text":"hi"}`)}}
	calls := 0
	tests := []struct {
		name    string
		breakIt func(e *echo)
		cases   []ToolCase
		want    string
	}{
		{"bad name", func(e *echo) { e.spec.Def.Name = "bad name!" }, ok, "does not match"},
		{"long name", func(e *echo) { e.spec.Def.Name = fmt.Sprintf("%065d", 0) }, ok, "does not match"},
		{"no title", func(e *echo) { e.spec.Manifest.Title = "" }, ok, "Manifest.Title is empty"},
		{"zero side effect", func(e *echo) { e.spec.Manifest.SideEffect = 0 }, ok, "not a valid side effect"},
		{"unknown trigger", func(e *echo) { e.spec.Manifest.Triggers = []core.Trigger{"cron"} }, ok, "unknown trigger"},
		{"unknown role", func(e *echo) { e.spec.Manifest.MinRole = "root" }, ok, "unknown Manifest.MinRole"},
		{"schema not json", func(e *echo) { e.spec.Def.Schema = json.RawMessage(`{`) }, ok, "not a JSON object"},
		{"schema not object", func(e *echo) { e.spec.Def.Schema = json.RawMessage(`{"type":"string"}`) }, ok, `"type": "object"`},
		{"schema dangling ref", func(e *echo) {
			e.spec.Def.Schema = json.RawMessage(`{"type":"object","properties":{"x":{"$ref":"#/$defs/missing"}}}`)
		}, ok, "does not compile"},
		{"unstable spec", func(e *echo) {
			e.specFn = func(s core.ToolSpec) core.ToolSpec { calls++; s.Def.Description = fmt.Sprint(calls); return s }
		}, ok, "differs between calls"},
		{"nondeterministic", func(e *echo) {
			n := 0
			e.mutate = func(_ *core.ToolEnv, a *core.Action) {
				n++
				a.Payload = json.RawMessage(fmt.Sprintf(`{"n":%d}`, n))
				a.Fingerprint = core.ActionFingerprint(a.Tool, a.Payload)
			}
		}, ok, "not deterministic"},
		{"execs", func(e *echo) {
			e.mutate = func(env *core.ToolEnv, _ *core.Action) {
				_, _ = env.Sandbox.Exec(context.Background(), env.BoxKey, core.ExecRequest{Argv: []string{"rm", "-rf", "x"}}, nil)
			}
		}, ok, `Sandbox.Exec(["rm" "-rf" "x"])`},
		{"ensures a box", func(e *echo) {
			e.mutate = func(env *core.ToolEnv, _ *core.Action) { _ = env.Sandbox.Ensure(context.Background(), core.BoxSpec{}) }
		}, ok, "Sandbox.Ensure"},
		{"dials", func(e *echo) {
			e.mutate = func(env *core.ToolEnv, _ *core.Action) {
				if resp, err := env.HTTP.Get("https://example.com/x?token=1"); err == nil {
					resp.Body.Close()
				}
			}
		}, ok, "HTTP GET https://example.com/x"},
		{"emits", func(e *echo) {
			e.mutate = func(env *core.ToolEnv, _ *core.Action) { env.Emit(core.ToolOutputData{Chunk: "x"}) }
		}, ok, "Emit(core.ToolOutputData)"},
		{"writes the workspace", func(e *echo) {
			e.mutate = func(env *core.ToolEnv, _ *core.Action) {
				_ = os.WriteFile(filepath.Join(env.Workspace, "out.txt"), []byte("x"), 0o600)
			}
		}, ok, "changed the workspace: created out.txt"},
		{"lowers side effect", func(e *echo) {
			e.mutate = func(_ *core.ToolEnv, a *core.Action) { a.SideEffect = core.SERead }
		}, ok, "below Manifest.SideEffect"},
		{"wrong tool", func(e *echo) {
			e.mutate = func(_ *core.ToolEnv, a *core.Action) { a.Tool = "other" }
		}, ok, `Action.Tool = "other"`},
		{"no kind", func(e *echo) {
			e.mutate = func(_ *core.ToolEnv, a *core.Action) { a.Kind = "" }
		}, ok, "Action.Kind is empty"},
		{"non-canonical payload", func(e *echo) {
			e.mutate = func(_ *core.ToolEnv, a *core.Action) {
				a.Payload = json.RawMessage(`{ "text": "hi" }`)
				a.Fingerprint = core.ActionFingerprint(a.Tool, a.Payload)
			}
		}, ok, "not canonical JSON"},
		{"bad fingerprint", func(e *echo) {
			e.mutate = func(_ *core.ToolEnv, a *core.Action) { a.Fingerprint = "abc" }
		}, ok, "Action.Fingerprint"},
		{"nil action", func(e *echo) {
			e.mutate = func(_ *core.ToolEnv, a *core.Action) { *a = core.Action{} }
		}, ok, "Action.Tool"},
		{"panics", func(e *echo) {
			e.mutate = func(*core.ToolEnv, *core.Action) { panic("boom") }
		}, ok, "Plan panicked: boom"},
		{"accepts bad args", func(*echo) {}, []ToolCase{ok[0], {Name: "bad", Args: json.RawMessage(`{"text":"valid"}`), WantErr: true}}, "accepted args the case expects to fail"},
		{"case violates schema", func(*echo) {}, []ToolCase{{Name: "extra", Args: json.RawMessage(`{"text":"hi","extra":1}`)}}, "do not match the tool's schema"},
		{"plan fails", func(*echo) {}, []ToolCase{{Name: "empty", Args: json.RawMessage(`{"text":""}`)}}, "Plan failed"},
		{"only error cases", func(*echo) {}, []ToolCase{{Name: "bad", Args: json.RawMessage(`{}`), WantErr: true}}, "no case expects Plan to succeed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEcho()
			tt.breakIt(e)
			fails := record(t, func(r reporter) { runTool(r, e, tt.cases) })
			wantFailure(t, fails, tt.want)
		})
	}
}

func TestToolGoodToolHasNoFailures(t *testing.T) {
	if fails := record(t, func(r reporter) { runTool(r, newEcho(), echoCases) }); len(fails) != 0 {
		t.Fatalf("unexpected failures: %q", fails)
	}
}
