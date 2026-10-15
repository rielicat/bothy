package channel

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/channeltest"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

type runFeed struct {
	t    *testing.T
	sink core.RunSink
	run  core.RunID
	sess core.SessionID
}

func (h *harness) feed(chat core.ChatRef, mode string) *runFeed {
	return &runFeed{t: h.t, sink: h.hub.StreamTo(chat, mode), run: core.RunID(core.NewID(core.PrefixRun)),
		sess: core.SessionID(core.NewID(core.PrefixSession))}
}

func (f *runFeed) emit(typ core.EventType, d core.EventData) {
	f.sink.OnEvent(core.Event{Type: typ, Run: f.run, Session: f.sess, At: time.Now(), Data: d})
	synctest.Wait()
}

func (f *runFeed) delta(text string) { f.emit(core.EvRunDelta, core.DeltaData{Text: text}) }

func (f *runFeed) finish(st core.RunStatus) {
	f.emit(core.EvRunFinished, core.RunFinishedData{Status: st, Steps: 3, Usage: core.Usage{Input: 12000, Output: 400}, Cost: core.MicrosFromUSD(0.006)})
	time.Sleep(30 * time.Second)
	synctest.Wait()
}

func withOps(oc **opsChannel) func(*harness) {
	return func(h *harness) {
		h.wrap = func(c *channeltest.Channel) core.Channel {
			*oc = &opsChannel{Channel: c}
			return *oc
		}
	}
}

func TestStreamEditsAtMostOncePerInterval(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var oc *opsChannel
		h := newHarness(t, withOps(&oc)).start()
		f := h.feed(h.dmChat(ownerChat), "stream")
		f.emit(core.EvRunStarted, core.RunStartedData{Trigger: core.TrigMessage})
		var want strings.Builder
		for i := range 60 {
			w := fmt.Sprintf("word%d ", i)
			want.WriteString(w)
			f.delta(w)
			time.Sleep(70 * time.Millisecond)
		}
		f.finish(core.RunDone)

		ops := oc.all()
		if len(ops) < 3 {
			t.Fatalf("only %d sends/edits for a 4 s stream", len(ops))
		}
		for i := 1; i < len(ops); i++ {
			if gap := ops[i].at.Sub(ops[i-1].at); gap < time.Second {
				t.Fatalf("ops %d and %d only %v apart (EditMinInterval 1s): %+v", i-1, i, gap, ops)
			}
		}
		first := ops[0]
		if first.kind != "send" || len(first.msg.Buttons) != 1 || first.msg.Buttons[0][0].Data != "s:"+string(f.run) {
			t.Fatalf("first op = %+v, want the live message with a Stop button", first)
		}
		last := ops[len(ops)-1]
		if last.kind != "edit" || len(last.msg.Buttons) != 0 {
			t.Fatalf("final op = %+v, want an edit without the Stop button", last)
		}
		if v := h.visible(ownerChat); len(v) != 1 || v[0] != strings.TrimSpace(want.String()) {
			t.Fatalf("chat shows %q", v)
		}
	})
}

func TestStreamPlaceholderTypingAndStatus(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		f := h.feed(h.dmChat(ownerChat), "stream")
		f.emit(core.EvRunStarted, core.RunStartedData{Trigger: core.TrigMessage})
		if n := len(h.ch.TypingIn); n != 1 {
			t.Fatalf("typing calls at start = %d", n)
		}
		if n := len(h.ch.SentMessages()); n != 0 {
			t.Fatal("placeholder sent before 1.5 s")
		}
		time.Sleep(1600 * time.Millisecond)
		synctest.Wait()
		if v := h.visible(ownerChat); len(v) != 1 || v[0] != "Thinking…" {
			t.Fatalf("after 1.5 s the chat shows %q, want the placeholder", v)
		}
		time.Sleep(4 * time.Second)
		synctest.Wait()
		if n := len(h.ch.TypingIn); n < 2 {
			t.Fatalf("typing not renewed after 4 s (%d calls)", n)
		}
		f.emit(core.EvToolCall, core.ToolCallData{Tool: "shell", Title: "Run shell command",
			Action: &core.Action{Kind: core.ActExec, Argv: []string{"bash", "-lc", "pip install pandas"}}})
		time.Sleep(time.Second)
		synctest.Wait()
		h.wantVisible(ownerChat, "_Running `pip install pandas`…_")
		f.emit(core.EvToolResult, core.ToolResultData{Status: core.TCOK})
		f.finish(core.RunDone)
		if v := h.visible(ownerChat); len(v) != 1 || v[0] != "Done." {
			t.Fatalf("a run without text ends with %q", v)
		}
	})
}

func TestStreamSplitsLongReply(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		var b strings.Builder
		for i := 0; b.Len() < 3500; i++ {
			fmt.Fprintf(&b, "Paragraph %d has a few words to make it longer than a line or two of prose.\n\n", i)
		}
		b.WriteString("```python\n")
		for i := 0; i < 60; i++ {
			fmt.Fprintf(&b, "print('line %d of a code block that crosses the split')\n", i)
		}
		b.WriteString("```\n\n")
		for i := 0; b.Len() < 10000; i++ {
			fmt.Fprintf(&b, "Closing paragraph %d with enough words to fill out the reply.\n\n", i)
		}
		text := strings.TrimSpace(b.String())

		f := h.feed(h.dmChat(ownerChat), "stream")
		f.emit(core.EvRunStarted, core.RunStartedData{})
		for i := 0; i < len(text); i += 700 {
			f.delta(text[i:min(len(text), i+700)])
			time.Sleep(200 * time.Millisecond)
		}
		f.finish(core.RunDone)

		pieces := h.visible(ownerChat)
		if len(pieces) < 3 {
			t.Fatalf("a %d-char reply arrived as %d messages", len(text), len(pieces))
		}
		for i, p := range pieces {
			if ulen(p) > 4096 {
				t.Fatalf("piece %d has %d units", i, ulen(p))
			}
			if open, _, _ := fenceState("", p); open {
				t.Fatalf("piece %d leaves a code fence open:\n%s", i, p)
			}
		}
		if got, want := contentLines(strings.Join(pieces, "\n")), contentLines(text); !slices.Equal(got, want) {
			t.Fatalf("split lost or reordered content: got %d lines, want %d", len(got), len(want))
		}
	})
}

// contentLines drops blank and fence lines, which splitting may add.
func contentLines(s string) []string {
	var out []string
	open, marker := false, ""
	for l := range strings.SplitSeq(s, "\n") {
		t := fenceCandidate(l)
		switch {
		case open && closesFence(t, marker):
			open = false
			continue
		case !open && openingFence(t) != "":
			open, marker = true, openingFence(t)
			continue
		}
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func TestStreamNeutralizesLinksWhenExposed(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		chat := h.dmChat(ownerChat)
		sess, err := h.runner.NewSession(h.ctx, core.UserPrincipal(h.owner.ID), "telegram", &chat)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.Sessions().SetTaint(h.ctx, sess.ID, core.TaintPrivate|core.TaintUntrusted); err != nil {
			t.Fatal(err)
		}
		f := h.feed(chat, "stream")
		f.sess = sess.ID
		f.delta("Upload it to https://evil.example/c?d=secret or [here](https://evil.example/x), see evil.org. `keep https://code.example`")
		f.finish(core.RunDone)
		got := h.lastText(ownerChat)
		for _, bad := range []string{"https://evil", "](", "evil.org"} {
			if strings.Contains(got, bad) {
				t.Fatalf("exposed session output %q still contains %q", got, bad)
			}
		}
		for _, want := range []string{"hxxps://evil[.]example/c?d=secret", "here (hxxps://evil[.]example/x)", "evil[.]org", "`keep https://code.example`"} {
			if !strings.Contains(got, want) {
				t.Fatalf("exposed session output %q lacks %q", got, want)
			}
		}

		g := h.feed(chat, "stream")
		g.delta("Docs at https://docs.example/start")
		g.finish(core.RunDone)
		if !strings.Contains(h.lastText(ownerChat), "https://docs.example/start") {
			t.Fatal("a clean session's links were neutralized")
		}
	})
}

// The first time a conversation's links are neutralized, the reply says why
// and how to get clickable links back; it says so once.
func TestStreamExplainsNeutralizedLinksOnce(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		chat := h.dmChat(ownerChat)
		sess, err := h.runner.NewSession(h.ctx, core.UserPrincipal(h.owner.ID), "telegram", &chat)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.store.Sessions().SetTaint(h.ctx, sess.ID, core.TaintPrivate|core.TaintUntrusted); err != nil {
			t.Fatal(err)
		}
		reply := func(text string) string {
			f := h.feed(chat, "stream")
			f.sess = sess.ID
			f.delta(text)
			f.finish(core.RunDone)
			return h.lastText(ownerChat)
		}
		if got := reply("No links in this one. "); strings.Contains(got, linkNote) {
			t.Fatalf("a reply without links got the note: %q", got)
		}
		got := reply("I opened PR #2: https://github.com/acme/web/pull/2 ")
		if !strings.Contains(got, "hxxps://github[.]com/acme/web/pull/2") || !strings.HasSuffix(got, "\n\n_"+linkNote+"_") {
			t.Fatalf("first neutralized reply = %q", got)
		}
		if got := reply("Again: https://github.com/acme/web/pull/3 "); strings.Contains(got, linkNote) {
			t.Fatalf("the note came twice: %q", got)
		}
	})
}

// Refine mode works for a while before the answer appears; the chat says
// so instead of a bare "Thinking…", until the refined answer streams.
func TestStreamShowsRefining(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		f := h.feed(h.dmChat(ownerChat), "stream")
		f.emit(core.EvRunStarted, core.RunStartedData{Trigger: core.TrigMessage})
		f.emit(core.EvRunStatus, core.RunStatusData{Status: "thinking"})
		f.emit(core.EvRunStatus, core.RunStatusData{Status: "thinking", Detail: core.RefineStatusDetail})
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if v := h.visible(ownerChat); len(v) != 1 || v[0] != "_Refining the answer…_" {
			t.Fatalf("while refining the chat shows %q", v)
		}
		f.delta("The answer is 42. ")
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if got := h.lastText(ownerChat); strings.Contains(got, "Refining") {
			t.Fatalf("the refined answer still says it is refining: %q", got)
		}
		f.finish(core.RunDone)
	})
}

// A tool's status line says what it does in plain words, its identifiers in
// code so none is a tappable link.
func TestToolDoing(t *testing.T) {
	redact := fakes.NewRedactor()
	redact.Add("k", []byte("SECRETQUERY"))
	for _, tc := range []struct {
		d    core.ToolCallData
		want string
	}{
		{core.ToolCallData{Tool: "fs_delete", Title: "Delete file", Action: &core.Action{Kind: core.ActFSDelete, Paths: []string{"telegram-del.txt"}}},
			"Deleting `telegram-del.txt`"},
		{core.ToolCallData{Tool: "fs_list", Title: "List files", Action: &core.Action{Kind: core.ActFSRead, Paths: []string{"."}}},
			"Looking in Bothy's files"},
		{core.ToolCallData{Tool: "web_search", Title: "Search the web", Action: &core.Action{Kind: core.ActSearch,
			Payload: []byte(`{"query":"pandas SECRETQUERY"}`)}}, "Searching the web for “pandas " + escapeMD(fakes.Mask) + "”"},
		{core.ToolCallData{Tool: "web_fetch", Title: "Fetch web page", Action: &core.Action{Kind: core.ActHTTP, URL: "https://docs.example.com/a?b=c", Method: "GET"}},
			"Opening `docs.example.com`"},
		{core.ToolCallData{Tool: "repo_checkout", Title: "Check out repository", Action: &core.Action{Kind: core.ActFSWrite,
			Payload: []byte(`{"repo":"acme/web","ref":"main"}`)}}, "Getting a copy of `acme/web` (`main`)"},
		{core.ToolCallData{Tool: "python", Title: "Run Python", Action: &core.Action{Kind: core.ActExec, Argv: []string{"python3", "-c", "print(1)"}}},
			"Running a Python script"},
		{core.ToolCallData{Tool: "mcp__linear__create_issue", Title: "MCP linear: Create issue", Action: &core.Action{Kind: core.ActMCP, MCPServer: "linear"}},
			"Create issue"},
		{core.ToolCallData{Tool: "schedule_list", Title: "List schedules"}, "List schedules"},
		{core.ToolCallData{Tool: "fs_read", Title: "Read file", Action: &core.Action{Kind: core.ActFSRead}}, "Read file"},
	} {
		if got := toolDoing(tc.d, redact.Redact); got != tc.want {
			t.Errorf("toolDoing(%s) = %q, want %q", tc.d.Tool, got, tc.want)
		}
	}
}

func TestStreamRedactsAndHoldsBackPartialSecrets(t *testing.T) {
	bubble(t, func(t *testing.T) {
		var oc *opsChannel
		h := newHarness(t, withOps(&oc)).start()
		h.redactor.Add("k", []byte("SECRETVALUE123"))
		f := h.feed(h.dmChat(ownerChat), "stream")
		f.delta("the key is SECRETVAL")
		time.Sleep(2 * time.Second)
		synctest.Wait()
		f.delta("UE123 do not share ")
		time.Sleep(2 * time.Second)
		synctest.Wait()
		f.finish(core.RunDone)
		for _, o := range oc.all() {
			if strings.Contains(o.msg.Text, "SECRETVAL") {
				t.Fatalf("a %s showed part of the secret: %q", o.kind, o.msg.Text)
			}
		}
		if got := h.lastText(ownerChat); !strings.Contains(got, fakes.Mask) {
			t.Fatalf("final text %q is not redacted", got)
		}
	})
}

// A registered secret may contain spaces, so holding back only the partial
// last word used to show its first words in a live edit; and a split into
// several messages redacted each piece alone, so a secret straddling the cut
// went out in halves.
func TestStreamNeverShowsPartOfASecretWithSpaces(t *testing.T) {
	const secret = "correct horse battery staple"
	fragments := []string{"correct", "horse", "battery", "staple"}
	check := func(t *testing.T, ops []op) {
		t.Helper()
		for _, o := range ops {
			for _, w := range fragments {
				if strings.Contains(o.msg.Text, w) {
					t.Fatalf("a %s showed %q of the secret: %q", o.kind, w, o.msg.Text)
				}
			}
		}
	}
	t.Run("live edit", func(t *testing.T) {
		bubble(t, func(t *testing.T) {
			var oc *opsChannel
			h := newHarness(t, withOps(&oc)).start()
			h.redactor.Add("pw", []byte(secret))
			f := h.feed(h.dmChat(ownerChat), "stream")
			f.delta("The passphrase is correct hor")
			time.Sleep(2 * time.Second)
			synctest.Wait()
			f.delta("se battery staple, keep it safe. ")
			time.Sleep(2 * time.Second)
			synctest.Wait()
			f.finish(core.RunDone)
			check(t, oc.all())
			if got := h.lastText(ownerChat); !strings.Contains(got, "The passphrase is "+fakes.Mask+", keep it safe.") {
				t.Fatalf("final text = %q", got)
			}
		})
	})
	t.Run("message split", func(t *testing.T) {
		bubble(t, func(t *testing.T) {
			var oc *opsChannel
			h := newHarness(t, withOps(&oc)).start()
			h.redactor.Add("pw", []byte(secret))
			f := h.feed(h.dmChat(ownerChat), "final")
			// Words up to just below the 4096-unit limit, then the secret
			// across it: a word-boundary cut falls inside the secret.
			filler := strings.Repeat("word ", (4096-14)/5)
			f.delta(filler + secret + " and more words after it.")
			f.finish(core.RunDone)
			check(t, oc.all())
			if n := len(oc.all()); n < 2 {
				t.Fatalf("the reply was not split (%d messages)", n)
			}
		})
	})
}

func TestStreamFinalModeAndHeartbeat(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		f := h.feed(h.dmChat(ownerChat), "final")
		f.emit(core.EvRunStarted, core.RunStartedData{Trigger: core.TrigSchedule})
		f.delta("Your daily summary: ")
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if len(h.ch.SentMessages()) != 0 || len(h.ch.TypingIn) != 0 {
			t.Fatal("final mode sent or typed before the run ended")
		}
		f.delta("all quiet.")
		f.finish(core.RunDone)
		if v := h.visible(ownerChat); len(v) != 1 || v[0] != "Your daily summary: all quiet." {
			t.Fatalf("final delivery = %q", v)
		}

		hb := h.feed(h.dmChat(ownerChat), "final")
		hb.delta("HEARTBEAT_OK nothing to report")
		hb.finish(core.RunDone)
		if n := len(h.visible(ownerChat)); n != 1 {
			t.Fatalf("a HEARTBEAT_OK reply was delivered (%d messages)", n)
		}

		bold := h.feed(h.dmChat(ownerChat), "final")
		bold.delta("**HEARTBEAT_OK**")
		bold.finish(core.RunDone)
		if n := len(h.visible(ownerChat)); n != 1 {
			t.Fatalf("an emphasised HEARTBEAT_OK reply was delivered (%d messages)", n)
		}
	})
}

// TestStreamScheduleHeader: a delivered schedule result arrives unasked, so
// it opens with the schedule that sent it and where to change it.
func TestStreamScheduleHeader(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		p := core.UserPrincipal(h.owner.ID)
		sess, err := h.runner.NewSession(h.ctx, p, "schedule", nil)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		sc := &core.Schedule{ID: core.ScheduleID(core.NewID(core.PrefixSchedule)), Principal: p, Name: "Weekly PR digest", Kind: core.SchedCron,
			Spec: "0 8 * * 1", TZ: "UTC", Prompt: "digest", Slot: core.SlotMain, MissedRun: "skip", Enabled: true,
			CreatedBy: "user:" + string(h.owner.ID), CreatedAt: now, UpdatedAt: now}
		if err := h.store.Schedules().Upsert(h.ctx, sc); err != nil {
			t.Fatal(err)
		}
		feed := func(trigger core.Trigger, schedule core.ScheduleID) *runFeed {
			r := &core.Run{Session: sess.ID, Principal: p, User: h.owner.ID, Trigger: trigger, Schedule: schedule, Status: core.RunDone, CreatedAt: now}
			if err := h.store.Runs().Create(h.ctx, r); err != nil {
				t.Fatal(err)
			}
			f := h.feed(h.dmChat(ownerChat), "final")
			f.run, f.sess = r.ID, sess.ID
			return f
		}

		f := feed(core.TrigSchedule, sc.ID)
		f.delta("There is 1 open PR in acme/web.")
		f.finish(core.RunDone)
		if got, want := h.lastText(ownerChat), "_From the schedule “Weekly PR digest” · change it under Schedules_\n\nThere is 1 open PR in acme/web."; got != want {
			t.Fatalf("schedule delivery = %q, want %q", got, want)
		}

		hb := feed(core.TrigHeartbeat, "")
		hb.delta("The staging deploy failed overnight.")
		hb.finish(core.RunDone)
		if got := h.lastText(ownerChat); got != "_From your check-in · change it under Schedules_\n\nThe staging deploy failed overnight." {
			t.Fatalf("check-in delivery = %q", got)
		}

		quiet := feed(core.TrigHeartbeat, "")
		quiet.delta("HEARTBEAT_OK")
		quiet.finish(core.RunDone)
		if n := len(h.visible(ownerChat)); n != 2 {
			t.Fatalf("a quiet check-in sent its header alone (%d messages)", n)
		}
	})
}

func TestStreamFinishNotesAndFooter(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		h.cfg.Set(func(c *config.Config) { c.Channels.UsageFooter = true })
		f := h.feed(h.dmChat(ownerChat), "stream")
		f.delta("partial answer ")
		f.finish(core.RunStopped)
		got := h.lastText(ownerChat)
		if !strings.Contains(got, "partial answer") || !strings.Contains(got, "_Stopped._") || !strings.Contains(got, "3 steps · 12.4K tok · $0.006") {
			t.Fatalf("final text = %q", got)
		}

		g := h.feed(h.dmChat(ownerChat), "stream")
		g.emit(core.EvMessageCreated, core.MessageData{Message: &core.Message{Kind: core.KindNotice,
			Parts: []core.Part{{Type: core.PartText, Text: "Budget exceeded: daily cap."}}}})
		g.finish(core.RunBudgetExceeded)
		if got := h.lastText(ownerChat); !strings.Contains(got, "Budget exceeded: daily cap.") {
			t.Fatalf("notice not shown: %q", got)
		}

		p := h.feed(h.dmChat(ownerChat), "stream")
		p.delta("I need approval first. ")
		p.emit(core.EvRunParked, core.RunParkedData{Approval: core.ApprovalID(core.NewID(core.PrefixApproval))})
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if got := h.lastText(ownerChat); !strings.Contains(got, "Parked") {
			t.Fatalf("parked run ends with %q", got)
		}
	})
}

func TestStreamToUnknownChannelIsDiscarded(t *testing.T) {
	bubble(t, func(t *testing.T) {
		h := newHarness(t).start()
		f := h.feed(core.ChatRef{Channel: "web", ChatID: "x"}, "stream")
		f.delta("nobody listens ")
		f.finish(core.RunDone)
		if n := len(h.ch.SentMessages()); n != 0 {
			t.Fatalf("sent %d messages for a chat without an adapter", n)
		}
	})
}
