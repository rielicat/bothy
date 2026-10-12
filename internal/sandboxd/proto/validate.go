package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"
)

// Self-test check names. sandboxd reports the engine checks; sbx-init selftest
// reports the canary checks (SPEC §2.10, probes 1 to 7) in this order.
const (
	CheckEngine    = "engine"     // sandboxd can reach the engine socket
	CheckEngineAPI = "engine_api" // engine API >= 1.45 (volume subpaths)
	CheckImages    = "images"     // every BOTHY_SBX_IMAGES entry is present
	CheckCanary    = "canary"     // the canary box ran sbx-init selftest
	CheckRunsc     = "runsc"      // present only when runsc is registered but failed its canary

	CheckNetBlocked  = "net_blocked"
	CheckDNSBlocked  = "dns_blocked"
	CheckCaps        = "caps"
	CheckRootfsRO    = "rootfs_ro"
	CheckMounts      = "mounts"
	CheckUID         = "uid"
	CheckEgressRelay = "egress_relay"
)

// IsolationChecks are the canary probes whose failure means the box is not
// isolated (probes 1 to 6); a failing egress relay alone only disables egress.
var IsolationChecks = []string{CheckNetBlocked, CheckDNSBlocked, CheckCaps, CheckRootfsRO, CheckMounts, CheckUID}

// Limits on requests, enforced by both the gateway client and sandboxd.
const (
	MaxArgv      = 256
	MaxArgvBytes = 256 << 10
	MaxEnv       = 64
	MaxEnvBytes  = 64 << 10
	MaxStdin     = 2 << 20
	MaxOutputCap = 16 << 20
	MinMemoryMB  = 64
	MaxMemoryMB  = 2048
	MinCPUs      = 0.1
	MaxCPUs      = 2
	MinPids      = 16
	MaxPids      = 512

	BoxUID = 10001 // uid and gid of every process in a box
)

var (
	keyRE       = regexp.MustCompile(`^box_[0-9a-z]{26}$`)
	workspaceRE = regexp.MustCompile(`^ws_[0-9a-z]{26}$`)
	mcpTreeRE   = regexp.MustCompile(`^mcp/[a-z0-9][a-z0-9-]{0,31}/in_[0-9a-z]{26}$`)
	envKeyRE    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	profileRE   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

// ErrInvalid is wrapped by every validation error.
var ErrInvalid = errors.New("invalid request")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func ValidKey(s string) bool       { return keyRE.MatchString(s) }
func ValidWorkspace(s string) bool { return s == "" || workspaceRE.MatchString(s) }
func ValidMCPTree(s string) bool   { return s == "" || mcpTreeRE.MatchString(s) }
func ValidProfile(s string) bool   { return profileRE.MatchString(s) }

// EgressSubpath is the run-volume subpath holding a box's proxy socket. It is
// derived from the key on both sides and never sent over the wire.
func EgressSubpath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "egress/" + hex.EncodeToString(sum[:])[:32]
}

// ValidateCreate checks ids and the profile; profiles lists the configured
// image keys.
func ValidateCreate(r *CreateReq, profiles map[string]string) error {
	if !ValidKey(r.Key) {
		return invalid("key must match ^box_[0-9a-z]{26}$")
	}
	if !ValidWorkspace(r.Workspace) {
		return invalid("workspace must match ^ws_[0-9a-z]{26}$ or be empty")
	}
	if !ValidMCPTree(r.MCPTree) {
		return invalid("mcp_tree must match ^mcp/<server>/in_<26>$ or be empty")
	}
	if r.MCPTreeRW && r.MCPTree == "" {
		return invalid("mcp_tree_rw needs mcp_tree")
	}
	if _, ok := profiles[r.Profile]; !ok {
		return invalid("unknown profile %q", r.Profile)
	}
	return nil
}

// Clamp returns l with every field clamped to the box limits; zero values take
// the minimum.
func (l Limits) Clamp() Limits {
	return Limits{
		MemoryMB: clamp(l.MemoryMB, MinMemoryMB, MaxMemoryMB),
		CPUs:     clampF(l.CPUs, MinCPUs, MaxCPUs),
		Pids:     clamp(l.Pids, MinPids, MaxPids),
	}
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

func clampF(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	return max(lo, min(v, hi))
}

// ValidateArgv enforces the argv limits of ExecReq and StdioReq.
func ValidateArgv(argv []string) error {
	if len(argv) == 0 {
		return invalid("argv is empty")
	}
	if len(argv) > MaxArgv {
		return invalid("argv has %d entries (max %d)", len(argv), MaxArgv)
	}
	total := 0
	for _, a := range argv {
		if strings.IndexByte(a, 0) >= 0 {
			return invalid("argv contains a NUL byte")
		}
		total += len(a)
	}
	if total > MaxArgvBytes {
		return invalid("argv is %d bytes (max %d)", total, MaxArgvBytes)
	}
	if argv[0] == "" {
		return invalid("argv[0] is empty")
	}
	return nil
}

// CleanCwd validates a cwd relative to /workspace and returns its canonical
// form ("" for the workspace root). It refuses rather than repairs: absolute
// paths, ".." segments and uncleaned paths are errors.
func CleanCwd(cwd string) (string, error) {
	if cwd == "" || cwd == "." {
		return "", nil
	}
	if strings.IndexByte(cwd, 0) >= 0 || strings.ContainsRune(cwd, '\\') {
		return "", invalid("cwd contains a forbidden character")
	}
	if path.IsAbs(cwd) {
		return "", invalid("cwd must be relative to /workspace")
	}
	for _, seg := range strings.Split(cwd, "/") {
		if seg == ".." {
			return "", invalid("cwd must not contain ..")
		}
	}
	trimmed := strings.TrimSuffix(cwd, "/")
	if path.Clean(trimmed) != trimmed {
		return "", invalid("cwd must be a cleaned path")
	}
	return trimmed, nil
}

// ValidateEnv enforces the env key grammar and the forbidden keys of
// core.ExecRequest: they would break the proxy setup or inject code into
// every interpreter the command starts.
func ValidateEnv(env map[string]string) error {
	if len(env) > MaxEnv {
		return invalid("env has %d entries (max %d)", len(env), MaxEnv)
	}
	total := 0
	for k, v := range env {
		if !envKeyRE.MatchString(k) {
			return invalid("env key %q must match ^[A-Z_][A-Z0-9_]{0,63}$", k)
		}
		if ForbiddenEnvKey(k) {
			return invalid("env key %s is not allowed", k)
		}
		if strings.IndexByte(v, 0) >= 0 {
			return invalid("env value of %s contains a NUL byte", k)
		}
		total += len(k) + len(v)
	}
	if total > MaxEnvBytes {
		return invalid("env is %d bytes (max %d)", total, MaxEnvBytes)
	}
	return nil
}

func ForbiddenEnvKey(k string) bool {
	switch k {
	case "PATH", "HOME", "BASH_ENV", "NODE_OPTIONS", "PYTHONPATH":
		return true
	}
	return strings.HasPrefix(k, "LD_") || strings.HasSuffix(k, "_PROXY")
}
