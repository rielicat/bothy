package core

import (
	"context"
	"io"
	"net/http"
	"time"
)

type SelfTestCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type SandboxStatus struct {
	Backend   string          `json:"backend"` // sandboxd | bwrap | none
	Available bool            `json:"available"`
	Reason    string          `json:"reason,omitempty"` // why not available, plain language
	Fix       string          `json:"fix,omitempty"`    // copyable command or doc anchor
	Runtime   string          `json:"runtime"`          // runsc | runc | bwrap
	Engine    string          `json:"engine,omitempty"` // "docker 27.4.1 (api 1.47)"
	Rootless  bool            `json:"rootless"`
	SelfTest  []SelfTestCheck `json:"self_test"`
	CheckedAt time.Time       `json:"checked_at"`
}

type BoxProfile string

const (
	ProfileBase BoxProfile = "base" // python3, bash, git, curl, uv
	ProfileNode BoxProfile = "node" // base + Node 24 LTS
)

type BoxLimits struct {
	MemoryMB int     `json:"memory_mb"`
	CPUs     float64 `json:"cpus"`
	Pids     int     `json:"pids"`
}

type BoxSpec struct {
	Key       string // box_<26>; DerivedID("box", "session:"+id) or ("mcp:"+server+":"+principal)
	Profile   BoxProfile
	Workspace WorkspaceID // mounted rw at /workspace; "" = none (MCP servers without workspace access)
	MCPTree   string      // "mcp/<server>/<install_id>" mounted at /opt/mcp; "" = none
	MCPTreeRW bool        // true only for the installer box
	Limits    BoxLimits
	Egress    BoxEgress
}

type BoxEgress struct {
	Session   SessionID
	Principal PrincipalID
	Extra     []string // host:port always allowed for this box (MCP server egress list, installer registries)
	NoGrants  bool     // ignore session/principal grants (MCP and installer boxes)
}

type ExecRequest struct {
	Argv      []string
	Stdin     []byte
	Cwd       string            // relative to /workspace, cleaned
	Env       map[string]string // keys ^[A-Z_][A-Z0-9_]{0,63}$; no LD_*, PATH, HOME, *_PROXY, BASH_ENV, NODE_OPTIONS, PYTHONPATH
	Timeout   time.Duration
	MaxOutput int // bytes kept per stream; the rest is drained and dropped
}

type ExecResult struct {
	Exit      int           `json:"exit"`
	Stdout    []byte        `json:"-"`
	Stderr    []byte        `json:"-"`
	Truncated bool          `json:"truncated"`
	TimedOut  bool          `json:"timed_out"`
	Killed    bool          `json:"killed"` // by /stop
	Duration  time.Duration `json:"duration"`
}

// Sandbox is the gateway-side client for the active backend (sandboxd, bwrap or
// none). With backend none every method except Status, Usage and Watch returns
// ErrNoSandbox.
type Sandbox interface {
	Status(ctx context.Context) SandboxStatus
	// Ensure creates the directories the box mounts (work volume: ws/<id> and,
	// when set, the MCPTree dir, both 2770 group 10001, plus skills/ if missing;
	// run volume: egress/<h> 0750 group 10001), opens the box's egress socket via
	// Egress.OpenBox, then creates the box (idempotent per Key).
	Ensure(ctx context.Context, spec BoxSpec) error
	// Usage reports a workspace's size against sandbox.workspace_quota_mb from
	// the periodic quota scan (works with every backend, including none; fs_write
	// refuses writes over quota).
	Usage(ctx context.Context, ws WorkspaceID) (usedBytes, quotaBytes int64, err error)
	Exec(ctx context.Context, key string, req ExecRequest, onOut func(stream int, b []byte)) (*ExecResult, error)
	Stdio(ctx context.Context, key string, argv []string, env map[string]string) (io.ReadWriteCloser, error)
	// Kill SIGKILLs every process in the box except its init (sbx-init killall);
	// falls back to removing the box. The workspace persists. p99 < 1s.
	Kill(ctx context.Context, key string) error
	Destroy(ctx context.Context, key string) error // also calls Egress.CloseBox(key)
	Watch(fn func(SandboxStatus))                  // status changes (self-test failures, sandboxd restarts)
}

// SystemDest describes a gateway-originated destination (provider, channel API,
// search API, remote MCP server). Only these exact host:port pairs are dialable
// by the returned client.
type SystemDest struct {
	Name  string   // "provider:anthropic", "channel:telegram", "mcp:docs", "search:brave"
	Hosts []string // exact "host:port"
	// AllowPrivate admits RFC1918, ULA, CGNAT, loopback and link-local addresses
	// for these exact hosts (Ollama on the host or LAN, the searxng service).
	// Cloud metadata addresses stay blocked.
	AllowPrivate bool
	// PrivateOnly refuses every public address: a plain-http destination
	// that sends a key may reach only the admin's own network, whatever its
	// name resolves to. It admits nothing AllowPrivate doesn't.
	PrivateOnly bool
}

// PrivateOnlyRefused ends egress's refusal of a PrivateOnly destination whose
// name resolves to a public address, so a caller can name that fix.
const PrivateOnlyRefused = "plain http with a key may reach only a private address"

// EgressBinding carries what the proxy needs to log a connection and, for an
// unknown destination, to build the hold-for-approval PolicyInput.
type EgressBinding struct {
	Run         RunID
	ToolCallID  string
	User        UserID // the principal's account; "" for team-owned principals
	Role        Role
	Sender      string // RunRequest.Sender, so a team request names who asked
	Trigger     Trigger
	Schedule    ScheduleID // schedule-scoped (pre-authorized) domain grants apply at the proxy
	Interactive bool       // hold unknown CONNECTs for approval (egress.hold_for_approval) instead of failing
	Chat        *ChatRef   // for the hold-for-approval card
	Context     string
	Provenance  Provenance // of the message that started the run: floor F6 applies to held connections
}

type Egress interface {
	// OpenBox creates /run/bothy/egress/<h>/proxy.sock for the box (the socket is
	// the box identity) and returns the volume subpath "egress/<h>".
	OpenBox(ctx context.Context, key string, e BoxEgress) (subpath string, err error)
	CloseBox(key string)
	// Bind attaches the current run/tool call to a box before each exec, for
	// logging, taint and hold-for-approval.
	Bind(key string, b EgressBinding)
	// Unbind ends toolCallID's binding once its exec returns: its allow_once
	// admissions go, and later connections (background processes) are no
	// longer held for approval. The run stays bound for logging.
	Unbind(key, toolCallID string)
	// TakeActivity reports whether the box moved bytes since the last call.
	TakeActivity(key string) bool
	// PublicClient is the session-scoped client for gateway-side tools
	// (web_fetch): any public address, SSRF-guarded, pinned DNS, per-hop checks.
	PublicClient(s SessionID, p PrincipalID) *http.Client
	// SystemClient is for providers, channels, search APIs and HTTP MCP servers.
	SystemClient(d SystemDest) *http.Client
}
