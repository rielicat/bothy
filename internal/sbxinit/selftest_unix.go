//go:build unix

package sbxinit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

const probeTimeout = 3 * time.Second

// probeEnv holds what the canary probes read, so tests can point them at
// fixtures instead of the real box.
type probeEnv struct {
	runtime   string
	canary    string
	relay     string
	status    func() ([]byte, error)
	mountinfo func() ([]byte, error)
	roFile    string
	ids       func() (uid, gid, euid, egid int, groups []int, err error)
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	lookup    func(ctx context.Context, host string) ([]string, error)
}

func realProbeEnv(runtime, canary string) probeEnv {
	var d net.Dialer
	r := &net.Resolver{PreferGo: true}
	return probeEnv{
		runtime:   runtime,
		canary:    canary,
		relay:     relayAddr,
		status:    func() ([]byte, error) { return os.ReadFile("/proc/self/status") },
		mountinfo: func() ([]byte, error) { return os.ReadFile("/proc/self/mountinfo") },
		roFile:    "/usr/bin/x",
		ids: func() (int, int, int, int, []int, error) {
			g, err := os.Getgroups()
			return os.Getuid(), os.Getgid(), os.Geteuid(), os.Getegid(), g, err
		},
		dial:   d.DialContext,
		lookup: r.LookupHost,
	}
}

func selftestMain(args []string, stdout, stderr io.Writer) int {
	runtime, canary := "runc", ""
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			fmt.Fprintf(stderr, "sbx-init selftest: %s needs a value\n", args[i])
			return exitInternal
		}
		switch args[i] {
		case "--runtime":
			runtime = args[i+1]
		case "--canary":
			canary = args[i+1]
		default:
			fmt.Fprintf(stderr, "sbx-init selftest: unknown argument %q\n", args[i])
			return exitInternal
		}
		i++
	}
	if err := json.NewEncoder(stdout).Encode(runProbes(realProbeEnv(runtime, canary))); err != nil {
		fmt.Fprintln(stderr, "sbx-init selftest:", err)
		return exitInternal
	}
	return 0
}

// runProbes returns the SPEC §2.10 canary checks in table order.
func runProbes(e probeEnv) []proto.Check {
	return []proto.Check{
		probeNet(e), probeDNS(e), probeCaps(e), probeRootfs(e), probeMounts(e), probeUID(e), probeRelay(e),
	}
}

func check(name string, ok bool, format string, args ...any) proto.Check {
	return proto.Check{Name: name, OK: ok, Detail: fmt.Sprintf(format, args...)}
}

func probeNet(e probeEnv) proto.Check {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	c, err := e.dial(ctx, "tcp4", "1.1.1.1:443")
	if err == nil {
		c.Close()
		return check(proto.CheckNetBlocked, false, "connected to 1.1.1.1:443")
	}
	// A timeout means packets left the box and were dropped somewhere: that is
	// not --network none.
	for _, want := range []error{syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.EACCES, syscall.EPERM, syscall.EAFNOSUPPORT} {
		if errors.Is(err, want) {
			return check(proto.CheckNetBlocked, true, "%v", err)
		}
	}
	return check(proto.CheckNetBlocked, false, "unexpected error: %v", err)
}

func probeDNS(e probeEnv) proto.Check {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	addrs, err := e.lookup(ctx, "example.com")
	if err == nil && len(addrs) > 0 {
		return check(proto.CheckDNSBlocked, false, "example.com resolved to %v", addrs)
	}
	return check(proto.CheckDNSBlocked, true, "%v", err)
}

func probeCaps(e probeEnv) proto.Check {
	b, err := e.status()
	if err != nil {
		return check(proto.CheckCaps, false, "%v", err)
	}
	s := string(b)
	capEff, _ := statusField(s, "CapEff")
	nnp, _ := statusField(s, "NoNewPrivs")
	seccomp, _ := statusField(s, "Seccomp")
	detail := fmt.Sprintf("CapEff=%s NoNewPrivs=%s Seccomp=%s", capEff, nnp, seccomp)
	caps, err := strconv.ParseUint(capEff, 16, 64)
	if err != nil || caps != 0 {
		return check(proto.CheckCaps, false, "%s", detail)
	}
	// gVisor's Sentry is the syscall boundary; it does not report Docker's
	// seccomp profile, so only CapEff is required under runsc.
	if e.runtime == "runsc" {
		return check(proto.CheckCaps, true, "%s", detail)
	}
	return check(proto.CheckCaps, nnp == "1" && seccomp == "2", "%s", detail)
}

func probeRootfs(e probeEnv) proto.Check {
	f, err := os.OpenFile(e.roFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		f.Close()
		_ = os.Remove(e.roFile)
		return check(proto.CheckRootfsRO, false, "wrote %s", e.roFile)
	}
	if errors.Is(err, syscall.EROFS) {
		return check(proto.CheckRootfsRO, true, "%v", err)
	}
	// Permission checks can come first; then the root mount must be ro.
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		if mi, merr := e.mountinfo(); merr == nil && rootMountReadOnly(mi) {
			return check(proto.CheckRootfsRO, true, "%v; / is mounted ro", err)
		}
	}
	return check(proto.CheckRootfsRO, false, "unexpected error: %v", err)
}

func probeMounts(e probeEnv) proto.Check {
	b, err := e.mountinfo()
	if err != nil {
		return check(proto.CheckMounts, false, "%v", err)
	}
	if bad := badMounts(parseMountinfo(b)); len(bad) > 0 {
		return check(proto.CheckMounts, false, "unexpected mounts: %s", strings.Join(bad, ", "))
	}
	return check(proto.CheckMounts, true, "only expected mounts")
}

func probeUID(e probeEnv) proto.Check {
	uid, gid, euid, egid, groups, err := e.ids()
	if err != nil {
		return check(proto.CheckUID, false, "%v", err)
	}
	detail := fmt.Sprintf("uid=%d gid=%d euid=%d egid=%d groups=%v", uid, gid, euid, egid, groups)
	ok := uid == proto.BoxUID && gid == proto.BoxUID && euid == proto.BoxUID && egid == proto.BoxUID
	for _, g := range groups {
		if g != proto.BoxUID {
			ok = false
		}
	}
	return check(proto.CheckUID, ok, "%s", detail)
}

func probeRelay(e probeEnv) proto.Check {
	if e.canary == "" {
		return check(proto.CheckEgressRelay, false, "no canary nonce given")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	// PID 1 may still be binding the relay when the canary exec starts.
	var c net.Conn
	var err error
	for {
		if c, err = e.dial(ctx, "tcp", e.relay); err == nil || ctx.Err() != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		return check(proto.CheckEgressRelay, false, "%v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(probeTimeout))
	line, err := bufio.NewReader(io.LimitReader(c, 256)).ReadString('\n')
	if err != nil {
		return check(proto.CheckEgressRelay, false, "reading the canary listener: %v", err)
	}
	if strings.TrimSpace(line) != "bothy-canary "+e.canary {
		return check(proto.CheckEgressRelay, false, "unexpected reply from %s", e.relay)
	}
	return check(proto.CheckEgressRelay, true, "%s reaches the canary listener", e.relay)
}

type mount struct {
	root, point, opts, source string
}

// parseMountinfo reads the fields of proc(5) mountinfo that the probes need.
func parseMountinfo(b []byte) []mount {
	var out []mount
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		sep := slices.Index(f, "-")
		if sep < 6 || sep+2 >= len(f) {
			continue
		}
		out = append(out, mount{
			root:   unescapeOctal(f[3]),
			point:  unescapeOctal(f[4]),
			opts:   f[5],
			source: unescapeOctal(f[sep+2]),
		})
	}
	return out
}

func rootMountReadOnly(mi []byte) bool {
	ro := false
	for _, m := range parseMountinfo(mi) {
		if m.point == "/" { // the last mount on / wins
			ro = slices.Contains(strings.Split(m.opts, ","), "ro")
		}
	}
	return ro
}

// allowedMounts are the mount points a box may have: Docker's own (/proc,
// /dev, /sys, /etc/hosts, ...) and the compiled spec's (SPEC §2.6).
var allowedMounts = []string{"/proc", "/dev", "/sys", "/tmp", "/home/sbx", "/workspace", "/run/egress", "/skills",
	"/opt/mcp", "/etc/hosts", "/etc/hostname", "/etc/resolv.conf"}

func badMounts(ms []mount) []string {
	var bad []string
	for _, m := range ms {
		switch {
		case strings.Contains(m.point, "docker.sock") || strings.Contains(m.root, "docker.sock") || strings.Contains(m.source, "docker.sock"):
			bad = append(bad, m.point+" (runtime socket)")
		case under(m.point, "/run/bothy") || under(m.point, "/var/run") || under(m.point, "/run/docker"):
			bad = append(bad, m.point)
		case strings.HasSuffix(m.root, "/_data"):
			// A whole named volume (its _data root) instead of a subpath.
			bad = append(bad, m.point+" (volume root)")
		case m.point == "/":
		default:
			ok := false
			for _, a := range allowedMounts {
				if under(m.point, a) {
					ok = true
					break
				}
			}
			if !ok {
				bad = append(bad, m.point)
			}
		}
	}
	return bad
}

func under(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// unescapeOctal decodes the kernel's \040-style escapes in mountinfo.
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
