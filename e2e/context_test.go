//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// chTurnContext extracts the <turn-context> block of a user message.
func chTurnContext(user string) string {
	start := strings.Index(user, "<turn-context>")
	end := strings.Index(user, "</turn-context>")
	if start < 0 || end < start {
		return ""
	}
	return user[start : end+len("</turn-context>")]
}

func chAgentRequests(t *testing.T, e *chEnv) []chReq {
	t.Helper()
	var out []chReq
	for _, r := range chRequests(t, e.llm) {
		if r.agentCall() {
			out = append(out, r)
		}
	}
	return out
}

// TestMemoryTurnContext is E10: a memory write by the agent, and an edit of
// USER.md in the web UI, reach the model in the next turn's <turn-context>,
// while the tools block and system prompt stay byte-identical and every
// request extends the previous one (prompt-cache stability).
func TestMemoryTurnContext(t *testing.T) {
	e := chStart(t, chOpts{rules: []mockllm.Rule{
		chTitleRule,
		{MatchToolResult: `(?s)^`, Text: []string{"Noted."}},
		{MatchUser: `(?i)remember that my cat`, ToolCalls: []core.ToolCall{{Name: "memory",
			Args: json.RawMessage(`{"file":"MEMORY","op":"add","new":"The user's cat is called Miso."}`)}}},
		{MatchUser: `(?i)what is my cat called`, Text: []string{"Your cat is called Miso."}},
		{Text: []string{"You're welcome."}},
	}})
	sid := e.newSession(t)

	e.send(t, sid, "Please remember that my cat is called Miso.")
	if runs := e.waitRuns(t, sid, 1); runs[0].Status != core.RunDone || runs[0].ToolCalls != 1 {
		t.Fatalf("memory run = %+v", runs[0])
	}
	var mem dto.MemoryDoc
	if code := e.JSON(t, http.MethodGet, "/api/memory/MEMORY", nil, &mem); code != http.StatusOK || !strings.Contains(mem.Content, "The user's cat is called Miso.") {
		t.Fatalf("MEMORY.md after the write = %d %q", code, mem.Content)
	}
	e.send(t, sid, "What is my cat called?")
	e.waitRuns(t, sid, 2)

	var user dto.MemoryDoc
	if code := e.JSON(t, http.MethodGet, "/api/memory/USER", nil, &user); code != http.StatusOK {
		t.Fatalf("GET USER.md = %d", code)
	}
	put := e.Do(t, http.MethodPut, "/api/memory/USER", dto.MemoryPutReq{Content: "- Prefers metric units.\n"},
		http.Header{"If-Match": {`"` + user.ETag + `"`}})
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT USER.md = %s %s", put.Status, put.Body)
	}
	e.send(t, sid, "Thanks!")
	e.waitRuns(t, sid, 3)

	reqs := chAgentRequests(t, e)
	if len(reqs) != 4 {
		t.Fatalf("agent requests = %d, want 4 (memory call, its follow-up, two more turns)", len(reqs))
	}
	for i, r := range reqs {
		if !bytes.Equal(r.rawTools, reqs[0].rawTools) {
			t.Fatalf("request %d: the tools block changed", i)
		}
		if len(r.rawMsgs) == 0 || r.Messages[0].Role != "system" || !bytes.Equal(r.rawMsgs[0], reqs[0].rawMsgs[0]) {
			t.Fatalf("request %d: the system prompt changed", i)
		}
		if s := r.system(); strings.Contains(s, "Miso") || strings.Contains(s, "metric units") {
			t.Fatalf("request %d: a mid-epoch memory change entered the system prompt", i)
		}
		if i > 0 {
			prev := reqs[i-1].rawMsgs
			if len(r.rawMsgs) <= len(prev) {
				t.Fatalf("request %d does not extend request %d", i, i-1)
			}
			for j := range prev {
				if !bytes.Equal(prev[j], r.rawMsgs[j]) {
					t.Fatalf("request %d: message %d differs from request %d:\n%s\n%s", i, j, i-1, prev[j], r.rawMsgs[j])
				}
			}
		}
	}
	if len(reqs[0].Tools) == 0 || !slices.Contains(reqs[0].toolNames(), "memory") {
		t.Fatalf("tools = %v", reqs[0].toolNames())
	}
	if tc := chTurnContext(reqs[0].lastUser()); tc == "" || strings.Contains(tc, "memory changed") {
		t.Fatalf("first turn-context = %q", tc)
	}
	if tc := chTurnContext(reqs[2].lastUser()); !strings.Contains(tc, "memory changed since the last turn:") || !strings.Contains(tc, "The user's cat is called Miso.") {
		t.Fatalf("turn-context after the memory write = %q", tc)
	}
	tc := chTurnContext(reqs[3].lastUser())
	if !strings.Contains(tc, "memory changed since the last turn:") || !strings.Contains(tc, "Prefers metric units.") || strings.Contains(tc, "Miso") {
		t.Fatalf("turn-context after the USER.md edit = %q (want only the new change)", tc)
	}
}

// TestCompaction is E9: a 16K model (compact profile) through 12 forced
// compactions (/compact via the API, each folding one more turn). Every
// agent request carries the whole short constitution and the user's pin; a
// standing instruction from the history is auto-pinned and announced once.
func TestCompaction(t *testing.T) {
	const (
		window     = 16384
		reserve    = 2048 // compact profile
		pin        = "Don't delete anything until I say so."
		standing   = "From now on, always cite the file path you used."
		compaction = 12
	)
	summary := strings.Join([]string{"## Goals", "- Review the chunks the user sends.", "## Decisions", "None.", "## Facts",
		"- One chunk arrives per turn.", "## Open tasks", "None.", "## Standing instructions", "- " + standing, "## Files", "None."}, "\n")
	e := chStart(t, chOpts{window: window, rules: []mockllm.Rule{
		chTitleRule,
		{MatchSystem: `^You compress a conversation`, Text: []string{summary}},
		{Text: []string{"Reviewed."}},
	}})
	_, file, _, _ := runtime.Caller(0)
	constitution, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "internal", "agent", "CONSTITUTION.short.md"))
	if err != nil {
		t.Fatal(err)
	}

	sid := e.newSession(t)
	if code := e.JSON(t, http.MethodPost, "/api/sessions/"+string(sid)+"/pins", dto.PinReq{Text: pin}, nil); code/100 != 2 {
		t.Fatalf("add pin = %d", code)
	}
	filler := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit, sed do eiusmod tempor. ", 15)
	turns := 0
	turn := func(extra string) {
		turns++
		e.send(t, sid, fmt.Sprintf("Turn %d: please review chunk %d. %s%s", turns, turns, extra, filler))
		e.waitRuns(t, sid, turns)
	}
	compact := func(epoch int) {
		if code := e.JSON(t, http.MethodPost, "/api/sessions/"+string(sid)+"/compact", nil, nil); code != http.StatusAccepted {
			t.Fatalf("compact = %d", code)
		}
		chEventually(t, 20*time.Second, fmt.Sprintf("epoch %d", epoch), func() bool {
			var d dto.SessionDetail
			return e.JSON(t, http.MethodGet, "/api/sessions/"+string(sid), nil, &d) == http.StatusOK && d.Session.Epoch == epoch
		})
	}
	for i := 1; i <= 5; i++ {
		extra := ""
		if i == 2 {
			extra = standing + " "
		}
		turn(extra)
	}
	for k := 1; k <= compaction; k++ {
		if k > 1 {
			turn("")
		}
		compact(k)
	}
	turn("")

	var d dto.SessionDetail
	if code := e.JSON(t, http.MethodGet, "/api/sessions/"+string(sid), nil, &d); code != http.StatusOK || d.Session.Epoch != compaction {
		t.Fatalf("session = %d %+v", code, d.Session)
	}
	if !slices.ContainsFunc(d.Pins, func(p *core.Pin) bool { return p.Text == pin && p.Source == "user" }) ||
		!slices.ContainsFunc(d.Pins, func(p *core.Pin) bool { return p.Text == standing && p.Source == "auto" && p.MsgSeq > 0 }) {
		t.Fatalf("pins = %+v", d.Pins)
	}

	all := chRequests(t, e.llm)
	summaries := 0
	for _, r := range all {
		if strings.HasPrefix(r.system(), "You compress a conversation") {
			summaries++
		}
	}
	if summaries != compaction {
		t.Fatalf("summarizer requests = %d, want %d (no retries, no fallback)", summaries, compaction)
	}
	reqs := chAgentRequests(t, e)
	if len(reqs) != turns {
		t.Fatalf("agent requests = %d, want one per turn (%d)", len(reqs), turns)
	}
	for i, r := range reqs {
		sys := r.system()
		if !strings.Contains(sys, strings.TrimSpace(string(constitution))) {
			t.Fatalf("request %d lacks the constitution", i)
		}
		if !strings.Contains(sys, "- Profile: compact") || len(r.Tools) > 8 {
			t.Fatalf("request %d: not the compact profile (tools %v)", i, r.toolNames())
		}
		if !strings.Contains(sys, "- "+pin) {
			t.Fatalf("request %d lacks the pinned instruction verbatim", i)
		}
		if r.size/4 > window-reserve {
			t.Fatalf("request %d is %d bytes, over window - reserve", i, r.size)
		}
	}
	last := reqs[len(reqs)-1]
	if !strings.Contains(last.system(), "- "+standing) {
		t.Fatal("the auto pin is not in the system prompt after the last compaction")
	}
	if !slices.ContainsFunc(last.Messages, func(m chMsg) bool {
		return strings.Contains(chText(m.Content), fmt.Sprintf(`<context-summary epoch="%d">`, compaction))
	}) {
		t.Fatalf("the last request carries no epoch %d summary", compaction)
	}
	for _, m := range last.Messages {
		if strings.Contains(chText(m.Content), "Turn 1: please review chunk 1.") {
			t.Fatal("a folded turn is still in the history after 12 compactions")
		}
	}

	var page dto.MessagePage
	if code := e.JSON(t, http.MethodGet, "/api/sessions/"+string(sid)+"/messages?limit=200", nil, &page); code != http.StatusOK {
		t.Fatalf("messages = %d", code)
	}
	announce := "Pinned “" + standing + "” so Bothy keeps it in mind."
	announced := 0
	for _, m := range page.Messages {
		if m.Kind != core.KindNotice {
			continue
		}
		switch text := m.PlainText(); {
		case text == announce:
			announced++
		case strings.Contains(text, "dropped"):
			t.Fatalf("compaction fell back to dropping turns: %q", text)
		}
	}
	if announced != 1 {
		t.Fatalf("auto-pin announced %d times, want once", announced)
	}
}
