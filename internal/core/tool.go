package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// CanonicalJSON re-encodes JSON (a json.RawMessage, a []byte, or any
// JSON-marshalable value) with object keys sorted, no insignificant whitespace,
// numbers kept verbatim and HTML escaping off. Action.Payload, Fingerprint,
// ToolSpec.Hash, MCP pin hashes and the audit chain all hash these bytes, so
// every module agrees on them.
func CanonicalJSON(v any) ([]byte, error) {
	var raw []byte
	switch x := v.(type) {
	case json.RawMessage:
		raw = x
	case []byte:
		raw = x
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// ActionFingerprint is hex sha256(tool || 0x00 || canonical payload).
func ActionFingerprint(tool string, canonicalPayload []byte) string {
	h := sha256.New()
	h.Write([]byte(tool))
	h.Write([]byte{0})
	h.Write(canonicalPayload)
	return hex.EncodeToString(h.Sum(nil))
}

// SideEffect classes. The zero value is invalid so a missing manifest field is
// rejected at Register instead of silently meaning "read".
type SideEffect uint8

const (
	SERead SideEffect = iota + 1
	SELocalWrite
	SEExternalSend
	SEIrreversible
	SEFinancial
	SEAdmin // self-modification: config, policy, prompts, skills, MCP, schedules, auth
)

var seNames = [...]string{"", "read", "local_write", "external_send", "irreversible", "financial", "admin"}

func (s SideEffect) String() string {
	if int(s) < len(seNames) {
		return seNames[s]
	}
	return fmt.Sprintf("side_effect(%d)", s)
}

func (s SideEffect) Valid() bool { return s >= SERead && s <= SEAdmin }

func (s SideEffect) MarshalText() ([]byte, error) {
	if !s.Valid() {
		return nil, fmt.Errorf("invalid side effect %d", s)
	}
	return []byte(s.String()), nil
}

func (s *SideEffect) UnmarshalText(b []byte) error {
	v, err := ParseSideEffect(string(b))
	*s = v
	return err
}

func ParseSideEffect(name string) (SideEffect, error) {
	for i := 1; i < len(seNames); i++ {
		if seNames[i] == name {
			return SideEffect(i), nil
		}
	}
	return 0, fmt.Errorf("unknown side effect %q", name)
}

// Taint legs of the lethal trifecta, persisted per session as a bitmask.
type Taint uint8

const (
	TaintPrivate   Taint = 1 << iota // private data entered context (memory snapshot, private reads)
	TaintUntrusted                   // untrusted content entered context
	TaintSend                        // an exfiltration-capable capability is enabled (informational leg)
)

// Exposed is private+untrusted. In this state every external_send or
// irreversible action is forced to Ask (the trifecta floor).
func (t Taint) Exposed() bool { return t&TaintPrivate != 0 && t&TaintUntrusted != 0 }

func (t Taint) Has(x Taint) bool { return t&x == x }

type Manifest struct {
	Title       string        `json:"title"` // card title, e.g. "Run shell command"
	SideEffect  SideEffect    `json:"side_effect"`
	Exec        bool          `json:"exec"`     // needs a sandbox; hidden when none is available
	FSPaths     []string      `json:"fs_paths"` // workspace-relative globs it may touch
	Domains     []string      `json:"domains"`  // static egress domains; dynamic ones go in Action
	Secrets     []string      `json:"secrets"`  // vault names used gateway-side only
	Messaging   bool          `json:"messaging"`
	Taint       Taint         `json:"taint"`              // legs added to the session when it runs
	Triggers    []Trigger     `json:"triggers,omitempty"` // empty = all; schedule tools = {message}
	MinRole     Role          `json:"min_role,omitempty"` // empty = any
	SpaceOK     bool          `json:"space_ok"`           // offered to restricted space principals
	Core        bool          `json:"core"`               // schema always in the tools block; else via tool_search
	Timeout     time.Duration `json:"timeout"`
	MaxOutputKB int           `json:"max_output_kb"`
}

type ToolSpec struct {
	Def      ToolDef  `json:"def"`
	Source   string   `json:"source"` // "builtin" | "mcp:<server>" | "skill:<name>"
	Manifest Manifest `json:"manifest"`
	Hash     string   `json:"hash"` // sha256 of canonical Def JSON
}

type ActionKind string

const (
	ActExec     ActionKind = "exec"
	ActFSRead   ActionKind = "fs.read"
	ActFSWrite  ActionKind = "fs.write"
	ActFSDelete ActionKind = "fs.delete"
	ActHTTP     ActionKind = "http" // gateway-side fetch
	ActNet      ActionKind = "net"  // egress grant request (net_allow, proxy hold-for-approval)
	ActSend     ActionKind = "send" // message to a chat
	ActMCP      ActionKind = "mcp"  // MCP tool call
	ActMemory   ActionKind = "memory"
	ActSchedule ActionKind = "schedule"
	ActSkill    ActionKind = "skill"  // skill_load / skill_read
	ActSearch   ActionKind = "search" // web_search, session_search
	ActGeneric  ActionKind = "generic"
	// ActRelease dispatches a deploy workflow (release_deploy). Policy gives
	// it its own floor: never auto, never via rules or grants, only in
	// interactive runs, approved by the environment's approvers.
	ActRelease ActionKind = "release"
)

// Action is the structured, system-normalized form of a tool call. Policy,
// approval cards, audit and loop detection consume only this, and Exec receives
// exactly the Action that was decided (and, for parked runs, persisted).
type Action struct {
	Tool        string          `json:"tool"`
	Kind        ActionKind      `json:"kind"`
	SideEffect  SideEffect      `json:"side_effect"` // may escalate above Manifest.SideEffect, never lower
	Argv        []string        `json:"argv,omitempty"`
	Cwd         string          `json:"cwd,omitempty"`
	Method      string          `json:"method,omitempty"`
	URL         string          `json:"url,omitempty"`
	Host        string          `json:"host,omitempty"` // punycode, lowercase
	Port        int             `json:"port,omitempty"`
	Paths       []string        `json:"paths,omitempty"`
	Diff        string          `json:"diff,omitempty"` // unified diff for fs.write, capped 64KB
	Recipient   string          `json:"recipient,omitempty"`
	Domains     []string        `json:"domains,omitempty"`
	MCPServer   string          `json:"mcp_server,omitempty"`
	MCPTool     string          `json:"mcp_tool,omitempty"`
	Payload     json.RawMessage `json:"payload"`     // normalized args (CanonicalJSON), executed verbatim
	Fingerprint string          `json:"fingerprint"` // ActionFingerprint(Tool, Payload)
}

// Exec actions: Payload is {"argv":[…],"cwd":"…"} (shell: ["bash","-lc",cmd];
// python adds "code" and argv ["python3",".bothy/tmp/<fingerprint[:16]>.py"],
// Exec writes the code there first), so JSON pointers such as "/argv/2" in
// rules and grants address the command line and cards show the exact code.

// ToolEnv carries per-call context. Every capability a tool may use is here or
// injected into the tool's constructor; tools never reach for globals.
type ToolEnv struct {
	User        UserID
	Role        Role
	Principal   Principal
	Session     SessionID
	Run         RunID
	CallID      string // tcl_<26>
	Trigger     Trigger
	Schedule    ScheduleID
	Workspace   string // absolute dir; tools open it with os.OpenRoot
	BoxKey      string // box_<26> for this session
	Sandbox     Sandbox
	HTTP        *http.Client // egress-guarded public client scoped to this session (web_fetch)
	Chat        *ChatRef     // originating chat; the message tool can only reply here
	Taint       Taint
	Interactive bool
	Selection   Selection       // the run's tool selection (tool_search passes it to Registry.Search)
	Emit        func(EventData) // tool.output progress; non-blocking, rate-limited by the runner
}

type Tool interface {
	Spec() ToolSpec
	// Plan validates and normalizes args into an Action. It may read (workspace
	// files via os.Root, the DB) but must not write, send, exec or dial, and is
	// deterministic for the same input and state (conformance-tested).
	Plan(ctx context.Context, env *ToolEnv, args json.RawMessage) (*Action, error)
	Exec(ctx context.Context, env *ToolEnv, a *Action) (*ToolResult, error)
}

// Gated is optionally implemented by tools whose availability depends on
// runtime state the registry cannot see (message: env.Chat != nil; skill_load:
// at least one enabled skill; MCP tools: server ready). ForRun, Available and
// Search skip a tool whose Enabled returns false.
type Gated interface {
	Enabled(env *ToolEnv) bool
}

// ToolProvider is implemented by modules that contribute tools. cmd/bothy
// registers them at boot.
type ToolProvider interface{ Tools() []Tool }

type Selection struct {
	SandboxOK  bool
	SmallModel bool // compact profile: <= 8 core tools, short descriptions
	Degraded   bool // degraded profile: <= 5 core tools, no skills index
	Paused     bool // kill switch: read-only tools only
}

type ToolRegistry interface {
	Register(t Tool) error // name collision, invalid manifest or schema => error
	UnregisterSource(source string)
	// ReplaceSource swaps every tool registered under source for tools in one
	// step, so a run never sees the source half re-registered. Tools that fail
	// validation, name another source or collide are skipped and reported in
	// the (joined) error; the others are registered.
	ReplaceSource(source string, tools []Tool) error
	// Get returns the tool behind JSON Schema validation of its arguments
	// (before Plan; violations wrap ErrInvalid) and normalisation of the
	// planned Action (canonical payload, fingerprint, side effect never below
	// the manifest). Callers need no validator of their own (§5.7 b).
	Get(name string) (Tool, bool)
	// ForRun returns the core tools for the tools block plus tool_search/use_tool
	// when lazy tools exist, filtered by trigger, role, principal kind, sandbox and
	// Selection, sorted by name. Bytes must be identical for identical inputs.
	ForRun(env *ToolEnv, sel Selection) []ToolSpec
	// Available reports whether a (possibly lazy) tool is callable in this run.
	Available(name string, env *ToolEnv, sel Selection) bool
	Search(query string, env *ToolEnv, sel Selection, limit int) []ToolSpec
	All() []ToolSpec
}
