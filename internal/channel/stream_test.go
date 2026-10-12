package channel

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/testkit/channeltest"
	"github.com/bothyhq/bothy/internal/testkit/fakes"
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
		h.wantVisible(ownerChat, "Running `pip install pandas`…")
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
	for l := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(l); t != "" && openingFence(t) == "" {
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
