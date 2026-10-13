package bwrap

import (
	"encoding/binary"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"

	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

const key = "box_0123456789abcdefghjkmnpqrs"

var opts = Options{Bwrap: "/usr/bin/bwrap", Self: "/usr/local/bin/bothy", WorkDir: "/srv/work", RunDir: "/run/bothy"}

// pairs returns the arguments following each occurrence of flag.
func pairs(args []string, flag string, n int) [][]string {
	var out [][]string
	for i := 0; i+n < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if args[i] == flag {
			out = append(out, args[i+1:i+1+n])
		}
	}
	return out
}

func TestArgs(t *testing.T) {
	j := Jail{Key: key, Workspace: "ws_0123456789abcdefghjkmnpqrs"}
	a := Args(opts, j, "src", map[string]string{"FOO": "1", "A": "2"}, 90*time.Second, []string{"bash", "-lc", "echo hi"})
	if a[0] != "--clearenv" {
		t.Fatal("--clearenv must come first")
	}
	for _, f := range []string{"--unshare-all", "--unshare-user", "--disable-userns", "--die-with-parent", "--new-session"} {
		if !slices.Contains(a, f) {
			t.Errorf("missing %s", f)
		}
	}
	for flag, want := range map[string][]string{"--uid": {"10001"}, "--gid": {"10001"}, "--cap-drop": {"ALL"}, "--seccomp": {"3"},
		"--chdir": {"/workspace/src"}} {
		if got := pairs(a, flag, 1); len(got) != 1 || !slices.Equal(got[0], want) {
			t.Errorf("%s = %v, want %v", flag, got, want)
		}
	}
	env := map[string]string{}
	for _, kv := range pairs(a, "--setenv", 2) {
		env[kv[0]] = kv[1]
	}
	if env["PATH"] != "/usr/local/bin:/usr/bin:/bin" || env["HOME"] != "/home/sbx" || env["HTTPS_PROXY"] != "http://127.0.0.1:3128" ||
		env["FOO"] != "1" || env["A"] != "2" {
		t.Errorf("env = %v", env)
	}
	for k := range env {
		if strings.HasPrefix(k, "BOTHY_") {
			t.Errorf("gateway variable %s leaked", k)
		}
	}
	binds := pairs(a, "--bind", 2)
	if len(binds) != 1 || !slices.Equal(binds[0], []string{"/srv/work/ws/ws_0123456789abcdefghjkmnpqrs", "/workspace"}) {
		t.Errorf("rw binds = %v (only the workspace may be writable)", binds)
	}
	ro := pairs(a, "--ro-bind", 2)
	for _, want := range [][]string{{"/usr", "/usr"}, {"/etc/ssl", "/etc/ssl"}, {"/run/bothy/" + proto.EgressSubpath(key), "/run/egress"},
		{"/usr/local/bin/bothy", "/opt/bothy/bothy"}} {
		if !slices.ContainsFunc(ro, func(p []string) bool { return slices.Equal(p, want) }) {
			t.Errorf("missing --ro-bind %v", want)
		}
	}
	i := slices.Index(a, "--")
	want := []string{"/opt/bothy/bothy", "sbx-init", "--relay", "/run/egress/proxy.sock", "run", "--timeout", "1m30s",
		"--kill-after", "5s", "--", "bash", "-lc", "echo hi"}
	if i < 0 || !slices.Equal(a[i+1:], want) {
		t.Errorf("command = %q", a[i+1:])
	}
}

func TestArgsVariants(t *testing.T) {
	noWS := Args(opts, Jail{Key: key}, "", nil, time.Minute, []string{"x"})
	if len(pairs(noWS, "--bind", 2)) != 0 || !slices.Contains(noWS, "--dir") || pairs(noWS, "--chdir", 1)[0][0] != "/workspace" {
		t.Error("jail without a workspace must not bind one")
	}
	ro := Args(opts, Jail{Key: key, MCPTree: "mcp/docs/in_0123456789abcdefghjkmnpqrs"}, "", nil, time.Minute, []string{"x"})
	if !slices.ContainsFunc(pairs(ro, "--ro-bind", 2), func(p []string) bool { return p[1] == "/opt/mcp" }) {
		t.Error("MCP tree must be read-only by default")
	}
	rw := Args(opts, Jail{Key: key, MCPTree: "mcp/docs/in_0123456789abcdefghjkmnpqrs", MCPTreeRW: true}, "", nil, time.Minute, []string{"x"})
	if !slices.ContainsFunc(pairs(rw, "--bind", 2), func(p []string) bool { return p[1] == "/opt/mcp" }) {
		t.Error("installer jail must get a writable MCP tree")
	}
}

func TestProgram(t *testing.T) {
	prog, err := Program()
	if err != nil {
		t.Fatal(err)
	}
	if len(prog) == 0 || len(prog)%8 != 0 {
		t.Fatalf("program is %d bytes", len(prog))
	}
	raw := make([]bpf.RawInstruction, len(prog)/8)
	for i := range raw {
		rec := prog[i*8 : i*8+8]
		raw[i] = bpf.RawInstruction{Op: binary.NativeEndian.Uint16(rec[0:2]), Jt: rec[2], Jf: rec[3], K: binary.NativeEndian.Uint32(rec[4:8])}
	}
	insts, ok := bpf.Disassemble(raw)
	if !ok {
		t.Fatal("program does not disassemble")
	}
	// The first instruction loads seccomp_data.arch: the filter is arch-checked.
	if ld, ok := insts[0].(bpf.LoadAbsolute); !ok || ld.Off != 4 {
		t.Fatalf("first instruction %v does not load the arch", insts[0])
	}
	eperm := uint32(0x00050000 | 1)
	allow := uint32(0x7fff0000)
	var rets []uint32
	for _, in := range insts {
		if r, ok := in.(bpf.RetConstant); ok {
			rets = append(rets, r.Val)
		}
	}
	if !slices.Contains(rets, eperm) || !slices.Contains(rets, allow) {
		t.Fatalf("returns %x lack EPERM or ALLOW", rets)
	}
	vm, err := bpf.NewVM(insts)
	if err != nil {
		t.Fatal(err)
	}
	info, err := arch.GetInfo("")
	if err != nil {
		t.Fatal(err)
	}
	// x/net/bpf's VM loads words big-endian (packet order) while the kernel
	// loads seccomp_data natively, so each 32-bit field is stored big-endian
	// at the offset the filter reads it from on this little-endian host.
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		t.Skip("emulation assumes a little-endian host")
	}
	seccompData := func(nr, archID uint32, args ...uint64) []byte {
		data := make([]byte, 64)
		binary.BigEndian.PutUint32(data[0:4], nr)
		binary.BigEndian.PutUint32(data[4:8], archID)
		for i, a := range args {
			binary.BigEndian.PutUint32(data[16+8*i:], uint32(a))
			binary.BigEndian.PutUint32(data[16+8*i+4:], uint32(a>>32))
		}
		return data
	}
	run := func(syscall string, args ...uint64) uint32 {
		nr, ok := info.SyscallNames[syscall]
		if !ok {
			t.Fatalf("no syscall %s", syscall)
		}
		ret, err := vm.Run(seccompData(uint32(nr), uint32(info.ID), args...))
		if err != nil {
			t.Fatal(err)
		}
		return uint32(ret)
	}
	for _, sc := range []string{"ptrace", "bpf", "mount", "unshare", "setns", "keyctl", "io_uring_setup", "process_vm_readv", "chroot"} {
		if got := run(sc); got != eperm {
			t.Errorf("%s: %x, want EPERM", sc, got)
		}
	}
	for _, sc := range []string{"read", "write", "openat", "clone", "execve"} {
		if got := run(sc); got != allow {
			t.Errorf("%s: %x, want ALLOW", sc, got)
		}
	}
	for family, want := range map[uint64]uint32{1: allow, 2: allow, 10: allow, 16: eperm, 17: eperm, 1<<32 | 2: eperm} {
		if got := run("socket", family, 1, 0); got != want {
			t.Errorf("socket(%d): %x, want %x", family, got, want)
		}
	}
	for persona, want := range map[uint64]uint32{0: allow, 0xffffffff: allow, 0x0040000: eperm, 8: eperm} {
		if got := run("personality", persona); got != want {
			t.Errorf("personality(%#x): %x, want %x", persona, got, want)
		}
	}
	// Another architecture's syscalls never reach the allow-list logic.
	nr := uint32(info.SyscallNames["read"])
	if ret, _ := vm.Run(seccompData(nr, 0x40000003)); uint32(ret) != 0x80000000 { // AUDIT_ARCH_I386
		t.Errorf("foreign arch: %x, want KILL_PROCESS", uint32(ret))
	}
}

func TestVersionOK(t *testing.T) {
	for v, want := range map[string]bool{"bubblewrap 0.8.0": true, "bubblewrap 0.11.0": true, "bubblewrap 1.0": true,
		"bubblewrap 0.7.9": false, "bubblewrap": false, "": false} {
		if got := versionOK(v); got != want {
			t.Errorf("versionOK(%q) = %v", v, got)
		}
	}
}
