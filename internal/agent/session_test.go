package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/fakes"
)

// TestConstitutionPinned fails when an embedded prompt changes without a
// constitutionVersion bump.
func TestConstitutionPinned(t *testing.T) {
	sum := sha256.Sum256([]byte(constitutionFull + "\x00" + constitutionShort))
	got := hex.EncodeToString(sum[:8])
	if pinnedConstitutions[constitutionVersion] != got {
		t.Fatalf("CONSTITUTION files changed: bump constitutionVersion and pin %q: %q", constitutionVersion, got)
	}
	r := &Runner{}
	v, text := r.Constitution()
	if v != constitutionVersion || text != constitutionFull {
		t.Fatal("Constitution() does not return the full variant")
	}
	if len(constitutionShort) > 400*36/10 {
		t.Fatalf("short constitution is %d bytes (> ~400 tokens)", len(constitutionShort))
	}
	if len(constitutionFull) > 1000*36/10 {
		t.Fatalf("full constitution is %d bytes (> ~1000 tokens)", len(constitutionFull))
	}
	for _, must := range []string{"<untrusted", "tool_search", "use_tool", "approval", "secrets", "Standing instructions"} {
		if !strings.Contains(constitutionFull, must) {
			t.Errorf("full constitution lacks %q", must)
		}
	}
}

// pinnedConstitutions maps each constitutionVersion to the hash of its texts.
var pinnedConstitutions = map[string]string{"1": "b334c1706f89912e"}

func TestNewWritesDefaultPersonaOnce(t *testing.T) {
	data := t.TempDir()
	deps := func() Deps {
		return Deps{Store: nilStore{}, Config: config.Static(config.Default()), LLM: &fakes.LLM{}, Tools: &fakes.Registry{},
			Policy: &fakes.Policy{}, Approvals: &fakes.Approvals{}, Budget: &fakes.Budget{}, Bus: nilBus{}, DataDir: data}
	}
	if _, err := New(deps()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "SOUL.md")
	b, err := os.ReadFile(path)
	if err != nil || string(b) != defaultSoul {
		t.Fatalf("SOUL.md = %q %v", b, err)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("SOUL.md mode %v", st.Mode())
	}
	if err := os.WriteFile(path, []byte("custom persona"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := New(deps())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "custom persona" || r.persona() != "custom persona" {
		t.Fatal("existing persona overwritten or not read")
	}
	if _, err := New(Deps{}); err == nil {
		t.Fatal("New without dependencies succeeded")
	}
}

type nilStore struct{ core.Store }
type nilBus struct{ core.Bus }

func TestSessionsResolveForkAndNew(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{cfg: func(c *config.Config) { c.Agent.SessionIdleReset = config.Duration(time.Hour) },
			steps: []fakes.LLMStep{{Text: []string{"ok"}}}})
		defer h.close()
		chat := core.ChatRef{Channel: "telegram", ChatID: "42", Kind: core.ChatDM}
		s1, err := h.r.ResolveSession(h.ctx, h.pid, chat)
		if err != nil || s1.ChannelKey != chat.Key() || s1.Origin != "telegram" {
			t.Fatalf("resolve = %+v %v", s1, err)
		}
		s2, _ := h.r.ResolveSession(h.ctx, h.pid, chat)
		if s2.ID != s1.ID {
			t.Fatal("second resolve created a new session")
		}
		if _, err := h.r.mutateSession(h.ctx, s1.ID, func(s *core.Session) error { s.LastMessageAt = time.Now().Add(-2 * time.Hour); return nil }); err != nil {
			t.Fatal(err)
		}
		s3, _ := h.r.ResolveSession(h.ctx, h.pid, chat)
		if s3.ID == s1.ID {
			t.Fatal("idle reset did not start a fresh session")
		}
		if _, err := h.r.ResolveSession(h.ctx, h.pid, core.ChatRef{Channel: "irc"}); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("unknown channel = %v", err)
		}

		if err := h.store.Sessions().SetTaint(h.ctx, s3.ID, core.TaintPrivate|core.TaintUntrusted); err != nil {
			t.Fatal(err)
		}
		v := ownerViewer(h)
		fork, err := h.r.Fork(h.ctx, v, s3.ID, h.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		old, _ := h.store.Sessions().Get(h.ctx, core.SystemViewer(), s3.ID)
		if !old.Archived || fork.Taint != 0 || fork.ChannelKey != chat.Key() {
			t.Fatalf("fork = %+v, old archived %v", fork, old.Archived)
		}
		if got, _ := h.r.ResolveSession(h.ctx, h.pid, chat); got.ID != fork.ID {
			t.Fatal("the chat did not move to the fork")
		}
		msgs, _ := h.store.Messages().Range(h.ctx, core.SystemViewer(), fork.ID, 0, 0)
		if len(msgs) != 1 || msgs[0].Kind != core.KindNotice || strings.Contains(msgs[0].PlainText(), string(s3.ID)) ||
			!strings.HasPrefix(msgs[0].PlainText(), "This is a clean chat continued from ") {
			t.Fatalf("fork note = %+v", msgs)
		}
		if _, err := h.r.Fork(h.ctx, core.Viewer{User: "usr_x"}, s3.ID, "usr_x"); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("foreign fork = %v", err)
		}

		api, err := h.r.NewSession(h.ctx, h.pid, "api", nil)
		if err != nil || !api.Hidden || api.ModelSlot != core.SlotMain {
			t.Fatalf("api session = %+v %v", api, err)
		}
		if _, err := h.r.NewSession(h.ctx, h.pid, "fax", nil); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("bad origin = %v", err)
		}
	})
}

func TestSubmitValidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{})
		defer h.close()
		req := h.req("x")
		req.Principal = "spc_other"
		if _, err := h.r.Submit(h.ctx, req); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("foreign principal = %v", err)
		}
		req = h.req("x")
		req.User = "usr_someoneelse"
		if _, err := h.r.Submit(h.ctx, req); !errors.Is(err, core.ErrForbidden) {
			t.Fatalf("foreign user = %v", err)
		}
		req = h.req("x")
		req.Trigger = core.TrigResume
		if _, err := h.r.Submit(h.ctx, req); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("resume trigger = %v", err)
		}
		req = h.req("")
		req.Input = nil
		if _, err := h.r.Submit(h.ctx, req); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("empty input = %v", err)
		}
	})
}

func TestTitleGeneratedAfterFirstRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"Sure, let's plan."}}}})
		defer h.close()
		s, err := h.r.NewSession(h.ctx, h.pid, "web", nil)
		if err != nil {
			t.Fatal(err)
		}
		h.sess = s
		sub := h.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvSessionUpdated}}, 64)
		defer sub.Close()
		h.submit("Help me plan the weekend\n\nwith  the whole family, somewhere near the coast")
		synctest.Wait()
		if got := h.session().Title; got != "Weekend plans" {
			t.Fatalf("title = %q", got)
		}
		var titles []string
		for len(sub.C()) > 0 {
			titles = append(titles, (<-sub.C()).Data.(core.SessionData).Title)
		}
		if want := []string{"Help me plan the weekend with the whole family,", "Weekend plans"}; !slices.Equal(titles, want) {
			t.Fatalf("session titles %q, want the first message's start, then the generated title %q", titles, want)
		}
		var titled bool
		for _, c := range h.budget.(*fakes.Budget).Calls {
			titled = titled || c.Purpose == "title"
		}
		if !titled {
			t.Fatal("title call not metered")
		}
	})
}

func TestBothysOwnTurnsNameNoChat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"Nothing needs attention."}}}})
		defer h.close()
		s, err := h.r.NewSession(h.ctx, h.pid, "schedule", nil)
		if err != nil {
			t.Fatal(err)
		}
		h.sess = s
		req := h.req("Read HEARTBEAT.md and check the list.")
		req.Provenance = core.ProvSystem
		if _, err := h.r.Submit(h.ctx, req); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := h.session().Title; got != "" {
			t.Fatalf("a check-in named its chat %q", got)
		}
	})
}

func TestTitleKeepsARename(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{steps: []fakes.LLMStep{{Text: []string{"Sure."}}, {Text: []string{"Again."}}}})
		defer h.close()
		s, err := h.r.NewSession(h.ctx, h.pid, "web", nil)
		if err != nil {
			t.Fatal(err)
		}
		s.Title = "Renamed"
		if err := h.store.Sessions().Update(h.ctx, s); err != nil {
			t.Fatal(err)
		}
		h.sess = s
		h.submit("Help me plan the weekend")
		synctest.Wait()
		if got := h.session().Title; got != "Renamed" {
			t.Fatalf("title = %q, want the name the person chose", got)
		}
		h.llm.mu.Lock()
		n := h.llm.titles
		h.llm.mu.Unlock()
		if n != 0 {
			t.Fatalf("%d title calls for a named chat", n)
		}
	})
}

func TestContextPreviewSumsToTotal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{tools: []core.Tool{&testTool{name: "fs_read"}}, steps: []fakes.LLMStep{{Text: []string{"ok"}}}})
		defer h.close()
		h.submit("hello there")
		synctest.Wait()
		bd, err := h.r.ContextPreview(h.ctx, ownerViewer(h), h.sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		sum := bd.Constitution + bd.Persona + bd.Memory + bd.Skills + bd.Pins + bd.Tools + bd.Summary + bd.History + bd.ToolResults + bd.TurnContext
		if sum != bd.Total || bd.Window != fakes.DefaultModel.Context || bd.Reserve != 8192 || bd.Tools == 0 || bd.Constitution == 0 {
			t.Fatalf("breakdown = %+v", bd)
		}
		if _, err := h.r.ContextPreview(h.ctx, core.Viewer{User: "usr_x"}, h.sess.ID); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("foreign preview = %v", err)
		}
	})
}

func TestBootRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, opts{})
		chat := &core.ChatRef{Channel: "telegram", ChatID: "1", Kind: core.ChatDM}
		h.sess = h.newSession("telegram", chat)
		run := &core.Run{Session: h.sess.ID, Principal: h.pid, User: h.user.ID, Trigger: core.TrigMessage, Status: core.RunRunning, CreatedAt: time.Now()}
		if err := h.store.Runs().Create(h.ctx, run); err != nil {
			t.Fatal(err)
		}
		h.appr.Manual = true
		if _, err := h.appr.Request(h.ctx, core.ApprovalRequest{Run: run.ID, Session: h.sess.ID, Principal: h.pid, Action: &core.Action{Tool: "x"}}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error)
		go func() { done <- h.r.Run(ctx) }()
		synctest.Wait()
		if st := h.run(run.ID).Status; st != core.RunInterrupted {
			t.Fatalf("status = %s", st)
		}
		if sent := h.hub.Messages(); len(sent) != 1 || sent[0] != chat.Key()+"|"+interruptedNotice {
			t.Fatalf("hub = %v", sent)
		}
		if st := h.appr.All()[0].Status; st != core.ApprovalCancelled {
			t.Fatalf("approval of the interrupted run = %s", st)
		}
		if v, _ := h.store.Meta().Get(h.ctx, metaConstitution); v != constitutionVersion {
			t.Fatalf("meta.constitution_version = %q", v)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
