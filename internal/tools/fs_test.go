package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/conformance"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
	"github.com/bothyhq/bothy/internal/testkit/sandboxtest"
)

// workspace creates a workspace with a few files and an outside directory
// that symlinks point to.
func workspace(t *testing.T) (*core.ToolEnv, string) {
	t.Helper()
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{ws, outside, filepath.Join(ws, "notes", "deep"), filepath.Join(ws, "empty")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"notes/a.txt":         "alpha\nbeta\ngamma\n",
		"notes/deep/b.md":     "# b\n",
		"top.txt":             "top\n",
		"bin.dat":             "\x00\x01\x02binary",
		"../outside/secret":   "s3cr3t\n",
		"notes/no-newline.go": "package x",
	}
	for p, c := range files {
		if err := os.WriteFile(filepath.Join(ws, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../outside/secret", filepath.Join(ws, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside", filepath.Join(ws, "escape_dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("notes/a.txt", filepath.Join(ws, "inside")); err != nil {
		t.Fatal(err)
	}
	env := ownerEnv()
	env.Workspace = ws
	env.Session = core.SessionID(core.NewID(core.PrefixSession))
	env.Principal.Workspace = core.WorkspaceID(core.NewID(core.PrefixWorkspace))
	env.Selection = core.Selection{SandboxOK: true}
	return env, ws
}

func plan(t *testing.T, tool core.Tool, env *core.ToolEnv, args string) (*core.Action, error) {
	t.Helper()
	return tool.Plan(context.Background(), env, json.RawMessage(args))
}

func run(t *testing.T, tool core.Tool, env *core.ToolEnv, args string) (*core.ToolResult, error) {
	t.Helper()
	a, err := plan(t, tool, env, args)
	if err != nil {
		return nil, err
	}
	return tool.Exec(context.Background(), env, a)
}

func TestBuiltinsConformance(t *testing.T) {
	env, _ := workspace(t)
	chat := *env
	chat.Chat = &core.ChatRef{Channel: "telegram", ChatID: "42", Kind: core.ChatDM}
	noChat := *env
	noChat.Chat = nil
	cases := map[string][]conformance.ToolCase{
		fsReadName: {
			{Name: "file", Env: env, Args: json.RawMessage(`{"path":"notes/a.txt"}`)},
			{Name: "range", Env: env, Args: json.RawMessage(`{"path":"notes/a.txt","offset":2,"limit":1}`)},
			{Name: "inner symlink", Env: env, Args: json.RawMessage(`{"path":"inside"}`)},
			{Name: "dotdot", Env: env, Args: json.RawMessage(`{"path":"../outside/secret"}`), WantErr: true},
			{Name: "absolute", Env: env, Args: json.RawMessage(`{"path":"/etc/passwd"}`), WantErr: true},
			{Name: "escaping symlink", Env: env, Args: json.RawMessage(`{"path":"escape"}`), WantErr: true},
			{Name: "directory", Env: env, Args: json.RawMessage(`{"path":"notes"}`), WantErr: true},
			{Name: "missing", Env: env, Args: json.RawMessage(`{"path":"nope"}`), WantErr: true},
			{Name: "bad offset", Env: env, Args: json.RawMessage(`{"path":"top.txt","offset":0}`), WantErr: true},
		},
		fsWriteName: {
			{Name: "new", Env: env, Args: json.RawMessage(`{"path":"out/new.txt","content":"hello\n"}`)},
			{Name: "overwrite", Env: env, Args: json.RawMessage(`{"path":"top.txt","content":"changed\n"}`)},
			{Name: "append", Env: env, Args: json.RawMessage(`{"path":"top.txt","content":"more\n","mode":"append"}`)},
			{Name: "create existing", Env: env, Args: json.RawMessage(`{"path":"top.txt","content":"x","mode":"create"}`), WantErr: true},
			{Name: "escape", Env: env, Args: json.RawMessage(`{"path":"escape_dir/x","content":"x"}`), WantErr: true},
			{Name: "dotdot", Env: env, Args: json.RawMessage(`{"path":"a/../../x","content":"x"}`), WantErr: true},
			{Name: "dir", Env: env, Args: json.RawMessage(`{"path":"notes","content":"x"}`), WantErr: true},
			{Name: "bad mode", Env: env, Args: json.RawMessage(`{"path":"x","content":"x","mode":"truncate"}`), WantErr: true},
		},
		fsListName: {
			{Name: "root", Env: env, Args: json.RawMessage(`{}`)},
			{Name: "recursive", Env: env, Args: json.RawMessage(`{"path":"notes","recursive":true}`)},
			{Name: "file", Env: env, Args: json.RawMessage(`{"path":"top.txt"}`), WantErr: true},
			{Name: "escape", Env: env, Args: json.RawMessage(`{"path":"escape_dir"}`), WantErr: true},
		},
		fsDeleteName: {
			{Name: "file", Env: env, Args: json.RawMessage(`{"path":"top.txt"}`)},
			{Name: "empty dir", Env: env, Args: json.RawMessage(`{"path":"empty"}`)},
			{Name: "dir recursive", Env: env, Args: json.RawMessage(`{"path":"notes","recursive":true}`)},
			{Name: "dir", Env: env, Args: json.RawMessage(`{"path":"notes"}`), WantErr: true},
			{Name: "root", Env: env, Args: json.RawMessage(`{"path":"."}`), WantErr: true},
			{Name: "dotdot", Env: env, Args: json.RawMessage(`{"path":"../outside/secret"}`), WantErr: true},
		},
		messageName: {
			{Name: "text", Env: &chat, Args: json.RawMessage(`{"text":"working on it"}`)},
			{Name: "no chat", Env: &noChat, Args: json.RawMessage(`{"text":"x"}`), WantErr: true},
			{Name: "empty", Env: &chat, Args: json.RawMessage(`{"text":"  "}`), WantErr: true},
			{Name: "recipient", Env: &chat, Args: json.RawMessage(`{"text":"x","to":"other"}`), WantErr: true},
		},
	}
	for _, tool := range Builtins(BuiltinDeps{}) {
		name := tool.Spec().Def.Name
		t.Run(name, func(t *testing.T) { conformance.Tool(t, tool, cases[name]) })
	}
}

func TestFSRootConfinement(t *testing.T) {
	env, _ := workspace(t)
	escapes := []string{"../outside/secret", "/etc/passwd", "escape_dir/secret", "notes/../../outside/secret", "a\x00b"}
	for _, c := range []struct {
		tool  core.Tool
		extra map[string]any
		paths []string
	}{
		// Deleting the "escape" symlinks themselves stays inside; TestFSDelete
		// checks their targets survive.
		{fsRead{}, nil, append(escapes, "escape", "escape_dir")},
		{fsWrite{}, map[string]any{"content": "x"}, append(escapes, "escape", "escape_dir")},
		{fsList{}, map[string]any{"recursive": true}, append(escapes, "escape", "escape_dir")},
		{fsDelete{}, map[string]any{"recursive": true}, escapes},
	} {
		name := c.tool.Spec().Def.Name
		for _, p := range c.paths {
			args := map[string]any{"path": p}
			for k, v := range c.extra {
				args[k] = v
			}
			raw, _ := json.Marshal(args)
			_, err := run(t, c.tool, env, string(raw))
			if err == nil {
				t.Errorf("%s %q: escaped the workspace", name, p)
			} else if strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("%s %q: args rejected before reaching the filesystem: %v", name, p, err)
			}
		}
	}
	// A symlink swapped in after Plan is still refused by os.Root at Exec.
	a, err := plan(t, fsRead{}, env, `{"path":"inside"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(env.Workspace, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside/secret", filepath.Join(env.Workspace, "inside")); err != nil {
		t.Fatal(err)
	}
	if res, err := (fsRead{}).Exec(context.Background(), env, a); err == nil {
		t.Errorf("read through a swapped symlink: %q", res.Content)
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(env.Workspace), "outside", "secret")); string(b) != "s3cr3t\n" {
		t.Error("outside file changed")
	}
}

func TestFSRead(t *testing.T) {
	env, ws := workspace(t)
	res, err := run(t, fsRead{}, env, `{"path":"notes/a.txt"}`)
	if err != nil || res.Content != "alpha\nbeta\ngamma\n" {
		t.Fatalf("read: %+v %v", res, err)
	}
	a, _ := plan(t, fsRead{}, env, `{"path":"./notes//a.txt"}`)
	if a.Paths[0] != "notes/a.txt" || a.Kind != core.ActFSRead || string(a.Payload) != `{"path":"notes/a.txt"}` {
		t.Errorf("action %+v", a)
	}
	res, _ = run(t, fsRead{}, env, `{"path":"notes/a.txt","offset":2,"limit":1}`)
	if !strings.HasPrefix(res.Content, "beta\n") || !strings.Contains(res.Content, "offset 3") {
		t.Errorf("range: %q", res.Content)
	}
	res, _ = run(t, fsRead{}, env, `{"path":"notes/a.txt","offset":3}`)
	if res.Content != "gamma\n" {
		t.Errorf("tail: %q", res.Content)
	}
	res, _ = run(t, fsRead{}, env, `{"path":"notes/a.txt","offset":9}`)
	if !strings.Contains(res.Content, "has 3 lines") {
		t.Errorf("past the end: %q", res.Content)
	}
	res, _ = run(t, fsRead{}, env, `{"path":"bin.dat"}`)
	if !strings.Contains(res.Content, "binary file") || strings.Contains(res.Content, "\x00") {
		t.Errorf("binary: %q", res.Content)
	}
	big := strings.Repeat("0123456789abcdef\n", 40000)
	if err := os.WriteFile(filepath.Join(ws, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ = run(t, fsRead{}, env, `{"path":"big.txt"}`)
	if !res.Truncated || len(res.Content) > readCap+200 || !strings.Contains(res.Content, "truncated at") {
		t.Errorf("cap: truncated=%v len=%d", res.Truncated, len(res.Content))
	}
	oneLine := strings.Repeat("x", readCap*2)
	if err := os.WriteFile(filepath.Join(ws, "line.txt"), []byte(oneLine), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ = run(t, fsRead{}, env, `{"path":"line.txt","offset":1,"limit":5}`)
	if !res.Truncated || len(res.Content) > readCap+200 {
		t.Errorf("single huge line: truncated=%v len=%d", res.Truncated, len(res.Content))
	}
	if spec := (fsRead{}).Spec(); spec.Manifest.Taint != core.TaintPrivate || !spec.Manifest.SpaceOK {
		t.Errorf("manifest %+v", spec.Manifest)
	}
}

func TestFSWrite(t *testing.T) {
	env, ws := workspace(t)
	a, err := plan(t, fsWrite{}, env, `{"path":"top.txt","content":"top\nmore\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != core.ActFSWrite || a.SideEffect != core.SELocalWrite || a.Diff != "--- a/top.txt\n+++ b/top.txt\n@@ -1 +1,2 @@\n top\n+more\n" {
		t.Errorf("action %+v\n%s", a, a.Diff)
	}
	if _, err := (fsWrite{}).Exec(context.Background(), env, a); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "top.txt")); string(b) != "top\nmore\n" {
		t.Errorf("content %q", b)
	}
	a, _ = plan(t, fsWrite{}, env, `{"path":"new/dir/f.txt","content":"x","mode":"create"}`)
	if !strings.HasPrefix(a.Diff, "--- /dev/null\n+++ b/new/dir/f.txt\n@@ -0,0 +1 @@\n+x\n\\ No newline at end of file\n") {
		t.Errorf("create diff %q", a.Diff)
	}
	if _, err := (fsWrite{}).Exec(context.Background(), env, a); err != nil {
		t.Fatal(err)
	}
	if _, err := (fsWrite{}).Exec(context.Background(), env, a); err == nil {
		t.Error("create raced over an existing file")
	}
	if _, err := run(t, fsWrite{}, env, `{"path":"top.txt","content":"end\n","mode":"append"}`); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(ws, "top.txt")); string(b) != "top\nmore\nend\n" {
		t.Errorf("append %q", b)
	}
	entries, _ := os.ReadDir(ws)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	huge := `{"path":"h","content":"` + strings.Repeat("x", maxWriteSize+1) + `"}`
	if _, err := plan(t, fsWrite{}, env, huge); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("oversized content: %v", err)
	}
}

func TestFSWriteQuota(t *testing.T) {
	env, ws := workspace(t)
	sb := &sandboxtest.Sandbox{QuotaBytes: 100, UsedBytes: map[core.WorkspaceID]int64{env.Principal.Workspace: 95}}
	w := fsWrite{sandbox: sb}
	if _, err := run(t, w, env, `{"path":"q.txt","content":"0123456789"}`); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Errorf("over quota: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "q.txt")); !os.IsNotExist(err) {
		t.Error("over-quota write happened")
	}
	if _, err := run(t, w, env, `{"path":"top.txt","content":"t"}`); err != nil {
		t.Errorf("shrinking write refused: %v", err)
	}
	if _, err := run(t, w, env, `{"path":"q.txt","content":"0123"}`); err != nil {
		t.Errorf("write within quota: %v", err)
	}
}

func TestFSList(t *testing.T) {
	env, _ := workspace(t)
	res, err := run(t, fsList{}, env, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"notes/", "empty/", "top.txt  4 B", "escape@", "inside@"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("listing lacks %q:\n%s", want, res.Content)
		}
	}
	res, _ = run(t, fsList{}, env, `{"path":"notes","recursive":true}`)
	if !strings.Contains(res.Content, "deep/b.md") || !strings.Contains(res.Content, "deep/") {
		t.Errorf("recursive:\n%s", res.Content)
	}
	res, _ = run(t, fsList{}, env, `{"path":"empty"}`)
	if res.Content != "empty is empty." {
		t.Errorf("empty: %q", res.Content)
	}
}

func TestFSDelete(t *testing.T) {
	env, ws := workspace(t)
	a, err := plan(t, fsDelete{}, env, `{"path":"notes/a.txt"}`)
	if err != nil {
		t.Fatal(err)
	}
	if a.SideEffect != core.SEIrreversible || a.Kind != core.ActFSDelete || !strings.Contains(a.Diff, "-beta\n") || !strings.Contains(a.Diff, "+++ /dev/null") {
		t.Errorf("action %+v", a)
	}
	if _, err := (fsDelete{}).Exec(context.Background(), env, a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "notes", "a.txt")); !os.IsNotExist(err) {
		t.Error("not deleted")
	}
	if _, err := run(t, fsDelete{}, env, `{"path":"escape"}`); err != nil {
		t.Errorf("deleting a symlink itself: %v", err)
	}
	if _, err := run(t, fsDelete{}, env, `{"path":"escape_dir","recursive":true}`); err != nil {
		t.Errorf("deleting a directory symlink itself: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(ws), "outside", "secret")); string(b) != "s3cr3t\n" {
		t.Error("symlink target removed")
	}
	if _, err := run(t, fsDelete{}, env, `{"path":"notes","recursive":true}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "notes")); !os.IsNotExist(err) {
		t.Error("recursive delete failed")
	}
	if spec := (fsDelete{}).Spec(); spec.Manifest.Core || spec.Manifest.SpaceOK {
		t.Errorf("fs_delete must be lazy and not offered to spaces: %+v", spec.Manifest)
	}
}

func TestMessage(t *testing.T) {
	hub := &fakes.Hub{}
	m := message{messenger: hub}
	env := ownerEnv()
	if !m.Enabled(env) {
		t.Fatal("disabled with a chat")
	}
	a, err := plan(t, m, env, `{"text":"half way"}`)
	if err != nil {
		t.Fatal(err)
	}
	if a.Recipient != env.Chat.Key() || a.Kind != core.ActSend || a.SideEffect != core.SELocalWrite {
		t.Errorf("action %+v", a)
	}
	if _, err := m.Exec(context.Background(), env, a); err != nil {
		t.Fatal(err)
	}
	if sent := hub.Messages(); len(sent) != 1 || sent[0] != env.Chat.Key()+"|half way" {
		t.Errorf("sent %v", sent)
	}
	other := *env
	other.Chat = &core.ChatRef{Channel: "telegram", ChatID: "99", Kind: core.ChatDM}
	if _, err := m.Exec(context.Background(), &other, a); err == nil {
		t.Error("sent to a chat other than the planned one")
	}
	for _, c := range []*core.ChatRef{nil, {Channel: "web", ChatID: "x"}, {Channel: "api", ChatID: "x"}} {
		e := *env
		e.Chat = c
		if m.Enabled(&e) {
			t.Errorf("enabled for %+v", c)
		}
	}
	if spec := m.Spec(); !spec.Manifest.Messaging || spec.Manifest.SideEffect != core.SELocalWrite {
		t.Errorf("manifest %+v", spec.Manifest)
	}
	if strings.Contains(string(m.Spec().Def.Schema), "recipient") || strings.Contains(string(m.Spec().Def.Schema), `"to"`) {
		t.Error("message schema exposes a recipient")
	}
}

func TestFSWriteKeepsPermissions(t *testing.T) {
	env, ws := workspace(t)
	script := filepath.Join(ws, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, fsWrite{}, env, `{"path":"run.sh","content":"#!/bin/sh\necho hi\n"}`); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o750 {
		t.Errorf("overwrite changed the mode to %v", st.Mode().Perm())
	}
}

// expiringCtx reports no error for its first n Err calls, then Canceled, so
// a test can cancel in the middle of a read.
type expiringCtx struct {
	context.Context
	n atomic.Int32
}

func (c *expiringCtx) Err() error {
	if c.n.Add(-1) >= 0 {
		return nil
	}
	return context.Canceled
}

func TestFSReadHonoursCancellation(t *testing.T) {
	env, ws := workspace(t)
	if err := os.WriteFile(filepath.Join(ws, "long.txt"), []byte(strings.Repeat("line\n", 20000)), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := plan(t, fsRead{}, env, `{"path":"long.txt","offset":19999}`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &expiringCtx{Context: context.Background()}
	ctx.n.Store(2)
	if _, err := (fsRead{}).Exec(ctx, env, a); !errors.Is(err, context.Canceled) {
		t.Errorf("read cancelled while skipping lines: %v", err)
	}
	res, err := (fsRead{}).Exec(context.Background(), env, a)
	if err != nil || res.Content != "line\nline\n" {
		t.Errorf("read: %v %+v", err, res)
	}
}
