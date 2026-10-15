package core

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

// Provenance says who authored content. Anything not ProvUser/ProvSystem/ProvTool
// is untrusted and is wrapped in spotlight delimiters when sent to a model.
type Provenance string

const (
	ProvUser      Provenance = "user"      // the human who owns the session's principal (or the space owner)
	ProvUntrusted Provenance = "untrusted" // other senders, group participants, web/MCP output, API client "system"
	ProvTool      Provenance = "tool"      // trusted tool output (own workspace reads, exit codes)
	ProvSystem    Provenance = "system"    // Bothy-authored notices, turn-context, summaries
)

type MsgRole string

const (
	MsgUser      MsgRole = "user"
	MsgAssistant MsgRole = "assistant"
	MsgTool      MsgRole = "tool"
)

type MsgKind string

const (
	KindNormal  MsgKind = "normal"
	KindSummary MsgKind = "summary" // compaction output, first message of an epoch
	KindSteer   MsgKind = "steer"   // user message injected at a step boundary of a running run
	KindNotice  MsgKind = "notice"  // Bothy-authored (stopped, budget, loop); never sent to a model
)

type PartType string

const (
	PartText       PartType = "text"
	PartImage      PartType = "image"
	PartFile       PartType = "file"
	PartToolCall   PartType = "tool_call"
	PartToolResult PartType = "tool_result"
	PartThinking   PartType = "thinking"
	PartContext    PartType = "context" // persisted <turn-context> block of a user message
)

type Part struct {
	Type       PartType    `json:"t"`
	Text       string      `json:"text,omitempty"`
	Blob       *Blob       `json:"blob,omitempty"` // image or file
	ToolCall   *ToolCall   `json:"call,omitempty"`
	ToolResult *ToolResult `json:"result,omitempty"`
	// Sig is provider-opaque data that must round-trip verbatim (Anthropic thinking
	// signature, Gemini thoughtSignature, OpenAI Responses encrypted reasoning).
	// SigKind is the ProviderKind that produced it, or a client's own kind where
	// one provider kind has two wire APIs ("openai_responses"); the provider facade
	// strips it when the next call goes to a client of another kind.
	Sig     string       `json:"sig,omitempty"`
	SigKind ProviderKind `json:"sig_kind,omitempty"`
	Refine  *RefineTrace `json:"refine,omitempty"` // PartRefine only
	// State and Approval are set on tool_call parts by API reads and never
	// stored: where the call stands, and the approval it waited on.
	State    ToolState  `json:"state,omitempty"`
	Approval ApprovalID `json:"approval_id,omitempty"`
}

// ToolState is where a transcript tool call stands. A call is done only once
// its result exists, so a pending action never reads as finished.
type ToolState string

const (
	ToolRunning         ToolState = "running"
	ToolWaitingApproval ToolState = "waiting_approval"
	ToolDone            ToolState = "done"
	ToolFailed          ToolState = "failed"
	ToolDenied          ToolState = "denied"  // an approver said no
	ToolExpired         ToolState = "expired" // nobody answered in time, or the task stopped first
	ToolBlocked         ToolState = "blocked" // refused by policy, never asked
)

// Blob points at a file in the principal's workspace. Data is filled by the
// agent just before a provider call and is never persisted.
type Blob struct {
	MIME string `json:"mime"`
	Name string `json:"name,omitempty"`
	Ref  string `json:"ref"` // workspace-relative path, e.g. ".bothy/in/<msg>/photo.jpg"
	Size int64  `json:"size"`
	Data []byte `json:"-"`
}

type ToolCall struct {
	ID   string          `json:"id"` // provider id, or "call_<n>" when the provider sends none
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"` // a JSON object after repair
}

type ToolResult struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Content   string `json:"content"` // redacted, truncated head+tail
	IsError   bool   `json:"is_error,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	FullRef   string `json:"full_ref,omitempty"`  // workspace path of the full redacted output
	Untrusted bool   `json:"untrusted,omitempty"` // wrapped in <untrusted id=…> when sent to a model
}

type Message struct {
	ID         int64      `json:"id"`
	Session    SessionID  `json:"session_id"`
	Seq        int64      `json:"seq"`
	Run        RunID      `json:"run_id,omitempty"`
	Role       MsgRole    `json:"role"`
	Kind       MsgKind    `json:"kind"`
	Parts      []Part     `json:"parts"`
	Provenance Provenance `json:"provenance"`
	Sender     string     `json:"sender,omitempty"` // "telegram:12345", "web:usr_…", "api:tok_…"
	Tokens     int        `json:"tokens"`           // estimate, calibrated
	CreatedAt  time.Time  `json:"created_at"`
}

// PlainText returns the concatenated text parts (no tool JSON). Stored in
// messages.text for FTS.
func (m *Message) PlainText() string {
	var s string
	for _, p := range m.Parts {
		if p.Type == PartText {
			if s != "" {
				s += "\n"
			}
			s += p.Text
		}
	}
	return s
}

// Redacted returns a copy of m with every text, tool argument and tool result
// masked by r (nil masks nothing); m is unchanged. With keepSigned, thinking
// that carries a signature keeps its text: the provider checks the two
// together when the step is sent back.
func (m *Message) Redacted(r Redactor, keepSigned bool) *Message {
	out := *m
	if r == nil {
		return &out
	}
	out.Parts = make([]Part, len(m.Parts))
	for i, p := range m.Parts {
		if p.Text != "" && (!keepSigned || p.Type != PartThinking || p.Sig == "") {
			p.Text = r.Redact(p.Text)
		}
		if p.ToolResult != nil {
			tr := *p.ToolResult
			tr.Content = r.Redact(tr.Content)
			p.ToolResult = &tr
		}
		if p.ToolCall != nil {
			tc := *p.ToolCall
			tc.Args = r.RedactBytes(tc.Args)
			p.ToolCall = &tc
		}
		out.Parts[i] = p
	}
	return &out
}

// TitleChars is how much of a chat's first message names the chat until its
// generated title arrives.
const TitleChars = 48

// DraftTitle is that name: the text on one line, cut to TitleChars characters.
func DraftTitle(text string) string {
	s := strings.Join(strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}), " ")
	if r := []rune(s); len(r) > TitleChars {
		s = strings.TrimSpace(string(r[:TitleChars]))
	}
	return s
}

type Pin struct {
	ID        int64     `json:"id"`
	Session   SessionID `json:"session_id"`
	Text      string    `json:"text"`
	Source    string    `json:"source"` // "user" | "auto" (standing instruction found by compaction)
	MsgSeq    int64     `json:"msg_seq,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
