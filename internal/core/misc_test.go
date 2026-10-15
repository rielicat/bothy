package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestMessagePlainText(t *testing.T) {
	tests := []struct {
		name  string
		parts []Part
		want  string
	}{
		{"no parts", nil, ""},
		{"one text", []Part{{Type: PartText, Text: "hi"}}, "hi"},
		{"joined by newline", []Part{{Type: PartText, Text: "a"}, {Type: PartText, Text: "b"}}, "a\nb"},
		{"non-text skipped", []Part{
			{Type: PartContext, Text: "<turn-context>"},
			{Type: PartThinking, Text: "hmm"},
			{Type: PartText, Text: "answer"},
			{Type: PartToolCall, ToolCall: &ToolCall{ID: "c1", Name: "shell", Args: json.RawMessage(`{}`)}},
			{Type: PartToolResult, ToolResult: &ToolResult{CallID: "c1", Content: "out"}},
			{Type: PartText, Text: "done"},
		}, "answer\ndone"},
		{"only tool parts", []Part{{Type: PartToolCall, ToolCall: &ToolCall{Name: "x"}}}, ""},
	}
	for _, tt := range tests {
		m := &Message{Parts: tt.parts}
		if got := m.PlainText(); got != tt.want {
			t.Errorf("%s: PlainText = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// maskRedactor masks one value, like the vault's redactor with one saved key.
type maskRedactor string

func (r maskRedactor) Redact(s string) string { return strings.ReplaceAll(s, string(r), "[redacted]") }
func (r maskRedactor) RedactBytes(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte(r), []byte("[redacted]"))
}
func (maskRedactor) Add(string, []byte) {}
func (maskRedactor) Remove(string)      {}

func TestMessageRedacted(t *testing.T) {
	const key = "KEY-0123456789"
	m := &Message{ID: 7, Seq: 3, Parts: []Part{
		{Type: PartContext, Text: "ctx " + key},
		{Type: PartText, Text: "text " + key, Sig: "gemini"},
		{Type: PartThinking, Text: "signed " + key, Sig: "c2ln"},
		{Type: PartThinking, Text: "unsigned " + key},
		{Type: PartToolCall, ToolCall: &ToolCall{ID: "c1", Name: "echo", Args: json.RawMessage(`{"v":"` + key + `"}`)}},
		{Type: PartToolResult, ToolResult: &ToolResult{CallID: "c1", Content: "out " + key}},
	}}
	before, _ := json.Marshal(m)
	for _, keep := range []bool{false, true} {
		out := m.Redacted(maskRedactor(key), keep)
		b, _ := json.Marshal(out)
		got := string(b)
		if signed := strings.Contains(got, "signed "+key); signed != keep || strings.Count(got, key) != map[bool]int{false: 0, true: 1}[keep] {
			t.Errorf("keepSigned=%v: %s", keep, got)
		}
		if out.ID != 7 || out.Seq != 3 || out.Parts[1].Sig != "gemini" {
			t.Errorf("keepSigned=%v: lost fields: %+v", keep, out)
		}
	}
	if after, _ := json.Marshal(m); !bytes.Equal(before, after) {
		t.Fatalf("the original changed: %s", after)
	}
	if out := m.Redacted(nil, false); out == m || out.Parts[0].Text != m.Parts[0].Text {
		t.Fatalf("nil redactor: %+v", out)
	}
}

func TestChatRefKey(t *testing.T) {
	tests := []struct {
		c    ChatRef
		want string
	}{
		{ChatRef{Channel: "telegram", ChatID: "12345", Kind: ChatDM}, "telegram:12345:"},
		{ChatRef{Channel: "slack", ChatID: "C01", ThreadID: "1712345678.000100", Kind: ChatGroup}, "slack:C01:1712345678.000100"},
		{ChatRef{Channel: "web", ChatID: "ses_x"}, "web:ses_x:"},
		{ChatRef{}, "::"},
	}
	for _, tt := range tests {
		if got := tt.c.Key(); got != tt.want {
			t.Errorf("Key() = %q, want %q", got, tt.want)
		}
	}
	a := ChatRef{Channel: "discord", ChatID: "1", ThreadID: "2"}
	b := ChatRef{Channel: "discord", ChatID: "1", ThreadID: "3"}
	if a.Key() == b.Key() {
		t.Error("threads of one chat share a key")
	}
}

func TestSecret(t *testing.T) {
	const plain = "sk-ant-supersecretvalue"
	s := NewSecret([]byte(plain))
	if s.Reveal() != plain || string(s.Bytes()) != plain || s.IsZero() {
		t.Fatal("accessors lost the value")
	}
	if !(Secret{}).IsZero() || !NewSecret(nil).IsZero() {
		t.Fatal("empty secret not zero")
	}
	type holder struct {
		Key Secret
		Ptr *Secret
	}
	h := holder{Key: s, Ptr: &s}
	outputs := map[string]string{
		"%v":     fmt.Sprintf("%v", s),
		"%s":     fmt.Sprintf("%s", s),
		"%+v":    fmt.Sprintf("%+v", s),
		"%#v":    fmt.Sprintf("%#v", s),
		"%q":     fmt.Sprintf("%q", s),
		"%x":     fmt.Sprintf("%x", s),
		"struct": fmt.Sprintf("%+v", h),
		"ptr":    fmt.Sprint(&s),
	}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	outputs["json"] = string(b)
	for _, newHandler := range []func(*bytes.Buffer) slog.Handler{
		func(w *bytes.Buffer) slog.Handler { return slog.NewTextHandler(w, nil) },
		func(w *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(w, nil) },
	} {
		var buf bytes.Buffer
		slog.New(newHandler(&buf)).Info("msg", "secret", s, "holder", h)
		outputs["slog"] += buf.String()
	}
	for name, out := range outputs {
		if strings.Contains(out, plain) || strings.Contains(out, fmt.Sprintf("%x", plain)) {
			t.Errorf("%s leaked the secret: %s", name, out)
		}
	}
	if outputs["json"] != `{"Key":"[redacted]","Ptr":"[redacted]"}` {
		t.Errorf("json = %s", outputs["json"])
	}
	if UserScope("usr_a") != "user:usr_a" || GlobalScope != "global" {
		t.Error("scope strings changed")
	}
}

func TestActorHasScope(t *testing.T) {
	tests := []struct {
		name  string
		a     Actor
		scope string
		want  bool
	}{
		{"cookie member chat", Actor{Via: "cookie", Role: RoleMember}, "chat", true},
		{"cookie member read", Actor{Via: "cookie", Role: RoleMember}, "read", true},
		{"cookie member llm", Actor{Via: "cookie", Role: RoleMember}, "llm", true},
		{"cookie member admin", Actor{Via: "cookie", Role: RoleMember}, "admin", false},
		{"cookie admin admin", Actor{Via: "cookie", Role: RoleAdmin}, "admin", true},
		{"cookie owner admin", Actor{Via: "cookie", Role: RoleOwner}, "admin", true},
		{"cookie ignores token scopes", Actor{Via: "cookie", Role: RoleMember, Scopes: []string{"admin"}}, "admin", false},
		{"token with scope", Actor{Via: "token", Role: RoleOwner, Scopes: []string{"chat", "read"}}, "read", true},
		{"token without scope", Actor{Via: "token", Role: RoleOwner, Scopes: []string{"chat"}}, "read", false},
		{"token owner without admin scope", Actor{Via: "token", Role: RoleOwner, Scopes: []string{"chat"}}, "admin", false},
		{"token admin scope", Actor{Via: "token", Role: RoleAdmin, Scopes: []string{"admin"}}, "admin", true},
		{"token no scopes", Actor{Via: "token", Role: RoleOwner}, "chat", false},
		{"unknown via", Actor{Role: RoleOwner}, "chat", false},
	}
	for _, tt := range tests {
		if got := tt.a.HasScope(tt.scope); got != tt.want {
			t.Errorf("%s: HasScope(%q) = %v, want %v", tt.name, tt.scope, got, tt.want)
		}
	}
}

func TestRunStatusTerminal(t *testing.T) {
	live := map[RunStatus]bool{RunQueued: true, RunRunning: true, RunWaitingApproval: true, RunParked: true}
	for _, s := range []RunStatus{RunQueued, RunRunning, RunWaitingApproval, RunParked, RunDone, RunStopped,
		RunFailed, RunBudgetExceeded, RunLoopDetected, RunInterrupted, RunSkipped} {
		if s.Terminal() == live[s] {
			t.Errorf("RunStatus(%q).Terminal() = %v", s, s.Terminal())
		}
	}
}

func TestEventJSON(t *testing.T) {
	b, err := json.Marshal(Event{Seq: 7, Type: EvRunDelta, Principal: "usr_a", Data: DeltaData{Text: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "run.delta" || got["principal_id"] != "usr_a" || got["seq"] != float64(7) {
		t.Fatalf("event json = %s", b)
	}
	if d, ok := got["data"].(map[string]any); !ok || d["text"] != "hi" {
		t.Fatalf("payload json = %s", b)
	}
}
