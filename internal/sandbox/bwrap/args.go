// Package bwrap is the native Linux sandbox backend (SPEC §2.12): every exec
// is a bubblewrap jail with a clean environment, all namespaces unshared,
// nested user namespaces disabled and a seccomp filter. It is the only
// package allowed to import os/exec.
package bwrap

import (
	"maps"
	"path/filepath"
	"slices"
	"time"

	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

const (
	selfInJail = "/opt/bothy/bothy"
	seccompFD  = "3" // first of exec.Cmd.ExtraFiles
	killAfter  = 5 * time.Second
)

// Options locate the host side of a jail.
type Options struct {
	Bwrap   string // bwrap binary
	Self    string // the bothy binary, bound read-only at /opt/bothy/bothy
	WorkDir string // the work volume: ws/<id>, skills/, mcp/...
	RunDir  string // holds egress/<h>/proxy.sock
}

// Jail is the per-key part of a jail: what a container box would mount.
type Jail struct {
	Key       string
	Workspace string
	MCPTree   string
	MCPTreeRW bool
}

// baseEnv mirrors the container box environment (SPEC §2.6).
var baseEnv = [][2]string{
	{"PATH", "/usr/local/bin:/usr/bin:/bin"}, {"HOME", "/home/sbx"}, {"LANG", "C.UTF-8"},
	{"HTTP_PROXY", "http://127.0.0.1:3128"}, {"HTTPS_PROXY", "http://127.0.0.1:3128"},
	{"http_proxy", "http://127.0.0.1:3128"}, {"https_proxy", "http://127.0.0.1:3128"},
	{"NO_PROXY", ""}, {"NODE_USE_ENV_PROXY", "1"}, {"PIP_DISABLE_PIP_VERSION_CHECK", "1"}, {"GIT_TERMINAL_PROMPT", "0"},
}

// Args builds the bwrap command line for one exec. env must already be
// validated (proto.ValidateEnv) and cwd cleaned (proto.CleanCwd).
func Args(o Options, j Jail, cwd string, env map[string]string, timeout time.Duration, argv []string) []string {
	a := []string{"--clearenv"}
	for _, kv := range baseEnv {
		a = append(a, "--setenv", kv[0], kv[1])
	}
	for _, k := range slices.Sorted(maps.Keys(env)) {
		a = append(a, "--setenv", k, env[k])
	}
	a = append(a,
		"--unshare-all", "--unshare-user", "--disable-userns", "--uid", "10001", "--gid", "10001",
		"--die-with-parent", "--new-session", "--cap-drop", "ALL",
		"--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib",
		"--ro-bind-try", "/usr/lib64", "/lib64",
		"--ro-bind", "/etc/ssl", "/etc/ssl", "--ro-bind-try", "/etc/alternatives", "/etc/alternatives",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--tmpfs", "/home/sbx",
	)
	if j.Workspace != "" {
		a = append(a, "--bind", filepath.Join(o.WorkDir, "ws", j.Workspace), "/workspace")
	} else {
		a = append(a, "--dir", "/workspace")
	}
	dir := "/workspace"
	if cwd != "" {
		dir += "/" + cwd
	}
	a = append(a, "--chdir", dir, "--ro-bind-try", filepath.Join(o.WorkDir, "skills"), "/skills")
	if j.MCPTree != "" {
		bind := "--ro-bind"
		if j.MCPTreeRW {
			bind = "--bind"
		}
		a = append(a, bind, filepath.Join(o.WorkDir, filepath.FromSlash(j.MCPTree)), "/opt/mcp")
	}
	a = append(a,
		"--ro-bind", filepath.Join(o.RunDir, filepath.FromSlash(proto.EgressSubpath(j.Key))), "/run/egress",
		"--ro-bind", o.Self, selfInJail,
		"--seccomp", seccompFD,
		"--", selfInJail, "sbx-init", "--relay", "/run/egress/proxy.sock",
		"run", "--timeout", timeout.String(), "--kill-after", killAfter.String(), "--",
	)
	return append(a, argv...)
}
