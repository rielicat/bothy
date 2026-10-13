package bwrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	minMajor, minMinor = 0, 8 // --disable-userns
	drainLimit         = 64 << 20
	stdioTimeout       = 4 * time.Hour
)

// UsernsFix is printed when the user-namespace probe fails, typically
// Ubuntu's AppArmor restriction on unprivileged user namespaces.
const UsernsFix = "allow unprivileged user namespaces for bwrap: on Ubuntu 24.04+ install the bwrap AppArmor profile " +
	"(sudo apt install apparmor-profiles && sudo ln -s /usr/share/apparmor/extra-profiles/bwrap-userns-restrict /etc/apparmor.d/ " +
	"&& sudo systemctl reload apparmor), or run the compose deployment instead"

// Backend runs jails. It is safe for concurrent use.
type Backend struct {
	o       Options
	prog    []byte
	version string

	mu     sync.Mutex
	active map[string]map[*jailProc]struct{}
	// gen is bumped by Kill so running execs report killed; only keys with
	// running jails have an entry.
	gen map[string]uint64
}

type jailProc struct {
	kill func()
}

// Probe finds bwrap >= 0.8 and runs it once with the jail's namespace flags;
// checks lists what passed. An error means bwrap cannot isolate here.
func Probe(ctx context.Context) (o Options, version string, checks []core.SelfTestCheck, err error) {
	add := func(name string, ok bool, detail string) {
		checks = append(checks, core.SelfTestCheck{Name: name, OK: ok, Detail: detail})
	}
	path, err := exec.LookPath("bwrap")
	if err != nil {
		add("bwrap", false, "bwrap not found in PATH")
		return o, "", checks, errors.New("bwrap (bubblewrap >= 0.8) is not installed")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		add("bwrap", false, err.Error())
		return o, "", checks, fmt.Errorf("bwrap --version: %w", err)
	}
	version = string(bytes.TrimSpace(out))
	if !versionOK(version) {
		add("bwrap", false, version+" is older than 0.8")
		return o, version, checks, fmt.Errorf("%s is too old: bubblewrap >= 0.8 is required", version)
	}
	add("bwrap", true, version)
	self, err := os.Executable()
	if err != nil {
		return o, version, checks, err
	}
	o = Options{Bwrap: path, Self: self}
	probe := exec.CommandContext(ctx, path, "--unshare-all", "--unshare-user", "--disable-userns",
		"--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib",
		"--ro-bind-try", "/usr/lib64", "/lib64", "--proc", "/proc", "--dev", "/dev", "--", "/usr/bin/true")
	probe.Env = []string{}
	var stderr bytes.Buffer
	probe.Stderr = &stderr
	if err := probe.Run(); err != nil {
		add("userns", false, fmt.Sprintf("%v: %s", err, bytes.TrimSpace(stderr.Bytes())))
		return o, version, checks, fmt.Errorf("bwrap cannot create a user namespace: %s", bytes.TrimSpace(stderr.Bytes()))
	}
	add("userns", true, "unprivileged user namespaces work")
	return o, version, checks, nil
}

var versionRE = regexp.MustCompile(`(\d+)\.(\d+)`)

func versionOK(v string) bool {
	m := versionRE.FindStringSubmatch(v)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > minMajor || (major == minMajor && minor >= minMinor)
}

// New compiles the seccomp program once.
func New(o Options, version string) (*Backend, error) {
	prog, err := Program()
	if err != nil {
		return nil, err
	}
	return &Backend{o: o, prog: prog, version: version, active: map[string]map[*jailProc]struct{}{}, gen: map[string]uint64{}}, nil
}

func (b *Backend) Version() string { return b.version }

// Kill SIGKILLs every running jail of key; returns how many.
func (b *Backend) Kill(key string) int {
	b.mu.Lock()
	var ps []*jailProc
	for p := range b.active[key] {
		ps = append(ps, p)
	}
	if len(ps) > 0 {
		b.gen[key]++
	}
	b.mu.Unlock()
	for _, p := range ps {
		p.kill()
	}
	return len(ps)
}

// capture keeps max bytes of one stream, forwards everything kept to onOut
// and kills the jail once drainLimit bytes past the cap were dropped.
type capture struct {
	mu        *sync.Mutex
	stream    int
	max       int
	buf       []byte
	dropped   int
	truncated *bool
	onOut     func(int, []byte)
	kill      func()
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	keep := p
	if room := c.max - len(c.buf); room < len(p) {
		*c.truncated = true
		keep = p[:max(room, 0)]
		c.dropped += len(p) - len(keep)
		if c.dropped > drainLimit {
			c.kill()
		}
	}
	c.buf = append(c.buf, keep...)
	if len(keep) > 0 && c.onOut != nil {
		c.onOut(c.stream, keep)
	}
	return len(p), nil
}

var _ io.Writer = (*capture)(nil)
