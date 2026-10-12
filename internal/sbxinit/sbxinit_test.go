//go:build unix

package sbxinit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/sandboxd/proto"
)

// TestMain lets tests run the package's Main in a child process: run sets
// rlimits, umask and signal handlers that must not touch the test binary.
func TestMain(m *testing.M) {
	if os.Getenv("SBXINIT_TEST_MAIN") == "1" {
		os.Exit(Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func sbxInit(t *testing.T, stdin string, args ...string) (stdout, stderr string, code int, took time.Duration) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "SBXINIT_TEST_MAIN=1")
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := time.Now()
	err := cmd.Run()
	took = time.Since(start)
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		t.Fatalf("running sbx-init: %v", err)
	}
	return out.String(), errb.String(), code, took
}

func TestRunExitCodeAndStreams(t *testing.T) {
	out, errOut, code, _ := sbxInit(t, "", "run", "--timeout", "10s", "--", "sh", "-c", "echo out; echo err >&2; exit 3")
	if code != 3 || out != "out\n" || errOut != "err\n" {
		t.Fatalf("got code %d stdout %q stderr %q", code, out, errOut)
	}
}

func TestRunStdin(t *testing.T) {
	out, _, code, _ := sbxInit(t, "piped input", "run", "--timeout", "10s", "--", "cat")
	if code != 0 || out != "piped input" {
		t.Fatalf("got code %d stdout %q", code, out)
	}
}

func TestRunUmaskAndRlimits(t *testing.T) {
	out, _, code, _ := sbxInit(t, "", "run", "--timeout", "10s", "--", "sh", "-c", "umask; ulimit -n; ulimit -c")
	if code != 0 || strings.Fields(out)[0] != "0002" || strings.Fields(out)[1] != "1024" || strings.Fields(out)[2] != "0" {
		t.Fatalf("umask/nofile/core = %q (code %d)", out, code)
	}
}

func TestRunTimeoutKillsTheGroup(t *testing.T) {
	// The child ignores SIGTERM (and its background sleep inherits that), so
	// only the SIGKILL after --kill-after ends them.
	out, _, code, took := sbxInit(t, "", "run", "--timeout", "300ms", "--kill-after", "300ms", "--",
		"sh", "-c", `trap "" TERM; sleep 30 & echo $!; wait`)
	if code != exitTimeout {
		t.Fatalf("exit = %d, want %d", code, exitTimeout)
	}
	if took > 5*time.Second {
		t.Fatalf("took %v", took)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("no background pid in %q", out)
	}
	deadline := time.Now().Add(3 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("background process %d survived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// alive reports whether pid runs. A killed orphan may linger as a zombie
// when nobody reaps it (the test binary as a container's PID 1).
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		st := procState(stat)
		return st != 'Z' && st != 'X'
	}
	return true
}

func TestRunBackgroundProcessDoesNotHoldTheExec(t *testing.T) {
	out, _, code, took := sbxInit(t, "", "run", "--timeout", "20s", "--", "sh", "-c", "sleep 3 & echo started")
	if code != 0 || out != "started\n" {
		t.Fatalf("code %d stdout %q", code, out)
	}
	if took > 2*time.Second {
		t.Fatalf("exec waited for the background process: %v", took)
	}
}

func TestRunSignalledChild(t *testing.T) {
	_, _, code, _ := sbxInit(t, "", "run", "--timeout", "10s", "--", "sh", "-c", "kill -KILL $$")
	if code != 128+int(syscall.SIGKILL) {
		t.Fatalf("exit = %d, want 137", code)
	}
}

func TestRunNotFound(t *testing.T) {
	_, errOut, code, _ := sbxInit(t, "", "run", "--timeout", "10s", "--", "no-such-command-bothy")
	if code != exitNotFound || !strings.Contains(errOut, "not found") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	_, _, code, _ = sbxInit(t, "", "run", "--timeout", "10s", "--", "/nonexistent/x")
	if code != exitNotFound {
		t.Fatalf("absolute missing path: code %d", code)
	}
}

func TestBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--", "true"},
		{"run", "--timeout", "1s", "true"},
		{"run", "--timeout", "nope", "--", "true"},
		{"run", "--timeout", "1s", "--frobnicate", "--", "true"},
		{"run", "--timeout", "1s", "--"},
		{"--relay", "/x.sock", "killall"},
		{"selftest", "--runtime"},
		{"selftest", "--bogus", "x"},
		{"frobnicate"},
	} {
		if _, _, code, _ := sbxInit(t, "", args...); code != exitInternal {
			t.Errorf("%q: exit %d, want %d", args, code, exitInternal)
		}
	}
}

func TestParseRunArgs(t *testing.T) {
	o, err := parseRunArgs([]string{"--timeout", "2m", "--kill-after", "1s", "--", "bash", "-lc", "echo --timeout"})
	if err != nil || o.timeout != 2*time.Minute || o.killAfter != time.Second || len(o.argv) != 3 || o.argv[2] != "echo --timeout" {
		t.Fatalf("parseRunArgs = %+v, %v", o, err)
	}
	o, err = parseRunArgs([]string{"--timeout", "1s", "--", "x"})
	if err != nil || o.killAfter != 5*time.Second {
		t.Fatalf("default kill-after: %+v, %v", o, err)
	}
}

func TestLookPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if p, err := lookPath("tool", "/nonexistent:"+dir); err != nil || p != exe {
		t.Fatalf("lookPath = %q, %v", p, err)
	}
	if _, err := lookPath("data", dir); !errors.Is(err, errNotFound) {
		t.Fatalf("non-executable file found: %v", err)
	}
	if _, err := lookPath(filepath.Join(dir, "data"), ""); err == nil {
		t.Fatal("non-executable absolute path accepted")
	}
}

func writeProc(t *testing.T, root string, pid int, stat, status string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCountProcs(t *testing.T) {
	root := t.TempDir()
	box := "Name:\tx\nUid:\t10001\t10001\t10001\t10001\n"
	writeProc(t, root, 1, "1 (sbx-init) S 0", box)
	writeProc(t, root, 40, "40 (sbx-init) R 0", box) // self
	writeProc(t, root, 100, "100 (my (odd) name) S 1", box)
	writeProc(t, root, 101, "101 (defunct) Z 1", box)
	writeProc(t, root, 102, "102 (other) S 1", "Uid:\t0\t0\t0\t0\n")
	writeProc(t, root, 103, "103 (sleep) D 1", box)
	if err := os.MkdirAll(filepath.Join(root, "sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if n, err := countProcs(root, 40, 10001); err != nil || n != 2 {
		t.Fatalf("same-uid count = %d, %v; want 2", n, err)
	}
	if n, err := countProcs(root, 40, -1); err != nil || n != 3 {
		t.Fatalf("all count = %d, %v; want 3", n, err)
	}
	if _, err := countProcs(filepath.Join(root, "missing"), 1, -1); err == nil {
		t.Fatal("missing proc dir: want error")
	}
}

func TestProcState(t *testing.T) {
	for in, want := range map[string]byte{"7 (a) R 1": 'R', "8 (x) y) Z 1": 'Z', "9 (bad": 0, ")": 0} {
		if got := procState([]byte(in)); got != want {
			t.Errorf("procState(%q) = %q, want %q", in, got, want)
		}
	}
}

const boxMountinfo = `1203 1060 0:177 / / ro,relatime master:509 - overlay overlay ro,lowerdir=/x
1204 1203 0:180 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
1205 1203 0:181 / /dev rw,nosuid - tmpfs tmpfs rw,size=65536k,mode=755
1206 1205 0:182 / /dev/pts rw,nosuid,noexec,relatime - devpts devpts rw,gid=5,mode=620,ptmxmode=666
1207 1203 0:183 / /sys ro,nosuid,nodev,noexec,relatime - sysfs sysfs ro
1208 1207 0:28 / /sys/fs/cgroup ro,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw
1210 1205 0:184 / /dev/shm rw,nosuid,nodev,noexec,relatime - tmpfs shm rw,size=65536k
1211 1203 254:1 /docker/volumes/bothy_run/_data/egress/0123 /run/egress ro,relatime - ext4 /dev/vda1 rw
1212 1203 254:1 /docker/volumes/bothy_work/_data/ws/ws_x /workspace rw,relatime - ext4 /dev/vda1 rw
1213 1203 254:1 /docker/volumes/bothy_work/_data/skills /skills ro,relatime - ext4 /dev/vda1 rw
1214 1203 254:1 /docker/containers/abc/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/vda1 rw
1215 1203 254:1 /docker/containers/abc/hostname /etc/hostname rw,relatime - ext4 /dev/vda1 rw
1216 1203 254:1 /docker/containers/abc/hosts /etc/hosts rw,relatime - ext4 /dev/vda1 rw
1217 1203 0:185 / /tmp rw,nosuid,nodev,relatime - tmpfs tmpfs rw,size=262144k
1218 1203 0:186 / /home/sbx rw,nosuid,nodev,relatime - tmpfs tmpfs rw,size=65536k,mode=700,uid=10001,gid=10001
1219 1204 0:181 /null /proc/kcore rw,nosuid - tmpfs tmpfs rw,size=65536k,mode=755
1220 1207 0:187 / /sys/firmware ro,relatime - tmpfs tmpfs ro
1221 1203 254:1 /docker/volumes/bothy_work/_data/mcp/x/in_y /opt/mcp ro,relatime - ext4 /dev/vda1 rw
`

func TestMountChecks(t *testing.T) {
	if bad := badMounts(parseMountinfo([]byte(boxMountinfo))); len(bad) != 0 {
		t.Fatalf("box mounts flagged: %v", bad)
	}
	if !rootMountReadOnly([]byte(boxMountinfo)) {
		t.Fatal("ro root not detected")
	}
	for name, extra := range map[string]string{
		"docker.sock":   "1300 1203 0:26 /docker.sock /var/run/docker.sock rw - tmpfs tmpfs rw\n",
		"socket in dir": "1300 1203 254:1 /run/docker.sock /mnt rw - ext4 /dev/vda1 rw\n",
		"run volume":    "1300 1203 254:1 /docker/volumes/bothy_run/_data /run/bothy rw - ext4 /dev/vda1 rw\n",
		"volume root":   "1300 1203 254:1 /docker/volumes/bothy_work/_data /workspace rw - ext4 /dev/vda1 rw\n",
		"host mount":    "1300 1203 254:1 /home /mnt/host rw - ext4 /dev/vda1 rw\n",
		"space escaped": `1300 1203 254:1 /x /my\040dir rw - ext4 /dev/vda1 rw` + "\n",
	} {
		if bad := badMounts(parseMountinfo([]byte(boxMountinfo + extra))); len(bad) == 0 {
			t.Errorf("%s: not flagged", name)
		}
	}
	if rootMountReadOnly([]byte("1 0 0:1 / / rw,relatime - overlay overlay rw\n")) {
		t.Fatal("rw root reported ro")
	}
	if got := unescapeOctal(`a\040b\134c`); got != `a b\c` {
		t.Fatalf("unescapeOctal = %q", got)
	}
}

func fakeEnv() probeEnv {
	return probeEnv{
		runtime: "runc",
		status: func() ([]byte, error) {
			return []byte("Name:\tx\nCapEff:\t0000000000000000\nNoNewPrivs:\t1\nSeccomp:\t2\n"), nil
		},
		mountinfo: func() ([]byte, error) { return []byte(boxMountinfo), nil },
		ids:       func() (int, int, int, int, []int, error) { return 10001, 10001, 10001, 10001, []int{10001}, nil },
		dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)}
		},
		lookup: func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") },
	}
}

func TestProbes(t *testing.T) {
	e := fakeEnv()
	for _, c := range []proto.Check{probeNet(e), probeDNS(e), probeCaps(e), probeMounts(e), probeUID(e)} {
		if !c.OK {
			t.Errorf("%s failed on a good box: %s", c.Name, c.Detail)
		}
	}

	bad := fakeEnv()
	bad.dial = func(context.Context, string, string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}
	if probeNet(bad).OK {
		t.Error("net_blocked passed although the dial succeeded")
	}
	bad.dial = func(context.Context, string, string) (net.Conn, error) { return nil, context.DeadlineExceeded }
	if probeNet(bad).OK {
		t.Error("net_blocked passed on a timeout")
	}
	bad.lookup = func(context.Context, string) ([]string, error) { return []string{"93.184.215.14"}, nil }
	if probeDNS(bad).OK {
		t.Error("dns_blocked passed although example.com resolved")
	}
	bad.status = func() ([]byte, error) {
		return []byte("CapEff:\t00000000a80425fb\nNoNewPrivs:\t1\nSeccomp:\t2\n"), nil
	}
	if probeCaps(bad).OK {
		t.Error("caps passed with capabilities")
	}
	bad.status = func() ([]byte, error) { return []byte("CapEff:\t0000000000000000\nNoNewPrivs:\t0\nSeccomp:\t0\n"), nil }
	if probeCaps(bad).OK {
		t.Error("caps passed without no_new_privs and seccomp under runc")
	}
	bad.runtime = "runsc"
	if !probeCaps(bad).OK {
		t.Error("runsc only requires CapEff 0")
	}
	bad.ids = func() (int, int, int, int, []int, error) { return 10001, 10001, 10001, 10001, []int{10001, 0}, nil }
	if probeUID(bad).OK {
		t.Error("uid passed with supplementary group 0")
	}
	bad.ids = func() (int, int, int, int, []int, error) { return 0, 0, 0, 0, nil, nil }
	if probeUID(bad).OK {
		t.Error("uid passed as root")
	}
}

func TestProbeRootfs(t *testing.T) {
	e := fakeEnv()
	dir := t.TempDir()
	e.roFile = filepath.Join(dir, "x")
	if probeRootfs(e).OK {
		t.Fatal("rootfs_ro passed on a writable directory")
	}
	if _, err := os.Stat(e.roFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("probe left its file behind")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if c := probeRootfs(e); !c.OK {
		t.Fatalf("EACCES with a ro root mount should pass: %s", c.Detail)
	}
	e.mountinfo = func() ([]byte, error) { return []byte("1 0 0:1 / / rw - overlay overlay rw\n"), nil }
	if probeRootfs(e).OK {
		t.Fatal("EACCES with a rw root mount must fail")
	}
}

func TestProbeRelay(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
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
			fmt.Fprintln(c, "bothy-canary n0nce")
			c.Close()
		}
	}()
	var d net.Dialer
	e := probeEnv{canary: "n0nce", relay: ln.Addr().String(), dial: d.DialContext}
	if c := probeRelay(e); !c.OK {
		t.Fatalf("relay probe failed: %s", c.Detail)
	}
	e.canary = "other"
	if probeRelay(e).OK {
		t.Fatal("relay probe accepted the wrong nonce")
	}
	e.canary = ""
	if probeRelay(e).OK {
		t.Fatal("relay probe passed without a nonce")
	}
}

func TestRelay(t *testing.T) {
	dir, err := os.MkdirTemp("", "sbx")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "proxy.sock")
	ul, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ul.Close()
	go func() {
		for {
			c, err := ul.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c) // echo until the client half-closes
			}()
		}
	}()
	tl, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		serveRelay(tl, sock)
		close(done)
	}()
	c, err := net.Dial("tcp", tl.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("CONNECT pypi.org:443 HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = c.(*net.TCPConn).CloseWrite()
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "CONNECT pypi.org:443 HTTP/1.1\r\n\r\n" {
		t.Fatalf("relayed %q, %v", got, err)
	}
	c.Close()

	// Without the egress socket the client is cut off: fail closed.
	ul.Close()
	os.Remove(sock)
	c, err = net.Dial("tcp", tl.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := c.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("read %d, %v; want EOF", n, err)
	}
	c.Close()
	tl.Close()
	<-done
}
