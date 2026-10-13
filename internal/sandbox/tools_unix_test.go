//go:build unix

package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/sandboxtest"
)

// The box can write the workspace and knows where a given script will land:
// a FIFO planted there must not block the gateway, and a symlink must not
// redirect the write.
func TestPythonScriptReplacesPlantedFiles(t *testing.T) {
	_, python := testTools()
	sb := &sandboxtest.Sandbox{ExecFunc: func(context.Context, string, core.ExecRequest) (*core.ExecResult, error) {
		return &core.ExecResult{}, nil
	}}
	env, _ := toolEnv(t, sb)
	for _, plant := range []func(path string) error{
		func(p string) error { return syscall.Mkfifo(p, 0o660) },
		func(p string) error { return os.Symlink(filepath.Join(env.Workspace, "victim"), p) },
	} {
		a, err := python.Plan(context.Background(), env, json.RawMessage(`{"code":"print(42)"}`))
		if err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(env.Workspace, a.Argv[1])
		if err := os.MkdirAll(filepath.Dir(script), 0o770); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(script)
		if err := plant(script); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := python.Exec(context.Background(), env, a)
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("python blocked on a planted file")
		}
		fi, err := os.Lstat(script)
		if err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("script is %v, %v", fi.Mode(), err)
		}
		if b, _ := os.ReadFile(script); string(b) != "print(42)" {
			t.Fatalf("script = %q", b)
		}
		if _, err := os.Stat(filepath.Join(env.Workspace, "victim")); !os.IsNotExist(err) {
			t.Fatalf("write followed the symlink: %v", err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(env.Workspace, scriptDir))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}
