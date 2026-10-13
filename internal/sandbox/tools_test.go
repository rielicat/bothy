package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/conformance"
	"github.com/bothyhq/bothy/internal/testkit/sandboxtest"
)

func testTools() (*execTool, *execTool) {
	cfg := config.Static(config.Default())
	return newShell(cfg), newPython(cfg)
}

func TestShellConformance(t *testing.T) {
	shell, _ := testTools()
	conformance.Tool(t, shell, []conformance.ToolCase{
		{Name: "simple", Args: json.RawMessage(`{"command":"ls -la"}`)},
		{Name: "cwd", Args: json.RawMessage(`{"command":"make test","cwd":"src/app"}`)},
		{Name: "unicode", Args: json.RawMessage(`{"command":"echo 'héllo ✓' | wc -c"}`)},
		{Name: "missing command", Args: json.RawMessage(`{}`), WantErr: true},
		{Name: "blank command", Args: json.RawMessage(`{"command":"   "}`), WantErr: true},
		{Name: "escape cwd", Args: json.RawMessage(`{"command":"ls","cwd":"../other"}`), WantErr: true},
		{Name: "absolute cwd", Args: json.RawMessage(`{"command":"ls","cwd":"/etc"}`), WantErr: true},
		{Name: "unknown field", Args: json.RawMessage(`{"command":"ls","timeout":5}`), WantErr: true},
		{Name: "nul byte", Args: json.RawMessage(`{"command":"ls\u0000"}`), WantErr: true},
	})
}

func TestPythonConformance(t *testing.T) {
	_, python := testTools()
	conformance.Tool(t, python, []conformance.ToolCase{
		{Name: "print", Args: json.RawMessage(`{"code":"print(1+1)"}`)},
		{Name: "multiline", Args: json.RawMessage(`{"code":"import sys\nfor i in range(3):\n    print(i, file=sys.stderr)\n"}`)},
		{Name: "missing code", Args: json.RawMessage(`{}`), WantErr: true},
		{Name: "blank code", Args: json.RawMessage(`{"code":"\n\t"}`), WantErr: true},
		{Name: "cwd not accepted", Args: json.RawMessage(`{"code":"print(1)","cwd":"x"}`), WantErr: true},
		{Name: "too large", Args: json.RawMessage(`{"code":"` + strings.Repeat("x", maxCodeBytes+1) + `"}`), WantErr: true},
	})
}

func TestShellPlanAction(t *testing.T) {
	shell, _ := testTools()
	a, err := shell.Plan(context.Background(), &core.ToolEnv{}, json.RawMessage(`{"command":"git status","cwd":"repo/"}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != core.ActExec || a.SideEffect != core.SELocalWrite || !slices.Equal(a.Argv, []string{"bash", "-lc", "git status"}) ||
		a.Cwd != "repo" || string(a.Payload) != `{"argv":["bash","-lc","git status"],"cwd":"repo"}` {
		t.Fatalf("action = %+v payload %s", a, a.Payload)
	}
	spec := shell.Spec()
	m := spec.Manifest
	if spec.Def.Name != "shell" || !m.Exec || !m.Core || m.SpaceOK || m.SideEffect != core.SELocalWrite || m.Timeout != 120*time.Second ||
		m.MaxOutputKB != 64 || len(spec.Hash) != 64 || spec.Source != "builtin" {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestPythonPlanAction(t *testing.T) {
	_, python := testTools()
	a, err := python.Plan(context.Background(), &core.ToolEnv{}, json.RawMessage(`{"code":"print('hi')"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Argv) != 2 || a.Argv[0] != "python3" || !strings.HasPrefix(a.Argv[1], ".bothy/tmp/") || !strings.HasSuffix(a.Argv[1], ".py") ||
		len(a.Argv[1]) != len(".bothy/tmp/")+16+3 {
		t.Fatalf("argv = %q", a.Argv)
	}
	var p map[string]any
	if err := json.Unmarshal(a.Payload, &p); err != nil || p["code"] != "print('hi')" || p["cwd"] != "" {
		t.Fatalf("payload = %s", a.Payload)
	}
	b, _ := python.Plan(context.Background(), &core.ToolEnv{}, json.RawMessage(`{"code":"print('bye')"}`))
	if b.Argv[1] == a.Argv[1] {
		t.Fatal("different code shares a script name")
	}
}

func toolEnv(t *testing.T, sb core.Sandbox) (*core.ToolEnv, *[]core.ToolOutputData) {
	var mu sync.Mutex
	var out []core.ToolOutputData
	return &core.ToolEnv{
		CallID: "tcl_x", BoxKey: key1, Sandbox: sb, Workspace: t.TempDir(),
		Emit: func(d core.EventData) {
			mu.Lock()
			out = append(out, d.(core.ToolOutputData))
			mu.Unlock()
		},
	}, &out
}

func TestShellExec(t *testing.T) {
	shell, _ := testTools()
	sb := &sandboxtest.Sandbox{ExecFunc: func(_ context.Context, key string, req core.ExecRequest) (*core.ExecResult, error) {
		return &core.ExecResult{Exit: 2, Stdout: []byte("partial"), Stderr: []byte("boom\n"), Duration: 41 * time.Millisecond}, nil
	}}
	env, emitted := toolEnv(t, sb)
	a, err := shell.Plan(context.Background(), env, json.RawMessage(`{"command":"make","cwd":"src"}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := shell.Exec(context.Background(), env, a)
	if err != nil {
		t.Fatal(err)
	}
	calls := sb.ExecCalls()
	if len(calls) != 1 || calls[0].Key != key1 || !slices.Equal(calls[0].Req.Argv, []string{"bash", "-lc", "make"}) ||
		calls[0].Req.Cwd != "src" || calls[0].Req.Timeout != 120*time.Second || calls[0].Req.MaxOutput != 64<<10 {
		t.Fatalf("exec calls = %+v", calls)
	}
	want := "exit 2, 41ms\n--- stdout ---\npartial\n--- stderr ---\nboom"
	if res.Content != want || !res.IsError || res.CallID != "tcl_x" || res.Name != "shell" {
		t.Fatalf("result = %+v", res)
	}
	if len(*emitted) != 2 || (*emitted)[0].Stream != 1 || (*emitted)[1].Chunk != "boom\n" {
		t.Fatalf("emitted = %+v", *emitted)
	}

	sb.ExecFunc = func(context.Context, string, core.ExecRequest) (*core.ExecResult, error) {
		return &core.ExecResult{Exit: 124, TimedOut: true, Truncated: true, Duration: time.Second}, nil
	}
	res, _ = shell.Exec(context.Background(), env, a)
	if res.Content != "exit 124, 1s, timed out, output truncated\n(no output)" || !res.Truncated || !res.IsError {
		t.Fatalf("timeout result = %+v", res)
	}

	env.Sandbox = &sandboxtest.Sandbox{StatusV: core.SandboxStatus{Backend: "none"}}
	if _, err := shell.Exec(context.Background(), env, a); !errors.Is(err, core.ErrNoSandbox) {
		t.Fatalf("no sandbox: %v", err)
	}
	env.Sandbox = nil
	if _, err := shell.Exec(context.Background(), env, a); !errors.Is(err, core.ErrNoSandbox) {
		t.Fatalf("nil sandbox: %v", err)
	}
}

func TestPythonExecWritesTheScript(t *testing.T) {
	_, python := testTools()
	var gotArgv []string
	var env *core.ToolEnv
	sb := &sandboxtest.Sandbox{ExecFunc: func(_ context.Context, _ string, req core.ExecRequest) (*core.ExecResult, error) {
		gotArgv = req.Argv
		code, err := os.ReadFile(filepath.Join(env.Workspace, req.Argv[1]))
		if err != nil {
			return nil, err
		}
		return &core.ExecResult{Stdout: code}, nil
	}}
	env, _ = toolEnv(t, sb)
	a, err := python.Plan(context.Background(), env, json.RawMessage(`{"code":"print('hi')"}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := python.Exec(context.Background(), env, a)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(gotArgv, a.Argv) || !strings.Contains(res.Content, "print('hi')") || res.IsError {
		t.Fatalf("argv %q result %+v", gotArgv, res)
	}
	fi, err := os.Stat(filepath.Join(env.Workspace, a.Argv[1]))
	if err != nil || fi.Mode().Perm()&0o060 == 0 {
		t.Fatalf("script mode %v, %v (the box reads it through group 10001)", fi.Mode(), err)
	}

	// An Action whose argv no longer matches its code is refused.
	var p execPayload
	_ = json.Unmarshal(a.Payload, &p)
	p.Code = "import os; os.system('id')"
	tampered := *a
	tampered.Payload, _ = core.CanonicalJSON(p)
	if _, err := python.Exec(context.Background(), env, &tampered); !isInvalid(err) {
		t.Fatalf("tampered payload: %v", err)
	}
	env.Workspace = ""
	if _, err := python.Exec(context.Background(), env, a); err == nil {
		t.Fatal("python without a workspace")
	}
}

func TestEmitterChunksUTF8Safely(t *testing.T) {
	var got []core.ToolOutputData
	emit := emitter(&core.ToolEnv{CallID: "tcl_y", Emit: func(d core.EventData) { got = append(got, d.(core.ToolOutputData)) }})
	data := bytes.Repeat([]byte("ab✓"), 5000) // 25 KB of mixed-width runes
	emit(2, data)
	var joined strings.Builder
	for _, c := range got {
		if len(c.Chunk) > outputChunk || !utf8.ValidString(c.Chunk) || c.Stream != 2 || c.CallID != "tcl_y" {
			t.Fatalf("bad chunk: %d bytes valid %v", len(c.Chunk), utf8.ValidString(c.Chunk))
		}
		joined.WriteString(c.Chunk)
	}
	if joined.String() != string(data) {
		t.Fatal("chunks do not reassemble")
	}
	if emitter(&core.ToolEnv{}) != nil {
		t.Fatal("emitter without Emit must be nil")
	}
}

func TestExecResultSanitizesUTF8(t *testing.T) {
	r := execResult("shell", "tcl_z", &core.ExecResult{Stdout: []byte{0xff, 'o', 'k'}, Killed: true})
	if !utf8.ValidString(r.Content) || !strings.Contains(r.Content, "killed by /stop") || !r.IsError {
		t.Fatalf("result = %q", r.Content)
	}
}
