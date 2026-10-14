package refine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

// script answers calls with a function and records them; it also tracks how
// many calls were in flight at once.
type script struct {
	mu          sync.Mutex
	fn          func(c Call, n int) (Reply, error)
	calls       []Call
	inFlight    int
	maxInFlight int
	wait        time.Duration // held inside the call (in a synctest bubble)
}

func (s *script) Call(ctx context.Context, c Call) (Reply, error) {
	s.mu.Lock()
	s.calls = append(s.calls, c)
	n := len(s.calls)
	s.inFlight++
	s.maxInFlight = max(s.maxInFlight, s.inFlight)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()
	if s.wait > 0 {
		time.Sleep(s.wait)
	}
	rep, err := s.fn(c, n)
	if rep.CallID == "" {
		rep.CallID = fmt.Sprintf("llm_%d", n)
	}
	if rep.Cost == 0 {
		rep.Cost = 10
	}
	return rep, err
}

func (s *script) roles() map[core.RefineRole]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[core.RefineRole]int{}
	for _, c := range s.calls {
		out[c.Role]++
	}
	return out
}

// arithmetic is a model that drafts per a table, keeps a continued text as
// it is (adding the final answer line when missing), confirms 42 and
// corrects anything else to 42.
func arithmetic(drafts map[int]string) func(c Call, n int) (Reply, error) {
	return func(c Call, _ int) (Reply, error) {
		switch c.Role {
		case core.RefineDraft:
			return Reply{Text: drafts[c.Draft]}, nil
		case core.RefineContinue:
			if Extract(c.Trace) == "" {
				return Reply{Text: c.Trace + "\nFinal answer: 42"}, nil
			}
			return Reply{Text: c.Trace}, nil
		case core.RefineCritique:
			if Normalize(Extract(c.Trace)) == "42" {
				return Reply{Text: "Verdict: correct\nThe product is right."}, nil
			}
			return Reply{Text: "Verdict: needs changes\n6 × 7 is 42."}, nil
		}
		return Reply{Text: "Six times seven is 42.\nFinal answer: 42"}, nil
	}
}

var first = Reply{Text: "Six times seven is 42.", CallID: "llm_first", Model: "fake/m", Cost: 25}

func TestRunEarlyStopWhenEveryDraftAgrees(t *testing.T) {
	s := &script{fn: arithmetic(map[int]string{1: "It is 41.\nFinal answer: 41", 2: "Final answer: 42.0"})}
	var seen []core.RefineEntry
	out := Run(context.Background(), Settings{Drafts: 3, Depth: 2, EarlyStop: true, Parallel: 1}, first, s,
		func(e core.RefineEntry) { seen = append(seen, e) })
	tr := out.Trace
	// 1 first draft + 2 drafts + round 1: 3 continues, 3 critiques, 1 correction.
	if tr.Calls != 10 || len(s.calls) != 9 || len(seen) != 10 {
		t.Fatalf("calls: trace %d, caller %d, entries seen %d; want 10, 9, 10", tr.Calls, len(s.calls), len(seen))
	}
	if !tr.EarlyStop || tr.Rounds != 1 || tr.Fallback != "" || tr.Winner != 0 || tr.Answer != "42" {
		t.Fatalf("trace %+v", tr)
	}
	if len(tr.Votes) != 1 || tr.Votes[0].Count != 3 || tr.Votes[0].Answer != "42" {
		t.Fatalf("votes %+v", tr.Votes)
	}
	if out.Text != "Six times seven is 42.\nFinal answer: 42" {
		t.Fatalf("text %q", out.Text)
	}
	if tr.Cost != 25+9*10 {
		t.Fatalf("cost %d", tr.Cost)
	}
	if e := tr.Entries[0]; e.Role != core.RefineDraft || e.Draft != 0 || e.CallID != "llm_first" || e.Model != "fake/m" {
		t.Fatalf("first entry %+v", e)
	}
	var critiques, confirmed int
	for _, e := range tr.Entries {
		if e.Role == core.RefineCritique {
			critiques++
			if e.Confirmed {
				confirmed++
			}
		}
	}
	if critiques != 3 || confirmed != 2 {
		t.Fatalf("critiques %d confirmed %d", critiques, confirmed)
	}
	for i := 1; i < len(tr.Entries); i++ {
		a, b := tr.Entries[i-1], tr.Entries[i]
		if a.Round > b.Round || (a.Round == b.Round && a.Draft > b.Draft) {
			t.Fatalf("entries out of order at %d: %+v then %+v", i, a, b)
		}
	}
}

func TestRunWithoutEarlyStopRunsEveryRound(t *testing.T) {
	s := &script{fn: arithmetic(map[int]string{1: "Final answer: 41", 2: "Final answer: 42"})}
	out := Run(context.Background(), Settings{Drafts: 3, Depth: 2}, first, s, nil)
	tr := out.Trace
	// Round 2 confirms every draft, so it needs no correction.
	if tr.Rounds != 2 || tr.EarlyStop || tr.Calls != 16 || tr.Calls > core.RefineMaxCalls(3, 2) {
		t.Fatalf("trace %+v", tr)
	}
	if got := s.roles(); got[core.RefineDraft] != 2 || got[core.RefineContinue] != 6 || got[core.RefineCritique] != 6 || got[core.RefineCorrect] != 1 {
		t.Fatalf("roles %v", got)
	}
}

func TestRunNeverExceedsTheCallBoundAndRunsInParallel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &script{wait: time.Second, fn: func(c Call, n int) (Reply, error) {
			switch c.Role {
			case core.RefineCritique:
				return Reply{Text: "Verdict: needs changes"}, nil
			}
			return Reply{Text: fmt.Sprintf("Attempt %d.\nFinal answer: %d", n, n)}, nil
		}}
		out := Run(context.Background(), Settings{Drafts: 4, Depth: 2, EarlyStop: true, Parallel: 2}, first, s, nil)
		if want := core.RefineMaxCalls(4, 2); out.Trace.Calls != want {
			t.Fatalf("calls %d, want the bound %d", out.Trace.Calls, want)
		}
		if s.maxInFlight != 2 {
			t.Fatalf("max in flight %d, want 2", s.maxInFlight)
		}
		if out.Trace.Rounds != 2 || out.Trace.Fallback != "" {
			t.Fatalf("trace %+v", out.Trace)
		}
	})
}

func TestRunHaltFallsBackToTheBestAnswerSoFar(t *testing.T) {
	s := &script{}
	s.fn = func(c Call, n int) (Reply, error) {
		if n > 5 {
			return Reply{}, &Halt{Reason: FallbackBudget}
		}
		switch c.Role {
		case core.RefineDraft:
			return Reply{Text: "Final answer: 7"}, nil
		case core.RefineCritique:
			return Reply{Text: "Verdict: needs changes"}, nil
		case core.RefineCorrect:
			return Reply{Text: "Corrected.\nFinal answer: 7"}, nil
		}
		return Reply{Text: c.Trace + "\nFinal answer: 1"}, nil
	}
	first := Reply{Text: "I think 1.", CallID: "llm_first"}
	out := Run(context.Background(), Settings{Drafts: 3, Depth: 2, EarlyStop: true, Parallel: 1}, first, s, nil)
	tr := out.Trace
	// Drafts 1 and 2, then draft 0's whole round; draft 1's continue is refused.
	if len(s.calls) != 6 || tr.Calls != 6 {
		t.Fatalf("caller saw %d calls, trace counts %d", len(s.calls), tr.Calls)
	}
	if tr.Fallback != FallbackBudget || tr.Rounds != 0 || tr.EarlyStop {
		t.Fatalf("trace %+v", tr)
	}
	if tr.Winner != 0 || out.Text != "Corrected.\nFinal answer: 7" || tr.Votes[0].Count != 3 {
		t.Fatalf("winner %d %q votes %+v", tr.Winner, out.Text, tr.Votes)
	}
}

func TestRunFailedCallKeepsTheDraftsLastText(t *testing.T) {
	boom := errors.New("provider overloaded")
	s := &script{fn: func(c Call, n int) (Reply, error) {
		if c.Role == core.RefineContinue && c.Draft == 1 {
			return Reply{CallID: "llm_bad", Cost: 3}, boom
		}
		return arithmetic(map[int]string{1: "Final answer: 41", 2: "Final answer: 41"})(c, n)
	}}
	out := Run(context.Background(), Settings{Drafts: 3, Depth: 1, EarlyStop: true, Parallel: 1}, first, s, nil)
	tr := out.Trace
	if tr.Rounds != 1 || tr.Fallback != "" {
		t.Fatalf("trace %+v", tr)
	}
	var failed *core.RefineEntry
	for i, e := range tr.Entries {
		if e.Error != "" {
			failed = &tr.Entries[i]
		}
	}
	if failed == nil || failed.Draft != 1 || failed.Error != boom.Error() || failed.CallID != "llm_bad" || failed.Cost != 3 {
		t.Fatalf("failed entry %+v", failed)
	}
	// Draft 1 kept 41; drafts 0 and 2 ended on 42.
	if tr.Winner != 0 || tr.Votes[0].Answer != "42" || tr.Votes[0].Count != 2 || tr.Votes[1].Answer != "41" {
		t.Fatalf("votes %+v", tr.Votes)
	}
}

func TestRunEveryDraftFailing(t *testing.T) {
	s := &script{fn: func(c Call, _ int) (Reply, error) {
		if c.Role == core.RefineDraft {
			return Reply{Text: ""}, nil
		}
		return Reply{}, errors.New("down")
	}}
	out := Run(context.Background(), Settings{Drafts: 2, Depth: 2, Parallel: 1}, first, s, nil)
	if out.Trace.Fallback != FallbackFailed || out.Trace.Rounds != 0 || out.Text != first.Text || out.Trace.Winner != 0 {
		t.Fatalf("outcome %+v", out)
	}
	if e := out.Trace.Entries[1]; e.Draft != 1 || e.Error == "" {
		t.Fatalf("an empty draft is recorded as an error: %+v", e)
	}
}

func TestRunStopsWhenTheUserWritesAgain(t *testing.T) {
	s := &script{fn: arithmetic(map[int]string{1: "Final answer: 41"})}
	out := Run(context.Background(), Settings{Drafts: 2, Depth: 2, Proceed: func() bool { return false }}, first, s, nil)
	if out.Trace.Fallback != FallbackNewMessage || out.Trace.Calls != 2 || out.Trace.Rounds != 0 {
		t.Fatalf("trace %+v", out.Trace)
	}
}

func TestRunStoppedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &script{fn: func(c Call, n int) (Reply, error) {
		cancel()
		return Reply{}, ctx.Err()
	}}
	out := Run(ctx, Settings{Drafts: 3, Depth: 2, Parallel: 1}, first, s, nil)
	if out.Trace.Fallback != FallbackStopped || len(s.calls) != 1 || out.Text != first.Text {
		t.Fatalf("outcome %+v after %d calls", out, len(s.calls))
	}
	if out.Trace.Calls != 2 {
		t.Fatalf("a call that reached the model is counted: %+v", out.Trace)
	}
}

func TestRunSingleDraftRefinesWithoutVoting(t *testing.T) {
	s := &script{fn: arithmetic(nil)}
	out := Run(context.Background(), Settings{Drafts: 1, Depth: 2, EarlyStop: true}, first, s, nil)
	// One draft never "agrees" with itself, so both rounds run.
	if out.Trace.Rounds != 2 || out.Trace.EarlyStop || out.Trace.Calls != 1+2+2 || out.Trace.Answer != "42" {
		t.Fatalf("trace %+v", out.Trace)
	}
}

func TestRunRedactsAndClipsTheTrace(t *testing.T) {
	long := strings.Repeat("filler ", 2000) + "sk-secret\nFinal answer: sk-secret"
	s := &script{fn: func(c Call, _ int) (Reply, error) {
		if c.Role == core.RefineCritique {
			return Reply{Text: "Verdict: correct"}, nil
		}
		return Reply{Text: long}, nil
	}}
	red := func(t string) string { return strings.ReplaceAll(t, "sk-secret", "[redacted]") }
	out := Run(context.Background(), Settings{Drafts: 2, Depth: 1, EarlyStop: true, Redact: red}, first, s, nil)
	for _, e := range out.Trace.Entries {
		if strings.Contains(e.Text+e.Answer, "sk-secret") {
			t.Fatalf("unredacted entry %+v", e)
		}
		if len(e.Text) > 4096+64 {
			t.Fatalf("entry text of %d bytes not clipped", len(e.Text))
		}
	}
	e := out.Trace.Entries[1]
	if !strings.Contains(e.Text, "bytes omitted") || !strings.HasSuffix(e.Text, "Final answer: [redacted]") || e.Answer != "[redacted]" {
		t.Fatalf("clipped entry keeps its head and final answer: %q", e.Text[len(e.Text)-80:])
	}
	if out.Trace.Answer != "[redacted]" || out.Trace.Votes[0].Answer != "[redacted]" {
		t.Fatalf("trace answers %+v", out.Trace)
	}
	if !strings.Contains(out.Text, "sk-secret") {
		t.Fatal("the reply itself is redacted where it is shown, not by the engine")
	}
}

func TestSkip(t *testing.T) {
	out := Skip(Settings{Drafts: 3, Depth: 2}, Reply{Text: "Final answer: 5", CallID: "llm_x", Cost: 4}, FallbackTime)
	tr := out.Trace
	if out.Text != "Final answer: 5" || tr.Fallback != FallbackTime || tr.Calls != 1 || tr.Cost != 4 || tr.Answer != "5" || tr.Winner != 0 {
		t.Fatalf("outcome %+v", out)
	}
}

func TestRunRedactsAnswersBeforeNormalizing(t *testing.T) {
	// Normalizing lowercases; a redactor matching the secret exactly would
	// no longer see it in the normalized answer.
	const secret = "AbCdEf-Token-9"
	s := &script{fn: func(c Call, _ int) (Reply, error) {
		if c.Role == core.RefineCritique {
			return Reply{Text: "Verdict: correct"}, nil
		}
		return Reply{Text: "The key is " + secret + ".\nFinal answer: " + secret}, nil
	}}
	red := func(t string) string { return strings.ReplaceAll(t, secret, "[redacted]") }
	out := Run(context.Background(), Settings{Drafts: 2, Depth: 1, Redact: red, Parallel: 1}, first, s, nil)
	low := strings.ToLower(secret)
	tr := out.Trace
	check := func(where, s string) {
		t.Helper()
		if strings.Contains(strings.ToLower(s), low) {
			t.Fatalf("%s leaks the secret: %q", where, s)
		}
	}
	check("trace answer", tr.Answer)
	for _, v := range tr.Votes {
		check("vote", v.Answer)
	}
	for _, e := range tr.Entries {
		check("entry", e.Text+e.Answer)
	}
	if tr.Answer != "[redacted]" || tr.Votes[0].Answer != "[redacted]" || tr.Votes[0].Count != 2 {
		t.Fatalf("trace %+v", tr)
	}
}

func TestRunClipsLongAnswers(t *testing.T) {
	long := "Final answer: " + strings.Repeat("é and more ", 2000)
	s := &script{fn: func(c Call, _ int) (Reply, error) {
		if c.Role == core.RefineCritique {
			return Reply{Text: "Verdict: correct"}, nil
		}
		return Reply{Text: long}, nil
	}}
	out := Run(context.Background(), Settings{Drafts: 8, Depth: 4, Parallel: 1}, Reply{Text: long}, s, nil)
	tr := out.Trace
	size := len(tr.Answer)
	for _, v := range tr.Votes {
		size += len(v.Answer)
	}
	for _, e := range tr.Entries {
		if len(e.Answer) > answerCap || !utf8.ValidString(e.Answer) {
			t.Fatalf("entry answer of %d bytes", len(e.Answer))
		}
		size += len(e.Text) + len(e.Answer)
	}
	if len(tr.Answer) > answerCap || tr.Votes[0].Answer != tr.Answer || tr.Votes[0].Count != 8 {
		t.Fatalf("trace answer %d bytes, votes %+v", len(tr.Answer), tr.Votes)
	}
	if size > traceBudget+answerCap*2+len(tr.Entries)*64 {
		t.Fatalf("trace keeps %d bytes of text, over its %d budget", size, traceBudget)
	}
	if out.Text != strings.TrimSpace(long) {
		t.Fatal("the reply itself is never clipped")
	}
}
