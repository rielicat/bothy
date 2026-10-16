//go:build e2e

package e2e

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/channel/telegram/telegramtest"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// TestRestart is E17: the gateway is SIGKILLed while a Telegram run waits on
// the model and a scheduled run is parked on an approval. After the restart
// the in-flight run is interrupted with a notice in the session and in the
// chat, while the parked approval is still there; approving it resumes the
// same run, which executes the action exactly once.
func TestRestart(t *testing.T) {
	const tgUser = 5001
	llm := pltLLM(t,
		mockllm.Rule{MatchUser: `^<approved-action`, Text: []string{"Saved after approval."}},
		mockllm.Rule{MatchUser: `E17-PARK`, ToolCalls: []core.ToolCall{pltCall("call_p", "fs_write", `{"path":"parked.txt","content":"written once\n"}`)}},
		mockllm.Rule{MatchUser: `E17-SLOW`, Fault: mockllm.FaultSlowTTFT, Delay: time.Minute, Text: []string{"too late"}},
	)
	tg := pltNewTelegram()
	cfg := pltYAML(llm.URL(), nil, `
policy:
  rules:
    - {id: e17-write, tool: fs_write, verdict: ask}
  approval:
    scheduled_timeout: 1s
`)
	g := Start(t, Options{Config: cfg, Platforms: []Platform{tg}})
	owner := pltOwner(t, g)
	tg.LetIn(t, owner, tgUser, true)

	// A scheduled run asks, times out after 1s and parks.
	sc := dto.ScheduleUpsertReq{Schedule: core.Schedule{Principal: core.UserPrincipal(owner.ID()), Name: "e17 nightly",
		Kind: core.SchedCron, Spec: "0 3 1 1 *", TZ: "UTC", Prompt: "E17-PARK write the report", Slot: core.SlotMain,
		Deliver: &core.ChatRef{Channel: "web"}, MissedRun: "skip", Enabled: true}}
	var created core.Schedule
	if code := owner.JSON(t, http.MethodPost, "/api/schedules", sc, &created); code != http.StatusOK {
		t.Fatalf("create schedule = %d", code)
	}
	var fired struct {
		Run core.RunID `json:"run_id"`
	}
	if code := owner.JSON(t, http.MethodPost, "/api/schedules/"+string(created.ID)+"/run", nil, &fired); code != http.StatusAccepted || fired.Run == "" {
		t.Fatalf("run schedule = %d", code)
	}
	parked := owner.WaitApproval(t, fired.Run, core.ApprovalParked)
	owner.WaitRun(t, fired.Run, 10*time.Second, func(r *core.Run) bool { return r.Status == core.RunParked && r.ParkedOn == parked.ID })

	// An interactive web run waits on a pending (not parked) approval.
	waiting := owner.Send(t, owner.NewSession(t).ID, "E17-PARK a web copy")
	pending := owner.WaitApproval(t, waiting, core.ApprovalPending)

	// A Telegram run is in flight: mockllm holds the request open. It is the
	// teammate's, so the web doesn't list it; the chat hears what happened.
	tg.Inject(telegramtest.Inbound{ChatID: tgUser, SenderID: tgUser, Text: "E17-SLOW tell me a long story"})
	deadline := time.Now().Add(15 * time.Second)
	for !slices.ContainsFunc(llm.Requests(), func(b []byte) bool { return strings.Contains(string(b), "E17-SLOW") }) {
		if time.Now().After(deadline) {
			t.Fatalf("the Telegram run never reached the model\nlogs:\n%s", g.Logs())
		}
		time.Sleep(25 * time.Millisecond)
	}

	g.Kill()
	g = Start(t, Options{DataDir: g.DataDir, Config: cfg, Platforms: []Platform{tg}})
	owner = pltLogin(t, g, "owner", pltPassword)

	t.Run("in-flight run is interrupted with a notice", func(t *testing.T) {
		tg.WaitText(t, tgUser, "restarted before it finished this task", 10*time.Second)
	})

	t.Run("pending approval of an interrupted run is cancelled", func(t *testing.T) {
		if d := owner.Finished(t, waiting); d.Run.Status != core.RunInterrupted {
			t.Fatalf("waiting run after restart = %+v, want interrupted", d.Run)
		}
		var a core.Approval
		if code := owner.JSON(t, http.MethodGet, "/api/approvals/"+string(pending.ID), nil, &a); code != http.StatusOK || a.Status != core.ApprovalCancelled {
			t.Fatalf("its approval = %d %+v, want cancelled", code, a)
		}
		if resp := owner.Do(t, http.MethodPost, "/api/approvals/"+string(pending.ID), dto.ApprovalResolveReq{Choice: core.AllowOnce}, nil); resp.StatusCode/100 == 2 {
			t.Fatalf("approving a cancelled approval = %s", resp.Status)
		}
	})

	t.Run("parked approval survives and resumes once", func(t *testing.T) {
		a := owner.WaitApproval(t, fired.Run, core.ApprovalParked)
		if a.ID != parked.ID {
			t.Fatalf("parked approval after restart = %s, want %s", a.ID, parked.ID)
		}
		owner.WaitRun(t, fired.Run, 5*time.Second, func(r *core.Run) bool { return r.Status == core.RunParked })
		owner.Resolve(t, a.ID, core.AllowOnce)
		d := owner.Finished(t, fired.Run)
		if d.Run.Status != core.RunDone || d.Run.Trigger != core.TrigSchedule {
			t.Fatalf("resumed run = %+v, want the same scheduled run done", d.Run)
		}
		ok := 0
		for _, tc := range d.ToolCalls {
			if tc.Tool == "fs_write" && tc.Status == core.TCOK {
				ok++
			}
		}
		if ok != 1 {
			t.Fatalf("tool calls of the resumed run = %+v, want fs_write executed exactly once", d.ToolCalls)
		}
		if resp := owner.Do(t, http.MethodGet, "/api/workspace/files?path=parked.txt", nil, nil); resp.StatusCode != http.StatusOK || string(resp.Body) != "written once\n" {
			t.Fatalf("parked.txt = %s %q", resp.Status, resp.Body)
		}
		owner.WaitAudit(t, "tool.exec", func(e core.AuditEntry) bool { return e.Run == fired.Run })
		time.Sleep(600 * time.Millisecond) // two audit flushes: a second execution would show by now
		execs := 0
		for _, e := range owner.Audit(t, "tool.exec") {
			if e.Run == fired.Run {
				execs++
			}
		}
		if execs != 1 {
			t.Fatalf("tool.exec audit rows for the resumed run = %d, want 1", execs)
		}
		if !slices.ContainsFunc(llm.Requests(), func(b []byte) bool { return strings.Contains(string(b), `<approved-action tool=\"fs_write\"`) }) {
			t.Fatalf("the approved action's outcome never reached the model")
		}
	})
}

// TestGracefulRestartTellsChats: a SIGTERM (or the admin Restart button)
// drains the runner while a Telegram run waits on the model and a second
// message is queued behind it. Both runs end before the next boot, so boot
// recovery has nothing to report: the chat must hear about both during the
// drain, and the queued message must still be in the session afterwards.
func TestGracefulRestartTellsChats(t *testing.T) {
	const tgUser = 5002
	llm := pltLLM(t,
		mockllm.Rule{MatchUser: `GRACEFUL-SLOW`, Fault: mockllm.FaultSlowTTFT, Delay: time.Minute, Text: []string{"too late"}},
	)
	tg := pltNewTelegram()
	cfg := pltYAML(llm.URL(), nil, `
agent:
  busy_mode: queue
`)
	g := Start(t, Options{Config: cfg, Platforms: []Platform{tg}})
	owner := pltOwner(t, g)
	tg.LetIn(t, owner, tgUser, true)

	tg.Inject(telegramtest.Inbound{ChatID: tgUser, SenderID: tgUser, Text: "GRACEFUL-SLOW tell me a long story"})
	deadline := time.Now().Add(15 * time.Second)
	for !slices.ContainsFunc(llm.Requests(), func(b []byte) bool { return strings.Contains(string(b), "GRACEFUL-SLOW") }) {
		if time.Now().After(deadline) {
			t.Fatalf("the Telegram run never reached the model\nlogs:\n%s", g.Logs())
		}
		time.Sleep(25 * time.Millisecond)
	}
	tg.Inject(telegramtest.Inbound{ChatID: tgUser, SenderID: tgUser, Text: "QUEUED-SECOND and then summarize it"})
	tg.WaitText(t, tgUser, "Queued", 10*time.Second)

	_ = g.Stop() // drains for 10 s, then interrupts the run
	tg.WaitText(t, tgUser, "restarted before it finished this task", 5*time.Second)
	tg.WaitText(t, tgUser, "restarted before it got to this message", 5*time.Second)

	// Boot recovery has nothing more to tell the chat.
	_ = Start(t, Options{DataDir: g.DataDir, Config: cfg, Platforms: []Platform{tg}})
	time.Sleep(time.Second)
	n := 0
	for _, s := range tg.AllTexts(tgUser) {
		if strings.Contains(s, "restarted before it finished this task") {
			n++
		}
	}
	if n == 0 || n > 2 {
		t.Fatalf("the chat heard the restart %d times: %q", n, tg.AllTexts(tgUser))
	}
}
