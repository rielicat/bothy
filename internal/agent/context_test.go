package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

func textSteps(n int, prefix string) []fakes.LLMStep {
	steps := make([]fakes.LLMStep, n)
	for i := range steps {
		steps[i] = fakes.LLMStep{Text: []string{fmt.Sprintf("%s %d", prefix, i+1)}, Usage: core.Usage{Input: 1000 + i, Output: 10}}
	}
	return steps
}

func goodSummary(standing ...string) string {
	s := "## Goals\nHelp the user.\n## Decisions\nNone.\n## Facts\nNone.\n## Open tasks\nNone.\n## Standing instructions\n"
	if len(standing) == 0 {
		s += "None.\n"
	}
	for _, x := range standing {
		s += "- " + x + "\n"
	}
	return s + "## Files\nNone.\n"
}

func toolsJSON(t *testing.T, req *core.Request) string {
	t.Helper()
	b, err := json.Marshal(req.Tools)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// prefixHashes returns the prefix hash of every agent request in order, and
// checks the recorded llm_calls carry exactly those hashes.
func (h *harness) prefixHashes() []string {
	h.t.Helper()
	var out []string
	for _, req := range h.requests() {
		out = append(out, prefixHash(req.Tools, req.System))
	}
	var stored []string
	for _, run := range h.runs() {
		calls, err := h.store.Runs().LLMCalls(h.ctx, core.SystemViewer(), run.ID)
		if err != nil {
			h.t.Fatal(err)
		}
		for _, c := range calls {
			if c.Purpose == "agent" {
				stored = append(stored, c.PrefixHash)
			}
		}
	}
	a, b := slices.Clone(out), stored
	slices.Sort(a)
	slices.Sort(b)
	if !slices.Equal(a, b) {
		h.t.Fatalf("recorded prefix hashes %v != request hashes %v", b, a)
	}
	return out
}

func TestPrefixStableAcrossTurnsUntilCompaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tools := []core.Tool{&testTool{name: "fs_read"}, &testTool{name: "memory", se: core.SELocalWrite}}
		h := newHarness(t, opts{tools: tools, steps: textSteps(6, "reply")})
		defer h.close()
		h.mem.Files = map[core.PrincipalID]map[core.MemoryFile]string{h.pid: {core.FileMemory: "Likes tea."}}
		var hashes []string
		for i := 1; i <= 5; i++ {
			switch i {
			case 3:
				if _, err := h.mem.Apply(h.ctx, h.pid, "", core.MemoryOp{File: core.FileMemory, Op: "add", New: "Dog is called Rex."}, core.ProvUser); err != nil {
					t.Fatal(err)
				}
			case 4:
				if err := h.store.Pins().Add(h.ctx, &core.Pin{Session: h.sess.ID, Text: "Answer in British English.", Source: "user", CreatedAt: h.r.now()}); err != nil {
					t.Fatal(err)
				}
			}
			res := h.submit(fmt.Sprintf("turn %d", i))
			synctest.Wait()
			if st := h.run(res.Run).Status; st != core.RunDone {
				t.Fatalf("turn %d: %s", i, st)
			}
		}
		reqs := h.requests()
		if len(reqs) != 5 {
			t.Fatalf("requests = %d", len(reqs))
		}
		for i, req := range reqs[1:] {
			if toolsJSON(t, req) != toolsJSON(t, reqs[0]) || req.System[0].Text != reqs[0].System[0].Text || req.System[1].Text != reqs[0].System[1].Text {
				t.Fatalf("prefix of turn %d differs from turn 1", i+2)
			}
		}
		if !strings.Contains(lastUserText(reqs[2]), "MEMORY add: Dog is called Rex.") {
			t.Fatalf("memory delta missing from turn 3 context: %q", lastUserText(reqs[2]))
		}
		if !strings.Contains(lastUserText(reqs[3]), "Answer in British English.") || strings.Contains(reqs[3].System[1].Text, "British") {
			t.Fatal("new pin should appear in the turn-context, not in S2, until compaction")
		}
		if strings.Contains(lastUserText(reqs[4]), "Dog is called Rex") {
			t.Fatal("a delta is announced only once")
		}
		hashes = h.prefixHashes()
		for _, x := range hashes {
			if x != hashes[0] {
				t.Fatalf("prefix hashes differ before compaction: %v", hashes)
			}
		}
		if h.session().SnapshotHash == "" {
			t.Fatal("snapshot hash not stored")
		}

		h.llm.setSummaries(goodSummary())
		if err := h.r.Compact(h.ctx, core.Viewer{Principals: []core.PrincipalID{h.pid}}, h.sess.ID); err != nil {
			t.Fatal(err)
		}
		res := h.submit("turn 6")
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("turn 6: %s", st)
		}
		reqs = h.requests()
		after := reqs[len(reqs)-1]
		if !strings.Contains(after.System[1].Text, "Dog is called Rex.") || !strings.Contains(after.System[1].Text, "Answer in British English.") {
			t.Fatal("compaction did not refresh memory and pins into S2")
		}
		hashes = h.prefixHashes()
		if hashes[len(hashes)-1] == hashes[0] {
			t.Fatal("prefix hash did not change after compaction")
		}
		if !strings.Contains(allText(after), `<context-summary epoch="1">`) {
			t.Fatal("summary block missing")
		}
	})
}

func TestConstitutionAndInstructionsSurviveCompactions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: textSteps(7, "ok")})
		defer h.close()
		const standing = "From now on, always answer in French."
		if err := h.store.Pins().Add(h.ctx, &core.Pin{Session: h.sess.ID, Text: "don't act until I say", Source: "user", CreatedAt: h.r.now()}); err != nil {
			t.Fatal(err)
		}
		for i, text := range []string{"Hello there. " + standing, "two", "three", "four", "five", "six"} {
			if res := h.submit(text); func() core.RunStatus { synctest.Wait(); return h.run(res.Run).Status }() != core.RunDone {
				t.Fatalf("turn %d failed", i+1)
			}
		}
		h.llm.setSummaries(goodSummary(standing), goodSummary(standing))
		v := core.Viewer{Principals: []core.PrincipalID{h.pid}}
		for range 10 {
			if err := h.r.Compact(h.ctx, v, h.sess.ID); err != nil {
				t.Fatal(err)
			}
		}
		res := h.submit("seven")
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("final turn: %s", st)
		}
		reqs := h.requests()
		last := reqs[len(reqs)-1]
		if last.System[0].Text != s1(profileStandard, nil) || !strings.Contains(last.System[0].Text, "Bothy constitution") {
			t.Fatal("constitution missing after compactions")
		}
		s2 := last.System[1].Text
		if !strings.Contains(s2, "- don't act until I say") || !strings.Contains(s2, "- "+standing) {
			t.Fatalf("pins missing from S2:\n%s", s2)
		}
		sess := h.session()
		if sess.Epoch != 10 || !strings.Contains(sess.Summary, standing) {
			t.Fatalf("epoch %d summary %q", sess.Epoch, sess.Summary)
		}
		pins, _ := h.store.Pins().List(h.ctx, core.SystemViewer(), h.sess.ID)
		var auto int
		for _, p := range pins {
			if p.Source == "auto" {
				auto++
				if p.Text != standing {
					t.Fatalf("auto pin = %q", p.Text)
				}
			}
		}
		if auto != 1 {
			t.Fatalf("auto pins = %d", auto)
		}
		if !slicesContains(h.notices(), "I'll keep following: '"+standing+"'. /pins to manage.") {
			t.Fatalf("auto-pin not announced: %v", h.notices())
		}
	})
}

func TestSummaryValidationRetriesWithMissingQuotes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: textSteps(5, "ok")})
		defer h.close()
		const standing = "Never email my boss."
		for _, text := range []string{standing, "two", "three", "four", "five"} {
			h.submit(text)
			synctest.Wait()
		}
		h.llm.setSummaries(goodSummary(), goodSummary(standing))
		if err := h.r.Compact(h.ctx, core.SystemViewer(), h.sess.ID); err != nil {
			t.Fatal(err)
		}
		if got := h.session().Summary; !strings.Contains(got, standing) {
			t.Fatalf("summary = %q", got)
		}
		h.llm.mu.Lock()
		retry := h.llm.auxReqs[1].Messages[0].PlainText()
		h.llm.mu.Unlock()
		if !strings.Contains(retry, "- "+standing) {
			t.Fatalf("retry did not list the missing quote:\n%s", retry)
		}
	})
}

func TestSummarizerFailingTwiceFallsBackToTruncation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: textSteps(6, "ok")})
		defer h.close()
		for _, text := range []string{"one", "two", "three", "four", "five"} {
			h.submit(text)
			synctest.Wait()
		}
		before := h.session()
		h.llm.setSummaries() // every attempt fails
		if err := h.r.Compact(h.ctx, core.SystemViewer(), h.sess.ID); err != nil {
			t.Fatal(err)
		}
		after := h.session()
		if after.Epoch != 1 || after.SummaryUpto <= before.SummaryUpto || after.Summary != "" {
			t.Fatalf("session after fallback: epoch %d upto %d summary %q", after.Epoch, after.SummaryUpto, after.Summary)
		}
		if !slicesContains(h.notices(), droppedNotice) {
			t.Fatalf("notices = %v", h.notices())
		}
		h.llm.mu.Lock()
		attempts := h.llm.summaryN
		h.llm.mu.Unlock()
		if attempts != 2 {
			t.Fatalf("summarizer attempts = %d", attempts)
		}
		res := h.submit("six")
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("run after fallback: %s", st)
		}
		if strings.Contains(allText(h.requests()[5]), "one\n") {
			t.Fatal("dropped turn still sent")
		}
	})
}

// estimateRequest mirrors the assembler's accounting.
func estimateRequest(r *Runner, ref core.ModelRef, req *core.Request) int {
	est := func(s string) int {
		if s == "" {
			return 0
		}
		return r.d.LLM.Estimate(s, ref)
	}
	n := 0
	for _, s := range req.System {
		n += est(s.Text)
	}
	if len(req.Tools) > 0 {
		b, _ := json.Marshal(req.Tools)
		n += est(string(b))
	}
	for _, m := range req.Messages {
		n += sum(partTokens(est, m)) + msgOverhead
	}
	return n
}

func Test16KWindowRequestsFitAndBreakdownsSum(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		info := core.ModelInfo{Ref: core.ModelRef{Provider: "ollama", Model: "qwen3:14b"}, Context: 16384, MaxOutput: 2048, Tools: true, Local: true, Price: core.Price{Known: true}}
		big := &testTool{name: "fs_read", run: func(_ context.Context, _ *core.ToolEnv, _ *core.Action) (*core.ToolResult, error) {
			return &core.ToolResult{Content: strings.Repeat("log line with some words\n", 600)}, nil
		}}
		var steps []fakes.LLMStep
		for i := range 12 {
			steps = append(steps,
				fakes.LLMStep{Text: []string{"Reading."}, ToolCalls: []core.ToolCall{call("fs_read", fmt.Sprintf(`{"n":%d}`, i))}},
				fakes.LLMStep{Text: []string{strings.Repeat("Here is a long answer. ", 120)}, Usage: core.Usage{Input: 7000, Output: 600}})
		}
		h := newHarness(t, opts{info: info, tools: []core.Tool{big}, steps: steps})
		defer h.close()
		h.llm.setSummaries() // the summarizer fails; the truncation fallback must still keep every request in budget
		for i := range 12 {
			res := h.submit(fmt.Sprintf("Question %d: %s", i, strings.Repeat("please analyse the logs carefully ", 60)))
			synctest.Wait()
			if st := h.run(res.Run).Status; st != core.RunDone {
				t.Fatalf("turn %d: %s (%s) notices %v", i, st, h.run(res.Run).Error, h.notices())
			}
		}
		c := h.cfg
		p := profileFor(c, info.Context)
		if p != profileCompact {
			t.Fatalf("profile = %s", p)
		}
		limit := info.Context - reserveFor(c, p, info.Context)
		for i, req := range h.requests() {
			if n := estimateRequest(h.r, info.Ref, req); n > limit {
				t.Fatalf("request %d: %d tokens > %d", i, n, limit)
			}
			checkPairs(t, req)
			if req.System[0].Text != s1(profileCompact, nil) {
				t.Fatal("compact profile must use the short constitution")
			}
			if req.NumCtx != info.Context || req.MaxTokens != 2048 {
				t.Fatalf("num_ctx %d max_tokens %d", req.NumCtx, req.MaxTokens)
			}
		}
		if h.session().Epoch == 0 {
			t.Fatal("no compaction happened on a 16K window")
		}
		for _, run := range h.runs() {
			calls, _ := h.store.Runs().LLMCalls(h.ctx, core.SystemViewer(), run.ID)
			for _, ci := range calls {
				bd := ci.Breakdown
				if ci.Purpose != "agent" {
					continue
				}
				total := bd.Constitution + bd.Persona + bd.Memory + bd.Skills + bd.Pins + bd.Tools + bd.Summary + bd.History + bd.ToolResults + bd.TurnContext
				if total != bd.Total || bd.Window != info.Context {
					t.Fatalf("breakdown %+v sums to %d", bd, total)
				}
			}
		}
		// Tool output on the compact profile is capped at 1000 tokens.
		for _, m := range h.messages() {
			for _, p := range m.Parts {
				if p.ToolResult != nil && len(p.ToolResult.Content) > 1000*36/10+200 {
					t.Fatalf("tool result of %d bytes exceeds the compact cap", len(p.ToolResult.Content))
				}
			}
		}
	})
}

func TestProfilesShapeTheToolsBlock(t *testing.T) {
	names := func(req *core.Request) []string {
		var out []string
		for _, d := range req.Tools {
			out = append(out, d.Name)
		}
		return out
	}
	var all []core.Tool
	for _, n := range []string{"shell", "fs_read", "fs_write", "fs_list", "web_search", "web_fetch", "memory", "message", "tool_search", "use_tool", "python"} {
		all = append(all, &testTool{name: n})
	}
	cases := []struct {
		window int
		want   int
		tools  bool
	}{{128_000, 11, true}, {20_000, 8, true}, {10_000, 5, true}, {4096, 0, false}}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.window), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newHarness(t, opts{tools: all, info: core.ModelInfo{Ref: core.ModelRef{Provider: "p", Model: "m"}, Context: tc.window, MaxOutput: 1024, Tools: true},
					steps: []fakes.LLMStep{{Text: []string{"hi"}}}})
				defer h.close()
				h.submit("hello")
				synctest.Wait()
				req := h.requests()[0]
				if got := names(req); len(got) != tc.want {
					t.Fatalf("tools = %v", got)
				}
				if (req.ToolChoice == "auto") != tc.tools || req.ToolsCache != tc.tools {
					t.Fatalf("tool choice %q cache %v", req.ToolChoice, req.ToolsCache)
				}
				if !req.System[1].CacheBreak || !req.CacheTail {
					t.Fatal("cache breakpoints missing")
				}
				if !tc.tools && !slicesContains(h.notices(), "This model's window (4096 tokens) is too small for tools, so it can only chat.") {
					t.Fatalf("notices = %v", h.notices())
				}
			})
		})
	}
}

func TestModelOverrideUnknownIsCleared(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"hi"}}}})
		defer h.close()
		if _, err := h.r.mutateSession(h.ctx, h.sess.ID, func(s *core.Session) error { s.ModelOverride = "nope/model"; return nil }); err != nil {
			t.Fatal(err)
		}
		res := h.submit("hello")
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("status %s", st)
		}
		if h.session().ModelOverride != "" {
			t.Fatal("override not cleared")
		}
		if len(h.notices()) != 1 || !strings.Contains(h.notices()[0], `"nope/model" is not a known model`) {
			t.Fatalf("notices = %v", h.notices())
		}
	})
}

func TestStandingRulesAndSkillsIndex(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{cfg: func(c *config.Config) { c.Agent.StandingRules = []string{"Never use the word synergy."} },
			steps: []fakes.LLMStep{{Text: []string{"hi"}}}})
		defer h.close()
		h.skills.Metas = []core.SkillMeta{{Name: "pdf", Description: "Work with PDF files", Status: core.SkillEnabled, Hash: "h", ApprovedHash: "h"},
			{Name: "draft", Description: "Pending skill", Status: core.SkillPending}}
		h.submit("hello")
		synctest.Wait()
		req := h.requests()[0]
		if !strings.Contains(req.System[0].Text, "## Standing rules from the administrator\n- Never use the word synergy.") {
			t.Fatal("standing rules missing from S1")
		}
		if !strings.Contains(req.System[1].Text, "- pdf: Work with PDF files") || strings.Contains(req.System[1].Text, "draft") {
			t.Fatalf("skills index:\n%s", req.System[1].Text)
		}
		if !strings.Contains(req.System[1].Text, "- Profile: standard") || !strings.Contains(req.System[1].Text, "<persona>") {
			t.Fatal("facts or persona missing")
		}
	})
}

func TestLongSingleTurnKeepsItsEpoch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		info := core.ModelInfo{Ref: core.ModelRef{Provider: "ollama", Model: "qwen3:14b"}, Context: 16384, MaxOutput: 2048, Tools: true, Local: true, Price: core.Price{Known: true}}
		big := &testTool{name: "fs_read", run: func(_ context.Context, _ *core.ToolEnv, _ *core.Action) (*core.ToolResult, error) {
			return &core.ToolResult{Content: strings.Repeat("log line with some words\n", 600)}, nil
		}}
		var steps []fakes.LLMStep
		for i := range 8 {
			steps = append(steps, fakes.LLMStep{ToolCalls: []core.ToolCall{call("fs_read", fmt.Sprintf(`{"n":%d}`, i))}})
		}
		steps = append(steps, fakes.LLMStep{Text: []string{"Done."}})
		h := newHarness(t, opts{info: info, tools: []core.Tool{big}, steps: steps})
		defer h.close()
		res := h.submit("read all the logs")
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("status %s (%s)", st, h.run(res.Run).Error)
		}
		reqs := h.requests()
		limit := info.Context - reserveFor(h.cfg, profileCompact, info.Context)
		for i, req := range reqs {
			if n := estimateRequest(h.r, info.Ref, req); n > limit {
				t.Fatalf("request %d: %d tokens > %d", i, n, limit)
			}
		}
		if h.session().Epoch != 0 {
			t.Fatalf("epoch = %d; one turn has nothing to fold", h.session().Epoch)
		}
		if first, last := prefixHash(reqs[0].Tools, reqs[0].System), prefixHash(reqs[len(reqs)-1].Tools, reqs[len(reqs)-1].System); first != last {
			t.Fatal("the prefix changed within one turn")
		}
	})
}
