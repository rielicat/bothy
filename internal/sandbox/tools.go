package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

const (
	maxCodeBytes = 256 << 10
	outputChunk  = 8 << 10 // tool.output chunks are <= 8 KB
	scriptDir    = ".bothy/tmp"
)

// execPayload is the Action payload of shell and python (core.Action docs).
type execPayload struct {
	Argv []string `json:"argv"`
	Code string   `json:"code,omitempty"`
	Cwd  string   `json:"cwd"`
}

type execTool struct {
	cfg  config.View
	spec core.ToolSpec
	plan func(args json.RawMessage) (execPayload, error)
	// prepare runs just before the exec (python writes its script).
	prepare func(env *core.ToolEnv, p execPayload) error
}

func newExecTool(cfg config.View, def core.ToolDef, title string, plan func(json.RawMessage) (execPayload, error),
	prepare func(*core.ToolEnv, execPayload) error) *execTool {
	c := cfg.Current().Sandbox
	canon, err := core.CanonicalJSON(def)
	if err != nil {
		panic(err) // static definitions
	}
	sum := sha256.Sum256(canon)
	return &execTool{cfg: cfg, plan: plan, prepare: prepare, spec: core.ToolSpec{
		Def:    def,
		Source: "builtin",
		Manifest: core.Manifest{
			Title: title, SideEffect: core.SELocalWrite, Exec: true, FSPaths: []string{"**"},
			Core: true, Timeout: c.ExecTimeout.D(), MaxOutputKB: c.OutputKB,
		},
		Hash: hex.EncodeToString(sum[:]),
	}}
}

func (t *execTool) Spec() core.ToolSpec { return t.spec }

func (t *execTool) Plan(_ context.Context, _ *core.ToolEnv, args json.RawMessage) (*core.Action, error) {
	p, err := t.plan(args)
	if err != nil {
		return nil, err
	}
	if err := proto.ValidateArgv(p.Argv); err != nil {
		return nil, invalid(err)
	}
	payload, err := core.CanonicalJSON(p)
	if err != nil {
		return nil, err
	}
	name := t.spec.Def.Name
	return &core.Action{Tool: name, Kind: core.ActExec, SideEffect: core.SELocalWrite, Argv: p.Argv, Cwd: p.Cwd,
		Payload: payload, Fingerprint: core.ActionFingerprint(name, payload)}, nil
}

// Exec runs exactly the decided Action: argv and cwd come from its payload.
func (t *execTool) Exec(ctx context.Context, env *core.ToolEnv, a *core.Action) (*core.ToolResult, error) {
	var p execPayload
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return nil, fmt.Errorf("%s: bad action payload: %w", t.spec.Def.Name, err)
	}
	if env.Sandbox == nil {
		return nil, core.ErrNoSandbox
	}
	if t.prepare != nil {
		if err := t.prepare(env, p); err != nil {
			return nil, err
		}
	}
	c := t.cfg.Current().Sandbox
	res, err := env.Sandbox.Exec(ctx, env.BoxKey, core.ExecRequest{
		Argv: p.Argv, Cwd: p.Cwd, Timeout: c.ExecTimeout.D(), MaxOutput: c.OutputKB << 10,
	}, emitter(env))
	if err != nil {
		return nil, err
	}
	return execResult(t.spec.Def.Name, env.CallID, res), nil
}

// emitter publishes tool.output progress in UTF-8-safe chunks.
func emitter(env *core.ToolEnv) func(int, []byte) {
	if env.Emit == nil {
		return nil
	}
	return func(stream int, b []byte) {
		for len(b) > 0 {
			n := min(len(b), outputChunk)
			for n < len(b) && n > 0 && !utf8.RuneStart(b[n]) {
				n--
			}
			if n == 0 {
				n = min(len(b), outputChunk)
			}
			env.Emit(core.ToolOutputData{CallID: env.CallID, Stream: stream, Chunk: string(b[:n])})
			b = b[n:]
		}
	}
}

func execResult(name, callID string, r *core.ExecResult) *core.ToolResult {
	var b strings.Builder
	fmt.Fprintf(&b, "exit %d, %s", r.Exit, r.Duration.Round(time.Millisecond))
	if r.TimedOut {
		b.WriteString(", timed out")
	}
	if r.Killed {
		b.WriteString(", killed by /stop")
	}
	if r.Truncated {
		b.WriteString(", output truncated")
	}
	b.WriteByte('\n')
	section := func(label string, out []byte) {
		if len(out) == 0 {
			return
		}
		b.WriteString("--- " + label + " ---\n")
		b.Write(out)
		if out[len(out)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	section("stdout", r.Stdout)
	section("stderr", r.Stderr)
	if len(r.Stdout) == 0 && len(r.Stderr) == 0 {
		b.WriteString("(no output)\n")
	}
	return &core.ToolResult{
		CallID:    callID,
		Name:      name,
		Content:   strings.ToValidUTF8(strings.TrimRight(b.String(), "\n"), "�"),
		IsError:   r.Exit != 0 || r.TimedOut || r.Killed,
		Truncated: r.Truncated,
	}
}

// decodeArgs decodes tool args strictly into v.
func decodeArgs(args json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", core.ErrInvalid, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data after the arguments", core.ErrInvalid)
	}
	return nil
}

func newShell(cfg config.View) *execTool {
	def := core.ToolDef{
		Name: "shell",
		Description: "Run a bash command in this session's sandbox. The working directory is /workspace (your files); " +
			"network access goes through an approval-gated proxy. Has git, curl, jq, rg and Python 3; there is no pip: " +
			"use uv (uv run --with <pkg> python x.py, uvx <tool>). Returns the exit code, stdout and stderr.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"command":{"type":"string","description":"bash command line"},` +
			`"cwd":{"type":"string","description":"directory relative to /workspace (default: /workspace)"}},` +
			`"required":["command"],"additionalProperties":false}`),
	}
	return newExecTool(cfg, def, "Run shell command", func(args json.RawMessage) (execPayload, error) {
		var a struct {
			Command string `json:"command"`
			Cwd     string `json:"cwd"`
		}
		if err := decodeArgs(args, &a); err != nil {
			return execPayload{}, err
		}
		if strings.TrimSpace(a.Command) == "" {
			return execPayload{}, fmt.Errorf("%w: command is empty", core.ErrInvalid)
		}
		cwd, err := proto.CleanCwd(a.Cwd)
		if err != nil {
			return execPayload{}, invalid(err)
		}
		return execPayload{Argv: []string{"bash", "-lc", a.Command}, Cwd: cwd}, nil
	}, nil)
}

func newPython(cfg config.View) *execTool {
	def := core.ToolDef{
		Name: "python",
		Description: "Run Python 3 code in this session's sandbox with /workspace (your files) as the working directory. " +
			"Print what you need; returns the exit code, stdout and stderr.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"code":{"type":"string","description":"Python 3 source"}},` +
			`"required":["code"],"additionalProperties":false}`),
	}
	return newExecTool(cfg, def, "Run Python code", func(args json.RawMessage) (execPayload, error) {
		var a struct {
			Code string `json:"code"`
		}
		if err := decodeArgs(args, &a); err != nil {
			return execPayload{}, err
		}
		if strings.TrimSpace(a.Code) == "" {
			return execPayload{}, fmt.Errorf("%w: code is empty", core.ErrInvalid)
		}
		if len(a.Code) > maxCodeBytes {
			return execPayload{}, fmt.Errorf("%w: code exceeds %d bytes", core.ErrInvalid, maxCodeBytes)
		}
		return execPayload{Argv: pythonArgv(a.Code), Code: a.Code}, nil
	}, writeScript)
}

// pythonArgv names the script after the code. The name cannot depend on the
// Action fingerprint (that hashes argv itself), so it is the first 16 hex of
// the fingerprint of {"code": code}: content-addressed and deterministic.
func pythonArgv(code string) []string {
	canon, _ := core.CanonicalJSON(map[string]string{"code": code})
	return []string{"python3", scriptDir + "/" + core.ActionFingerprint("python", canon)[:16] + ".py"}
}

// writeScript puts the decided code where the decided argv points, inside
// the workspace through os.Root. The box can write the workspace and the
// name is predictable, so the code goes to a fresh file that is renamed into
// place: whatever the box planted there (a FIFO would block the open, a
// symlink would redirect it) is replaced, and a concurrent identical call
// never sees a truncated script.
func writeScript(env *core.ToolEnv, p execPayload) error {
	if !slices.Equal(p.Argv, pythonArgv(p.Code)) {
		return fmt.Errorf("python: %w: argv does not match the code", core.ErrInvalid)
	}
	if env.Workspace == "" {
		return fmt.Errorf("python: no workspace: %w", core.ErrInvalid)
	}
	root, err := os.OpenRoot(env.Workspace)
	if err != nil {
		return fmt.Errorf("python: %w", err)
	}
	defer root.Close()
	// os.Root takes permission bits only; inside the setgid workspace Linux
	// gives new directories the setgid bit and group 10001 anyway.
	if err := root.MkdirAll(scriptDir, 0o770); err != nil {
		return fmt.Errorf("python: %w", err)
	}
	tmp := scriptDir + "/." + rand.Text() + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o660)
	if err != nil {
		return fmt.Errorf("python: %w", err)
	}
	_, werr := io.WriteString(f, p.Code)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = root.Rename(tmp, p.Argv[1])
	}
	if werr != nil {
		_ = root.Remove(tmp)
		return fmt.Errorf("python: %w", werr)
	}
	return nil
}
