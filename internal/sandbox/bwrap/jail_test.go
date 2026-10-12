//go:build bwrap && linux

package bwrap

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/sandboxd/proto"
	"github.com/bothyhq/bothy/internal/sbxinit"
)

// The jail runs /opt/bothy/bothy sbx-init …; here that binary is this test
// binary, so it dispatches sbx-init itself.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "sbx-init" {
		os.Exit(sbxinit.Main(os.Args[2:]))
	}
	os.Exit(m.Run())
}

type jailEnv struct {
	b    *Backend
	j    Jail
	work string
}

func newJail(t *testing.T) *jailEnv {
	t.Helper()
	o, version, checks, err := Probe(context.Background())
	if err != nil {
		t.Fatalf("bwrap probe: %v (%+v)", err, checks)
	}
	dir := t.TempDir()
	o.WorkDir, o.RunDir = filepath.Join(dir, "work"), filepath.Join(dir, "run")
	b, err := New(o, version)
	if err != nil {
		t.Fatal(err)
	}
	j := Jail{Key: "box_0123456789abcdefghjkmnpqrs", Workspace: "ws_0123456789abcdefghjkmnpqrs"}
	for _, d := range []string{filepath.Join(o.WorkDir, "ws", j.Workspace), filepath.Join(o.RunDir, proto.EgressSubpath(j.Key))} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return &jailEnv{b: b, j: j, work: o.WorkDir}
}

func (e *jailEnv) run(t *testing.T, timeout time.Duration, argv ...string) *core.ExecResult {
	t.Helper()
	res, err := e.b.Exec(context.Background(), e.j, core.ExecRequest{Argv: argv, Timeout: timeout, MaxOutput: 1 << 20}, nil)
	if err != nil {
		t.Fatalf("exec %q: %v", argv, err)
	}
	return res
}

func TestJailEnvironmentIsClean(t *testing.T) {
	t.Setenv("BOTHY_SECRET_TEST", "hunter2")
	e := newJail(t)
	res := e.run(t, 10*time.Second, "env")
	if res.Exit != 0 {
		t.Fatalf("env: exit %d %s", res.Exit, res.Stderr)
	}
	if strings.Contains(string(res.Stdout), "BOTHY_") || strings.Contains(string(res.Stdout), "hunter2") {
		t.Fatalf("gateway environment leaked into the jail:\n%s", res.Stdout)
	}
	for _, want := range []string{"HOME=/home/sbx", "HTTPS_PROXY=http://127.0.0.1:3128", "PATH=/usr/local/bin:/usr/bin:/bin"} {
		if !strings.Contains(string(res.Stdout), want) {
			t.Errorf("missing %s in:\n%s", want, res.Stdout)
		}
	}
	res = e.run(t, 10*time.Second, "sh", "-c", "id -u; id -g; pwd")
	if string(res.Stdout) != "10001\n10001\n/workspace\n" {
		t.Fatalf("identity = %q", res.Stdout)
	}
}

func TestJailNetworkOnlyThroughTheRelay(t *testing.T) {
	e := newJail(t)
	sock := filepath.Join(e.b.o.RunDir, proto.EgressSubpath(e.j.Key), "proxy.sock")
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			fmt.Fprintln(c, "relay-ok")
			c.Close()
		}
	}()
	res := e.run(t, 20*time.Second, "bash", "-c",
		`for i in 1 2 3 4 5 6 7 8 9 10; do exec 3<>/dev/tcp/127.0.0.1/3128 2>/dev/null && break; sleep 0.2; done; head -1 <&3`)
	if strings.TrimSpace(string(res.Stdout)) != "relay-ok" {
		t.Fatalf("relay: exit %d stdout %q stderr %q", res.Exit, res.Stdout, res.Stderr)
	}
	res = e.run(t, 20*time.Second, "bash", "-c", `echo > /dev/tcp/1.1.1.1/443`)
	if res.Exit == 0 {
		t.Fatal("direct connection to 1.1.1.1:443 succeeded")
	}
	res = e.run(t, 20*time.Second, "getent", "hosts", "example.com")
	if res.Exit == 0 {
		t.Fatalf("DNS resolved inside the jail: %s", res.Stdout)
	}
}

func TestJailPtraceDenied(t *testing.T) {
	e := newJail(t)
	res := e.run(t, 20*time.Second, "python3", "-c",
		"import ctypes; libc = ctypes.CDLL(None, use_errno=True); print(libc.ptrace(0, 0, 0, 0), ctypes.get_errno())")
	if strings.TrimSpace(string(res.Stdout)) != "-1 1" {
		t.Fatalf("ptrace(PTRACE_TRACEME) = %q (stderr %q), want -1 EPERM", res.Stdout, res.Stderr)
	}
	res = e.run(t, 20*time.Second, "unshare", "-U", "true")
	if res.Exit == 0 {
		t.Fatal("nested user namespace created")
	}
}

func TestJailTimeoutAndKill(t *testing.T) {
	e := newJail(t)
	res := e.run(t, time.Second, "sleep", "30")
	if !res.TimedOut || res.Exit != 124 || res.Duration > 10*time.Second {
		t.Fatalf("timeout: %+v", res)
	}
	done := make(chan *core.ExecResult, 1)
	go func() { done <- e.run(t, time.Minute, "sh", "-c", "setsid sleep 600 >/dev/null 2>&1 & sleep 600") }()
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	if n := e.b.Kill(e.j.Key); n != 1 {
		t.Fatalf("Kill found %d jails", n)
	}
	select {
	case res := <-done:
		if !res.Killed {
			t.Fatalf("killed exec: %+v", res)
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("kill took %v", took)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exec survived Kill")
	}
}

func TestJailWorkspaceIsShared(t *testing.T) {
	e := newJail(t)
	res := e.run(t, 10*time.Second, "sh", "-c", "echo from-jail > /workspace/out.txt && cat /workspace/in.txt 2>/dev/null || true")
	if res.Exit != 0 {
		t.Fatalf("write: %+v %s", res, res.Stderr)
	}
	b, err := os.ReadFile(filepath.Join(e.work, "ws", e.j.Workspace, "out.txt"))
	if err != nil || string(b) != "from-jail\n" {
		t.Fatalf("gateway reads %q, %v", b, err)
	}
	res = e.run(t, 10*time.Second, "sh", "-c", "touch /usr/x 2>&1; ls /run")
	if strings.Contains(string(res.Stdout), "bothy") {
		t.Fatalf("host run dir visible: %s", res.Stdout)
	}
}
