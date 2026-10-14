package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/rielicat/bothy/internal/core"
)

// onceHook offers its block until a turn carrying it is committed, the way
// the knowledge notes hook does.
type onceHook struct {
	mu        sync.Mutex
	text      string
	taint     core.Taint
	fail      bool
	delivered bool
	commits   int
	inputs    []core.TurnContextInput
}

func (h *onceHook) TurnContext(_ context.Context, in core.TurnContextInput) (core.TurnContextBlock, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inputs = append(h.inputs, in)
	if h.fail {
		return core.TurnContextBlock{}, errors.New("hook broke")
	}
	if h.delivered {
		return core.TurnContextBlock{}, nil
	}
	return core.TurnContextBlock{Text: h.text, Taint: h.taint, Commit: func(context.Context) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.delivered = true
		h.commits++
		return nil
	}}, nil
}

func messageJSON(t *testing.T, m core.Message) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTurnContextHookBlockIsPersistedOnceAndKeepsThePrefix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: textSteps(3, "reply")})
		defer h.close()
		hook := &onceHook{text: "project notes for acme/api:\nBuild with make.", taint: core.TaintUntrusted}
		h.r = h.newRunnerWith(func(d *Deps) { d.TurnContext = []core.TurnContextHook{hook} })
		h.r.SetHub(h.hub)
		for i, text := range []string{"first", "second", "third"} {
			res := h.submit(text)
			synctest.Wait()
			if st := h.run(res.Run).Status; st != core.RunDone {
				t.Fatalf("turn %d: %s", i+1, st)
			}
		}
		reqs := h.requests()
		if len(reqs) != 3 {
			t.Fatalf("requests = %d", len(reqs))
		}
		if !strings.Contains(lastUserText(reqs[0]), "Build with make.") {
			t.Fatalf("hook block missing from the first turn-context:\n%s", lastUserText(reqs[0]))
		}
		for i, req := range reqs[1:] {
			if strings.Contains(lastUserText(req), "Build with make.") {
				t.Fatalf("turn %d repeated a delivered block", i+2)
			}
			if got, want := messageJSON(t, req.Messages[0]), messageJSON(t, reqs[0].Messages[0]); got != want {
				t.Fatalf("turn %d changed the persisted first turn:\n%s\n%s", i+2, got, want)
			}
		}
		hashes := h.prefixHashes()
		for _, x := range hashes {
			if x != hashes[0] {
				t.Fatalf("prefix hashes differ: %v", hashes)
			}
		}
		if hook.commits != 1 {
			t.Fatalf("commits = %d, want 1", hook.commits)
		}
		if !h.session().Taint.Has(core.TaintUntrusted) {
			t.Fatal("the block's taint did not reach the session")
		}
		in := hook.inputs[0]
		if in.Session != h.sess.ID || in.Principal.ID != h.pid || in.Epoch != h.sess.Epoch {
			t.Fatalf("hook input = %+v", in)
		}
	})
}

func TestTurnContextHookFailureDoesNotStopTheTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: textSteps(1, "reply")})
		defer h.close()
		hook := &onceHook{text: "never shown", fail: true}
		h.r = h.newRunnerWith(func(d *Deps) { d.TurnContext = []core.TurnContextHook{hook} })
		h.r.SetHub(h.hub)
		res := h.submit("hello")
		synctest.Wait()
		if st := h.run(res.Run).Status; st != core.RunDone {
			t.Fatalf("run = %s", st)
		}
		if got := lastUserText(h.requests()[0]); strings.Contains(got, "never shown") || !strings.Contains(got, "<turn-context>") {
			t.Fatalf("turn-context = %q", got)
		}
		if h.session().Taint != 0 {
			t.Fatalf("taint = %v", h.session().Taint)
		}
	})
}
