package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/skills/treehash"
)

const (
	maxLockBytes  = 16 << 20
	maxInstallErr = 1024
	installOutput = 64 << 10
	boxGroup      = 10001
	npmLockFile   = "package-lock.json"
	pypiLockFile  = "lock.txt"
	pypiInputFile = "requirements.in"
)

// Install installs a stdio server's pinned package into a fresh tree
// mcp/<server>/<install_id> through an installer box that may reach only the
// package registry. It validates synchronously and runs the install in the
// background (the API answers 202); Status shows "installing" until it ends.
func (m *Manager) Install(ctx context.Context, name string, by core.UserID) error {
	s := m.server(name)
	switch {
	case s == nil:
		return fmt.Errorf("mcp server %q: %w", name, core.ErrNotFound)
	case s.spec == nil:
		return fmt.Errorf("mcp server %s: %w: %w", name, core.ErrInvalid, s.specErr)
	case s.spec.transport != "stdio":
		return fmt.Errorf("mcp server %s: %w: only stdio servers are installed", name, core.ErrInvalid)
	case !m.d.Sandbox.Status(ctx).Available:
		return fmt.Errorf("mcp server %s: %w", name, core.ErrNoSandbox)
	}
	s.mu.Lock()
	if s.installing {
		s.mu.Unlock()
		return fmt.Errorf("mcp server %s: install already running: %w", name, core.ErrConflict)
	}
	s.installing = true
	// An admin's own try starts the backoff afresh.
	s.retries, s.retryAt = 0, time.Time{}
	s.mu.Unlock()
	s.setState(m.clock.Now(), stInstalling, "")
	if !m.spawn(func(ctx context.Context) {
		s.op.Lock()
		defer s.op.Unlock()
		m.installLocked(ctx, s, by)
	}) {
		s.mu.Lock()
		s.installing = false
		s.mu.Unlock()
		s.setState(m.clock.Now(), stError, "manager closed")
		return fmt.Errorf("mcp: manager closed: %w", core.ErrConflict)
	}
	return nil
}

// retryInstall installs again a server whose last install failed or was cut
// short, when discovery finds it so: at start-up, when the server is turned on
// again, when the sandbox comes back and when its backoff ends. Callers hold
// s.op. It reports false when an admin's install is about to run instead.
func (m *Manager) retryInstall(ctx context.Context, s *server) bool {
	s.mu.Lock()
	busy := s.installing
	s.installing = true
	s.mu.Unlock()
	if busy {
		return false
	}
	m.installLocked(ctx, s, "")
	return true
}

// installLocked runs an install, by an admin or (by == "") automatically.
// Callers hold s.op and have set s.installing. A failed install is tried
// again with the discovery backoff (1 minute doubling to 30), unless what was
// fetched doesn't match its pins: fetching again would bring the same.
func (m *Manager) installLocked(ctx context.Context, s *server, by core.UserID) {
	s.setState(m.clock.Now(), stInstalling, "")
	m.closeInstances(s)
	in, err := m.runInstall(ctx, s)
	// Cleared before the final state is set, so whoever sees the install end
	// may start the next one; op still serialises them.
	s.mu.Lock()
	s.installing = false
	s.mu.Unlock()
	actor := "system"
	if by != "" {
		actor = "user:" + string(by)
	}
	detail := map[string]any{"ecosystem": s.spec.pkg.Ecosystem, "package": s.spec.pkg.Name, "version": s.spec.pkg.Version}
	if in != nil {
		detail["install_id"], detail["tree_hash"], detail["lock_hash"] = in.InstallID, in.TreeHash, in.LockHash
	}
	if err != nil {
		detail["error"] = err.Error()
		m.record(ctx, actor, "mcp.install", s.name, "error", detail)
		m.log.Warn("mcp install failed", "server", s.name, "err", err)
		var mm mismatch
		again := !errors.As(err, &mm) && ctx.Err() == nil
		if again {
			s.failed(m.clock.Now())
		}
		s.mu.Lock()
		if s.installAgain = again; !again {
			s.retries, s.retryAt = 0, time.Time{}
		}
		s.mu.Unlock()
		s.setState(m.clock.Now(), stError, "install failed: "+err.Error())
		s.setCode("install_failed")
		return
	}
	s.mu.Lock()
	s.retries, s.retryAt, s.installAgain = 0, time.Time{}, false
	s.mu.Unlock()
	m.record(ctx, actor, "mcp.install", s.name, "ok", detail)
	m.removeOldTrees(s, in.InstallID)
	m.setDrifted(s, false)
	m.discoverLocked(ctx, s)
}

// mismatch marks an install whose package or lock doesn't match what it is
// pinned to.
type mismatch struct{ error }

func (e mismatch) Unwrap() error { return e.error }

func (m *Manager) runInstall(ctx context.Context, s *server) (*core.MCPInstall, error) {
	spec := s.spec
	reuse := m.recordedLock(ctx, s)
	in := &core.MCPInstall{Server: s.name, InstallID: core.NewID(core.PrefixInstall), Version: spec.pkg.Version,
		Status: "installing", CreatedAt: m.clock.Now()}
	if err := m.d.Store.MCP().PutInstall(ctx, in); err != nil {
		return nil, err
	}
	err := m.installTree(ctx, s, in, reuse)
	if err != nil {
		in.Status, in.Error = "failed", truncate(err.Error(), maxInstallErr)
		in.Lock, in.LockHash, in.TreeHash = nil, "", ""
	} else {
		in.Status = "ready"
	}
	if perr := m.d.Store.MCP().PutInstall(ctx, in); perr != nil && err == nil {
		err = perr
	}
	return in, err
}

// recordedLock returns the lock of the current install when it was recorded
// for the configured version (trust on first install: later installs of the
// same version reproduce exactly that dependency tree).
func (m *Manager) recordedLock(ctx context.Context, s *server) []byte {
	prev, err := m.d.Store.MCP().Install(ctx, s.name)
	if err != nil || prev.Version != s.spec.pkg.Version || len(prev.Lock) == 0 || sha256hex(prev.Lock) != prev.LockHash {
		return nil
	}
	return prev.Lock
}

func (m *Manager) installTree(ctx context.Context, s *server, in *core.MCPInstall, recorded []byte) (err error) {
	spec := s.spec
	rel := spec.installTree(in.InstallID)
	dir := filepath.Join(m.d.WorkDir, filepath.FromSlash(rel))
	// The installer box (uid 10001) writes the tree through the shared group,
	// so it is 2770 group 10001 (SPEC §2.11), wider than gosec's default. The
	// chown fails harmlessly where the gateway is not in that group (tests,
	// dev hosts).
	if err := os.MkdirAll(dir, 0o770); err != nil { //nolint:gosec
		return err
	}
	defer func() {
		if err != nil {
			if rerr := os.RemoveAll(dir); rerr != nil {
				m.log.Warn("remove failed mcp install tree", "server", s.name, "install", in.InstallID, "err", rerr)
			}
		}
	}()
	_ = os.Chown(dir, -1, boxGroup)
	if err := os.Chmod(dir, 0o770|os.ModeSetgid); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	box := installBox(s.name)
	if err := m.freshBox(ctx, box); err != nil {
		return err
	}
	if err := m.d.Sandbox.Ensure(ctx, core.BoxSpec{Key: box, Profile: spec.profile, MCPTree: rel, MCPTreeRW: true, Limits: m.limits(),
		Egress: core.BoxEgress{Extra: spec.registryHosts(), NoGrants: true}}); err != nil {
		return fmt.Errorf("installer box: %w", err)
	}
	var lock []byte
	switch spec.pkg.Ecosystem {
	case "npm":
		err = m.installNPM(ctx, box, root, spec, recorded)
	default:
		lock, err = m.installPyPI(ctx, box, root, spec, recorded)
	}
	// Nothing in the tree is trusted before the installer box is gone, so no
	// process left running in it can change files after they are verified.
	if rerr := m.removeBox(box); err == nil {
		err = rerr
	}
	if err != nil {
		return err
	}
	if spec.pkg.Ecosystem == "npm" {
		if lock, err = readCapped(root, npmLockFile); err != nil {
			return err
		}
		if err := checkNPMLock(lock, spec.pkg.Name, spec.pkg.Version, spec.pkg.Integrity); err != nil {
			return mismatch{err}
		}
		// npm ci never writes the lock, so any difference means the tree was
		// not installed from the lock recorded on first install.
		if recorded != nil && !bytes.Equal(lock, recorded) {
			return mismatch{fmt.Errorf("%s differs from the lock recorded on first install", npmLockFile)}
		}
	}
	sum, err := treehash.SumRoot(root)
	if err != nil {
		return fmt.Errorf("hash install tree: %w", err)
	}
	in.Lock, in.LockHash, in.TreeHash = lock, sha256hex(lock), sum
	return nil
}

// installNPM runs npm in the installer box: npm ci from the recorded lock
// when there is one, else npm install of the exact version. The caller
// verifies the resulting lock once the box is gone.
func (m *Manager) installNPM(ctx context.Context, box string, root *os.Root, spec *serverSpec, lock []byte) error {
	name, version := spec.pkg.Name, spec.pkg.Version
	manifest := map[string]any{"name": "bothy-mcp-" + spec.name, "private": true}
	argv := []string{"npm", "install", "--ignore-scripts", "--no-audit", "--no-fund", "--save-exact", "--prefix", "/opt/mcp", name + "@" + version}
	if lock != nil {
		if err := checkNPMLock(lock, name, version, spec.pkg.Integrity); err != nil {
			return mismatch{fmt.Errorf("recorded lock: %w", err)}
		}
		manifest["dependencies"] = map[string]string{name: version}
		if err := root.WriteFile(npmLockFile, lock, 0o660); err != nil {
			return err
		}
		argv = []string{"npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--prefix", "/opt/mcp"}
	}
	pj, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := root.WriteFile("package.json", append(pj, '\n'), 0o660); err != nil {
		return err
	}
	return m.installExec(ctx, box, argv)
}

// checkNPMLock verifies that a package-lock.json pins the configured package
// at the configured version with the configured integrity.
func checkNPMLock(lock []byte, name, version, integrity string) error {
	var l struct {
		Packages map[string]struct {
			Version   string `json:"version"`
			Integrity string `json:"integrity"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(lock, &l); err != nil {
		return fmt.Errorf("%s: %w", npmLockFile, err)
	}
	p, ok := l.Packages["node_modules/"+name]
	switch {
	case !ok:
		return fmt.Errorf("%s does not list %s", npmLockFile, name)
	case p.Version != version:
		return fmt.Errorf("%s pins %s@%s, want %s", npmLockFile, name, p.Version, version)
	case p.Integrity != integrity:
		return fmt.Errorf("integrity of %s@%s is %q, config pins %q", name, version, p.Integrity, integrity)
	}
	return nil
}

// installPyPI compiles a hashed lock for the exact version (unless one was
// recorded), checks the configured hash in it and installs from it with
// --require-hashes, returning the lock that was verified.
func (m *Manager) installPyPI(ctx context.Context, box string, root *os.Root, spec *serverSpec, lock []byte) ([]byte, error) {
	if lock == nil {
		req := spec.pkg.Name + "==" + spec.pkg.Version + "\n"
		if err := root.WriteFile(pypiInputFile, []byte(req), 0o660); err != nil {
			return nil, err
		}
		if err := m.installExec(ctx, box, []string{"uv", "pip", "compile", "--python", "python3", "--generate-hashes", "--no-header",
			"--quiet", "--output-file", "/opt/mcp/" + pypiLockFile, "/opt/mcp/" + pypiInputFile}); err != nil {
			return nil, err
		}
		var err error
		if lock, err = readCapped(root, pypiLockFile); err != nil {
			return nil, err
		}
	} else if err := root.WriteFile(pypiLockFile, lock, 0o660); err != nil {
		return nil, err
	}
	if err := checkPyPILock(lock, spec.pkg.Name, spec.pkg.Version, spec.pkg.Integrity); err != nil {
		return nil, mismatch{err}
	}
	if err := m.installExec(ctx, box, []string{"uv", "pip", "install", "--python", "python3", "--no-deps", "--require-hashes",
		"-r", "/opt/mcp/" + pypiLockFile, "--target", "/opt/mcp"}); err != nil {
		return nil, err
	}
	return lock, nil
}

var pypiSepRe = regexp.MustCompile(`[-_.]+`)

func pypiNormalize(name string) string { return pypiSepRe.ReplaceAllString(strings.ToLower(name), "-") }

// checkPyPILock verifies that a hashed requirements file pins the configured
// package at the configured version and lists the configured hash for it.
func checkPyPILock(lock []byte, name, version, integrity string) error {
	text := strings.ReplaceAll(string(lock), "\\\n", " ")
	want := pypiNormalize(name)
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		req, _, _ := strings.Cut(fields[0], ";")
		pkg, ver, ok := strings.Cut(req, "==")
		if !ok {
			continue
		}
		if base, _, extras := strings.Cut(pkg, "["); extras {
			pkg = base
		}
		if pypiNormalize(pkg) != want {
			continue
		}
		if ver != version {
			return fmt.Errorf("%s pins %s==%s, want %s", pypiLockFile, name, ver, version)
		}
		if !slices.Contains(fields[1:], "--hash="+integrity) {
			return fmt.Errorf("%s does not list hash %s for %s==%s", pypiLockFile, integrity, name, version)
		}
		return nil
	}
	return fmt.Errorf("%s does not list %s", pypiLockFile, name)
}

func (m *Manager) installExec(ctx context.Context, box string, argv []string) error {
	res, err := m.d.Sandbox.Exec(ctx, box, core.ExecRequest{Argv: argv, Timeout: installTimeout, MaxOutput: installOutput}, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv[:2], " "), err)
	}
	if res.TimedOut {
		return fmt.Errorf("%s timed out after %s", strings.Join(argv[:2], " "), installTimeout)
	}
	if res.Exit != 0 {
		tail := bytes.TrimSpace(res.Stderr)
		if len(tail) > 2048 {
			tail = tail[len(tail)-2048:]
		}
		return fmt.Errorf("%s exited %d: %s", strings.Join(argv[:2], " "), res.Exit, tail)
	}
	return nil
}

func readCapped(root *os.Root, name string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxLockBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxLockBytes {
		return nil, fmt.Errorf("%s is larger than %d MB", name, maxLockBytes>>20)
	}
	return b, nil
}

// removeOldTrees deletes the server's install trees other than keep.
func (m *Manager) removeOldTrees(s *server, keep string) {
	base := filepath.Join(m.d.WorkDir, "mcp", s.name)
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != keep && core.ValidID(core.PrefixInstall, e.Name()) {
			if err := os.RemoveAll(filepath.Join(base, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				m.log.Warn("remove old mcp install tree", "server", s.name, "install", e.Name(), "err", err)
			}
		}
	}
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
