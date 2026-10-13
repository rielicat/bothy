//go:build docker || runsc

package sandbox

// Acceptance tests against a real sandboxd and Docker engine (SPEC §12.2,
// M5). They run inside a container that shares the work and run volumes
// with sandboxd, as the gateway does in compose; deploy/sandbox/test/
// acceptance.sh sets that up. Configuration:
//
//	BOTHY_TEST_SANDBOXD_SOCKET  sandboxd's socket (required, else skipped)
//	BOTHY_TEST_WORK, BOTHY_TEST_RUN  the volumes' mount points
//	BOTHY_TEST_NET=1  the test container may reach example.com:443

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/sandboxd/proto"
)

// proxyEgress is a minimal stand-in for the egress module: one unix-socket
// proxy per box that tunnels CONNECT to allowlisted host:port pairs only.
type proxyEgress struct {
	core.Egress
	run string

	mu    sync.Mutex
	allow map[string]bool
	lns   map[string]net.Listener
}

func (p *proxyEgress) OpenBox(_ context.Context, key string, _ core.BoxEgress) (string, error) {
	sub := proto.EgressSubpath(key)
	sock := filepath.Join(p.run, sub, "proxy.sock")
	_ = os.Remove(sock)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(sock, 0o660); err != nil {
		return "", err
	}
	if err := os.Lchown(sock, -1, proto.BoxUID); err != nil {
		return "", err
	}
	p.mu.Lock()
	if p.lns == nil {
		p.lns = map[string]net.Listener{}
	}
	p.lns[key] = ln
	p.mu.Unlock()
	go p.serve(ln)
	return sub, nil
}

func (p *proxyEgress) CloseBox(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ln := p.lns[key]; ln != nil {
		ln.Close()
		delete(p.lns, key)
	}
}

func (p *proxyEgress) Allow(hostport string) {
	p.mu.Lock()
	if p.allow == nil {
		p.allow = map[string]bool{}
	}
	p.allow[hostport] = true
	p.mu.Unlock()
}

func (p *proxyEgress) serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go p.handle(c)
	}
}

func (p *proxyEgress) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	target := req.Host
	if req.Method != http.MethodConnect {
		target = req.URL.Host
		if !strings.Contains(target, ":") {
			target += ":80"
		}
	}
	p.mu.Lock()
	ok := req.Method == http.MethodConnect && p.allow[target]
	p.mu.Unlock()
	if !ok {
		fmt.Fprintf(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
		return
	}
	up, err := (&net.Dialer{Timeout: 10 * time.Second}).Dial("tcp", target)
	if err != nil {
		fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer up.Close()
	fmt.Fprintf(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
}

type dockerEnv struct {
	c    *Client
	eg   *proxyEgress
	work string
	cfg  *config.Config
}

func (d *dockerEnv) sh(t *testing.T, key, cmd string, timeout time.Duration) *core.ExecResult {
	t.Helper()
	res, err := d.c.Exec(context.Background(), key, core.ExecRequest{Argv: []string{"bash", "-lc", cmd}, Timeout: timeout, MaxOutput: 1 << 20}, nil)
	if err != nil {
		t.Fatalf("exec %q: %v", cmd, err)
	}
	return res
}

func (d *dockerEnv) ensure(t *testing.T, purpose string) (string, core.WorkspaceID) {
	t.Helper()
	key := core.DerivedID(core.PrefixBox, "session:"+purpose+core.NewID(core.PrefixSession))
	ws := core.WorkspaceID(core.NewID(core.PrefixWorkspace))
	err := d.c.Ensure(context.Background(), core.BoxSpec{Key: key, Workspace: ws,
		Limits: core.BoxLimits{MemoryMB: 256, CPUs: 1, Pids: 64}, Egress: core.BoxEgress{Session: "ses_test", Principal: "usr_test"}})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(func() { _ = d.c.Destroy(context.Background(), key) })
	return key, ws
}

// newDockerEnv connects to the sandboxd under test and waits until its
// self-test has settled (the first one may pull images).
func newDockerEnv(t *testing.T) (*dockerEnv, core.SandboxStatus) {
	t.Helper()
	sock := os.Getenv("BOTHY_TEST_SANDBOXD_SOCKET")
	if sock == "" {
		t.Skip("BOTHY_TEST_SANDBOXD_SOCKET not set (run deploy/sandbox/test/acceptance.sh)")
	}
	syscall.Umask(0o007) // the gateway's umask (cmd/bothy)
	work, run := os.Getenv("BOTHY_TEST_WORK"), os.Getenv("BOTHY_TEST_RUN")
	cfg := config.Default()
	cfg.Sandbox.Backend, cfg.Sandbox.Socket = "sandboxd", sock
	eg := &proxyEgress{run: run}
	ctx := context.Background()
	c, err := New(ctx, Deps{Config: config.Static(cfg), Egress: eg, WorkDir: work, RunDir: run,
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		st := c.svc.refresh(ctx)
		if st.Available || (len(st.SelfTest) > 0 && !strings.Contains(st.Reason, "not reachable")) {
			raw, _ := json.MarshalIndent(st, "", "  ")
			t.Logf("status:\n%s", raw)
			return &dockerEnv{c: c, eg: eg, work: work, cfg: cfg}, st
		}
		if time.Now().After(deadline) {
			raw, _ := json.MarshalIndent(st, "", "  ")
			t.Fatalf("sandboxd never settled:\n%s", raw)
		}
		time.Sleep(time.Second)
	}
}

func TestDockerAcceptance(t *testing.T) {
	d, st := newDockerEnv(t)
	if !st.Available {
		t.Fatalf("sandbox unavailable: %s", st.Reason)
	}
	c, eg, work, cfg, ctx := d.c, d.eg, d.work, d.cfg, context.Background()

	t.Run("self-test passes", func(t *testing.T) {
		for _, chk := range st.SelfTest {
			if !chk.OK && chk.Name != proto.CheckRunsc {
				t.Errorf("check %s failed: %s", chk.Name, chk.Detail)
			}
		}
		if len(st.SelfTest) < 10 || st.Runtime == "" {
			t.Fatalf("incomplete self-test: %+v", st)
		}
	})

	key, ws := d.ensure(t, "main")

	t.Run("uid is 10001", func(t *testing.T) {
		if res := d.sh(t, key, "id -u; id -g", 30*time.Second); string(res.Stdout) != "10001\n10001\n" {
			t.Fatalf("id = %q (exit %d, %s)", res.Stdout, res.Exit, res.Stderr)
		}
	})

	t.Run("run dir and runtime socket are not visible", func(t *testing.T) {
		res := d.sh(t, key, `test ! -e /run/bothy && test ! -e /var/run/docker.sock && test ! -e /run/docker.sock && `+
			`! grep -q -e docker.sock -e /run/bothy /proc/self/mountinfo && echo invisible`, 30*time.Second)
		if strings.TrimSpace(string(res.Stdout)) != "invisible" {
			t.Fatalf("exit %d stdout %q stderr %q", res.Exit, res.Stdout, res.Stderr)
		}
	})

	t.Run("dns fails", func(t *testing.T) {
		if res := d.sh(t, key, "getent hosts example.com", 30*time.Second); res.Exit == 0 {
			t.Fatalf("example.com resolved: %s", res.Stdout)
		}
	})

	t.Run("cloud metadata unreachable", func(t *testing.T) {
		for _, cmd := range []string{"curl -sf --max-time 5 http://169.254.169.254/latest/meta-data/",
			"curl -sf --max-time 5 --noproxy '*' http://169.254.169.254/latest/meta-data/"} {
			if res := d.sh(t, key, cmd, 30*time.Second); res.Exit == 0 {
				t.Errorf("%s succeeded: %s", cmd, res.Stdout)
			}
		}
	})

	t.Run("egress needs a grant", func(t *testing.T) {
		curl := "curl -sS -o /dev/null -w '%{http_code}' --max-time 30 https://example.com/"
		if res := d.sh(t, key, curl, time.Minute); res.Exit == 0 {
			t.Fatalf("reached example.com without a grant: %s", res.Stdout)
		}
		if os.Getenv("BOTHY_TEST_NET") != "1" {
			t.Skip("BOTHY_TEST_NET != 1: no internet for the granted half")
		}
		eg.Allow("example.com:443")
		res := d.sh(t, key, curl, time.Minute)
		if res.Exit != 0 || string(res.Stdout) != "200" {
			t.Fatalf("with a grant: exit %d stdout %q stderr %q", res.Exit, res.Stdout, res.Stderr)
		}
	})

	t.Run("exec timeout", func(t *testing.T) {
		res := d.sh(t, key, "sleep 30", 2*time.Second)
		if !res.TimedOut || res.Exit != 124 {
			t.Fatalf("timeout result %+v", res)
		}
	})

	t.Run("kill removes double-forked processes within 1s", func(t *testing.T) {
		res := d.sh(t, key, `echo marker > /tmp/marker; `+
			`(setsid sleep 600 >/dev/null 2>&1 &); setsid sh -c 'sleep 600 >/dev/null 2>&1 &'; sleep 0.5; pgrep -c -x sleep`, 30*time.Second)
		if n := strings.TrimSpace(string(res.Stdout)); n != "2" {
			t.Fatalf("expected 2 detached sleeps, got %q (%s)", n, res.Stderr)
		}
		start := time.Now()
		if err := c.Kill(ctx, key); err != nil {
			t.Fatal(err)
		}
		took := time.Since(start)
		// No sleep is left, and /tmp (a tmpfs) survived: killall did it and
		// the box was not recreated.
		res = d.sh(t, key, "pgrep -x sleep; cat /tmp/marker", 30*time.Second)
		if string(res.Stdout) != "marker\n" {
			t.Fatalf("after Kill: %q (stderr %q)", res.Stdout, res.Stderr)
		}
		if took > time.Second {
			t.Fatalf("Kill took %v", took)
		}
		t.Logf("Kill took %v", took)
	})

	t.Run("fork bomb is contained by pids", func(t *testing.T) {
		if res := d.sh(t, key, "cat /sys/fs/cgroup/pids.max", 30*time.Second); strings.TrimSpace(string(res.Stdout)) != "64" {
			t.Fatalf("pids.max = %q", res.Stdout)
		}
		res := d.sh(t, key, `bomb() { bomb | bomb & }; bomb; sleep 3; echo survived`, 8*time.Second)
		t.Logf("bomb: exit %d timed out %v", res.Exit, res.TimedOut)
		if err := c.Kill(ctx, key); err != nil {
			t.Fatal(err)
		}
		if res := d.sh(t, key, "echo alive", 30*time.Second); string(res.Stdout) != "alive\n" {
			t.Fatalf("box unusable after the bomb: %+v %s", res, res.Stderr)
		}
	})

	t.Run("workspace files are shared through group 10001", func(t *testing.T) {
		dir := filepath.Join(work, "ws", string(ws))
		res := d.sh(t, key, "echo from-box > /workspace/from-box.txt && stat -c '%a %g' /workspace/from-box.txt", 30*time.Second)
		if strings.TrimSpace(string(res.Stdout)) != "664 10001" {
			t.Fatalf("box file mode/group = %q (%s)", res.Stdout, res.Stderr)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "from-box.txt")); err != nil || string(b) != "from-box\n" {
			t.Fatalf("gateway read %q, %v", b, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "to-box.txt"), []byte("from-gateway\n"), 0o660); err != nil {
			t.Fatal(err)
		}
		res = d.sh(t, key, "cat /workspace/to-box.txt && echo appended >> /workspace/to-box.txt", 30*time.Second)
		if res.Exit != 0 || string(res.Stdout) != "from-gateway\n" {
			t.Fatalf("box read %q (exit %d, %s)", res.Stdout, res.Exit, res.Stderr)
		}
		fi, err := os.Stat(filepath.Join(dir, "to-box.txt"))
		if err != nil || fi.Mode()&fs.ModePerm != 0o660 || fi.Sys().(*syscall.Stat_t).Gid != proto.BoxUID {
			t.Fatalf("gateway file %v gid %v, %v", fi.Mode(), fi.Sys().(*syscall.Stat_t).Gid, err)
		}
	})

	t.Run("a second session's workspace is invisible", func(t *testing.T) {
		key2, ws2 := d.ensure(t, "second")
		secret := filepath.Join(work, "ws", string(ws2), "secret-"+string(ws2)+".txt")
		if err := os.WriteFile(secret, []byte("s3cret"), 0o660); err != nil {
			t.Fatal(err)
		}
		res := d.sh(t, key, `find / -xdev -name 'secret-*' 2>/dev/null | wc -l; ls /workspace | grep -c secret-; `+
			`grep -c ' /workspace ' /proc/self/mountinfo`, time.Minute)
		if strings.Fields(string(res.Stdout))[0] != "0" || strings.Fields(string(res.Stdout))[1] != "0" ||
			strings.Fields(string(res.Stdout))[2] != "1" {
			t.Fatalf("first box sees the second workspace: %q", res.Stdout)
		}
		if res := d.sh(t, key2, "cat /workspace/secret-*", 30*time.Second); string(res.Stdout) != "s3cret" {
			t.Fatalf("second box cannot read its own file: %q", res.Stdout)
		}
	})

	t.Run("stdio round trip", func(t *testing.T) {
		rwc, err := c.Stdio(ctx, key, []string{"cat"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer rwc.Close()
		if _, err := io.WriteString(rwc, "{\"jsonrpc\":\"2.0\"}\n"); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(rwc).ReadString('\n')
		if err != nil || line != "{\"jsonrpc\":\"2.0\"}\n" {
			t.Fatalf("stdio %q, %v", line, err)
		}
	})

	t.Run("python tool end to end", func(t *testing.T) {
		_, python := newShell(config.Static(cfg)), newPython(config.Static(cfg))
		env := &core.ToolEnv{CallID: core.NewID(core.PrefixToolCall), BoxKey: key, Sandbox: c, Workspace: filepath.Join(work, "ws", string(ws))}
		a, err := python.Plan(ctx, env, json.RawMessage(`{"code":"import os\nprint(os.getuid(), sum(range(10)))"}`))
		if err != nil {
			t.Fatal(err)
		}
		res, err := python.Exec(ctx, env, a)
		if err != nil || res.IsError || !strings.Contains(res.Content, "10001 45") {
			t.Fatalf("python: %+v, %v", res, err)
		}
	})

	t.Run("a stdio server that ignores EOF does not hold the box", func(t *testing.T) {
		rwc, err := c.Stdio(ctx, key, []string{"sleep", "600"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		rwc.Close()
		deadline := time.Now().Add(20 * time.Second)
		for {
			again, err := c.Stdio(ctx, key, []string{"cat"}, nil)
			if err == nil {
				again.Close()
				break
			}
			if !errors.Is(err, core.ErrBusy) || time.Now().After(deadline) {
				t.Fatalf("second stdio stream: %v", err)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if err := c.Kill(ctx, key); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a background process that logs to stdout outlives its exec", func(t *testing.T) {
		res := d.sh(t, key, `(while :; do echo tick; sleep 0.1; done) & echo $! > /tmp/bg.pid; echo started`, 30*time.Second)
		if res.Exit != 0 || !strings.HasPrefix(string(res.Stdout), "started\n") || res.Duration > 5*time.Second {
			t.Fatalf("exec: %+v %q", res, res.Stdout)
		}
		time.Sleep(time.Second)
		if res := d.sh(t, key, "kill -0 $(cat /tmp/bg.pid) && echo alive", 30*time.Second); string(res.Stdout) != "alive\n" {
			t.Fatalf("background writer died with its exec: %q %q", res.Stdout, res.Stderr)
		}
		if err := c.Kill(ctx, key); err != nil {
			t.Fatal(err)
		}
	})
}
