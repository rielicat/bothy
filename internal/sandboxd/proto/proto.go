// Package proto is the fixed-schema gateway <-> sandboxd wire format (HTTP/1.1
// + JSON over /run/bothy/sandboxd.sock). Both sides decode with
// DisallowUnknownFields. Owner: M5 (client and server are in the same module).
package proto

const Version = "1" // path prefix /v1

// Validation (sandboxd rejects with 400 otherwise):
//   Key       ^box_[0-9a-z]{26}$
//   Workspace ^ws_[0-9a-z]{26}$ or ""
//   MCPTree   ^mcp/[a-z0-9][a-z0-9-]{0,31}/in_[0-9a-z]{26}$ or ""
//   Profile   one of the image keys in BOTHY_SBX_IMAGES
//   Limits    clamped to [64,2048] MB, [0.1,2] CPUs, [16,512] pids
//
// Subpaths are derived by sandboxd, never supplied: workspace = "ws/<Workspace>"
// on the work volume; egress = "egress/" + hex(sha256(Key))[:32] on the run
// volume. The gateway pre-creates both directories before calling Create.

// Mount modes reported in Health.Mounts.
const (
	MountsSubpath = "subpath" // volume subpaths (engine API >= 1.45, Docker 26+)
	MountsBind    = "bind"    // bind mounts of the volumes' host paths, verified after start (API 1.44, Docker 25)
)

type Health struct {
	Version  string   `json:"version"`
	API      string   `json:"api"`              // engine API version negotiated, must be >= 1.44
	Engine   string   `json:"engine"`           // "docker 27.4.1"
	Runtime  string   `json:"runtime"`          // runsc | runc
	Rootless bool     `json:"rootless"`         // from /info SecurityOptions
	Subpath  bool     `json:"subpath"`          // volume subpath supported
	Mounts   string   `json:"mounts,omitempty"` // MountsSubpath | MountsBind, once the engine checks passed
	OK       bool     `json:"ok"`               // false => gateway treats exec as unavailable
	Reason   string   `json:"reason,omitempty"`
	Fix      string   `json:"fix,omitempty"`
	SelfTest []Check  `json:"self_test"`
	Boxes    int      `json:"boxes"`
	MaxBoxes int      `json:"max_boxes"`
	Profiles []string `json:"profiles"` // the image keys of BOTHY_SBX_IMAGES, sorted
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

type Limits struct {
	MemoryMB int     `json:"memory_mb"`
	CPUs     float64 `json:"cpus"`
	Pids     int     `json:"pids"`
}

type CreateReq struct {
	Key       string `json:"key"`
	Profile   string `json:"profile"`
	Workspace string `json:"workspace"`   // mounted rw at /workspace from work volume subpath ws/<id>
	MCPTree   string `json:"mcp_tree"`    // work volume subpath, mounted at /opt/mcp
	MCPTreeRW bool   `json:"mcp_tree_rw"` // installer box only
	Limits    Limits `json:"limits"`
}

type CreateResp struct {
	ID      string `json:"id"`
	Created bool   `json:"created"` // false = already running (idempotent per key)
	Runtime string `json:"runtime"`
}

type ExecReq struct {
	Argv      []string          `json:"argv"` // <= 256 entries, <= 256 KiB total
	Cwd       string            `json:"cwd"`  // relative, cleaned, under /workspace
	Env       map[string]string `json:"env"`
	StdinB64  string            `json:"stdin_b64"`
	TimeoutMS int               `json:"timeout_ms"` // 1..max_exec (default cap 30 min)
	MaxOutput int               `json:"max_output"` // bytes kept per stream
}

// Exec response: Content-Type application/x-ndjson, one Frame per line:
//
//	{"s":1,"d":"<b64>"} stdout   {"s":2,"d":"<b64>"} stderr
//	last line: {"exit":0,"ms":41,"truncated":false,"timed_out":false,"killed":false}
type Frame struct {
	Stream    int    `json:"s,omitempty"`
	Data      string `json:"d,omitempty"`
	Exit      *int   `json:"exit,omitempty"`
	Ms        int64  `json:"ms,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	Killed    bool   `json:"killed,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Stdio: POST /v1/boxes/{key}/stdio with "Connection: Upgrade", "Upgrade:
// bothy-stdio/1" and a StdioReq body; 101 then a raw full-duplex byte stream
// (stdin -> process, process stdout -> client; stderr goes to a 64 KiB ring
// readable at GET /v1/boxes/{key}/stderr). One stdio stream per box.
type StdioReq struct {
	Argv []string          `json:"argv"`
	Cwd  string            `json:"cwd"`
	Env  map[string]string `json:"env"`
}

type KillResp struct {
	Killed    int  `json:"killed"`    // processes reported by sbx-init killall
	Recreated bool `json:"recreated"` // fallback path: container killed and removed
}

type BoxInfo struct {
	Key      string `json:"key"`
	ID       string `json:"id"`
	Profile  string `json:"profile"`
	Created  int64  `json:"created_ms"`
	LastUsed int64  `json:"last_used_ms"`
	Runtime  string `json:"runtime"`
}

type Error struct {
	Code    string `json:"code"` // bad_request | not_found | busy | engine | unavailable
	Message string `json:"message"`
}
