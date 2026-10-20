package agent

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/rielicat/bothy/internal/core"
)

const (
	titlePrompt = "Write a title of at most six words for this conversation, in plain words and sentence case, " +
		"with no quotes, emoji or full stop. Reply with the title only."
	titleMaxTokens = 300
	titleTimeout   = time.Minute
	// titleScan is how many of a session's first messages are read to find
	// the one that names it and the reply to it.
	titleScan = 50
)

var errTitled = errors.New("session already has a title")

// firstAsk is the message that names a session: the first one a person wrote
// with some text in it.
func firstAsk(msgs []*core.Message) *core.Message {
	for _, m := range msgs {
		if m.Role == core.MsgUser && m.Kind == core.KindNormal && m.Provenance != core.ProvSystem && strings.TrimSpace(m.PlainText()) != "" {
			return m
		}
	}
	return nil
}

// draftTitle names an untitled session after its first message, the turn
// just stored included, until the generated title arrives. Bothy's own turns
// (check-ins, approved actions) name nothing.
func (r *Runner) draftTitle(ctx context.Context, sid core.SessionID) string {
	msgs, err := r.d.Store.Messages().Range(ctx, core.SystemViewer(), sid, 0, titleScan)
	if err != nil {
		return ""
	}
	m := firstAsk(msgs)
	if m == nil {
		return ""
	}
	return core.DraftTitle(r.redact(m.PlainText()))
}

func (r *Runner) setDraft(sid core.SessionID, title string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if title == "" {
		delete(r.drafts, sid)
		return
	}
	r.drafts[sid] = title
}

// isDraft reports whether the session's title is still the draft a task gave
// it, waiting for the generated one.
func (r *Runner) isDraft(s *core.Session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.drafts[s.ID]
	return ok && d == s.Title
}

// titleAsync names a session after its first run (fast slot, metered).
func (r *Runner) titleAsync(rs *runState) {
	if r.paused.Load() || r.closing.Load() {
		return
	}
	sid, p := rs.req.Session, rs.req.Principal
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		ctx, cancel := context.WithTimeout(r.bgCtx, titleTimeout)
		defer cancel()
		r.makeTitle(ctx, sid, p)
	}()
}

func (r *Runner) makeTitle(ctx context.Context, sid core.SessionID, p core.PrincipalID) {
	if r.d.Budget.CheckDaily(ctx, p) != nil {
		return
	}
	msgs, err := r.d.Store.Messages().Range(ctx, core.SystemViewer(), sid, 0, titleScan)
	if err != nil {
		return
	}
	first := firstAsk(msgs)
	if first == nil {
		return
	}
	draft := core.DraftTitle(r.redact(first.PlainText()))
	ask := truncateRunes(r.redact(first.PlainText()), 2000)
	if first.Provenance == core.ProvUntrusted {
		ask = wrapUntrusted(randomID(), ask, "source", "message")
	}
	var reply string
	for _, m := range msgs {
		if m.Seq > first.Seq && m.Role == core.MsgAssistant && m.Kind == core.KindNormal && m.PlainText() != "" {
			reply = truncateRunes(r.redact(m.PlainText()), 1000)
			break
		}
	}
	slot := core.SlotFast
	if len(r.d.LLM.Chain(core.SlotFast, nil)) == 0 {
		slot = core.SlotMain
	}
	build := func(m core.ModelInfo) (*core.Request, error) {
		out := titleMaxTokens
		if m.MaxOutput > 0 {
			out = min(out, m.MaxOutput)
		}
		text := "User: " + ask
		if reply != "" {
			text += "\n\nAssistant: " + reply
		}
		return &core.Request{Model: m.Ref.Model, System: []core.SystemBlock{{Text: titlePrompt}},
			Messages:  []core.Message{{Role: core.MsgUser, Parts: []core.Part{{Type: core.PartText, Text: text}}}},
			MaxTokens: out, NumCtx: m.Context}, nil
	}
	resp, ci, err := r.d.LLM.Complete(ctx, slot, nil, build, nil)
	if ci.Ref.Model != "" || ci.Cost > 0 {
		ci.Purpose = "title"
		if serr := r.d.Budget.RecordSpend(context.WithoutCancel(ctx), p, ci); serr != nil {
			r.log.Warn("record title spend", "err", serr)
		}
	}
	if err != nil || resp == nil {
		r.log.Debug("title generation failed", "session", sid, "err", err)
		return
	}
	title := sentenceCase(cleanTitle(resp.Message.PlainText()), first.PlainText())
	if title == "" {
		return
	}
	s, err := r.mutateSession(ctx, sid, func(s *core.Session) error {
		if s.Title != "" && s.Title != draft {
			return errTitled
		}
		s.Title = title
		return nil
	})
	if err == nil || errors.Is(err, errTitled) {
		r.setDraft(sid, "")
	}
	if err != nil {
		return
	}
	r.d.Bus.Publish(core.Event{Type: core.EvSessionUpdated, Principal: p, Session: sid,
		Data: core.SessionData{Title: s.Title, Taint: s.Taint, Epoch: s.Epoch}})
}

// cleanTitle keeps a single short line without quotes, markdown or emoji.
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Map(func(r rune) rune {
		// Backticks and emphasis marks inside the title show up literally
		// in the sidebar ("Open redirect via unvalidated `next` param").
		if r == '`' || r == '*' {
			return -1
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.So, r) || unicode.Is(unicode.Variation_Selector, r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimLeft(s, "#*-> ")
	s = strings.Trim(s, "\"'`*_“”‘’ ")
	s = strings.TrimRight(s, ".:;! ")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = strings.TrimSpace(string(r[:80])) + "…"
	}
	return s
}

// smallWords stay lower-case in Title Case too, so they don't tell it apart.
var smallWords = map[string]bool{"a": true, "an": true, "and": true, "as": true, "at": true, "but": true, "by": true, "for": true,
	"from": true, "in": true, "into": true, "of": true, "on": true, "or": true, "the": true, "to": true, "vs": true, "with": true}

// sentenceCase undoes the Title Case models write despite the prompt ("Open
// Pull Requests in acme/api"). Only when every plain word after the first
// is capitalised are they lower-cased, and a word the person wrote
// capitalised themselves (a name) keeps its capital; acronyms and
// mixed-case words (API, GitHub) are never touched.
func sentenceCase(title, ask string) string {
	words := strings.Fields(title)
	bare := func(w string) string { return strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) }) }
	capital := func(w string) bool {
		r := []rune(w)
		return len(r) > 1 && unicode.IsUpper(r[0]) && strings.ToLower(string(r[1:])) == string(r[1:]) && strings.IndexFunc(w, func(r rune) bool { return !unicode.IsLetter(r) }) < 0
	}
	asked := map[string]bool{}
	for _, w := range strings.Fields(ask) {
		asked[bare(w)] = true
	}
	caps := 0
	for _, w := range words[min(1, len(words)):] {
		switch c := bare(w); {
		case capital(c):
			caps++
		case c != "" && c == strings.ToLower(c) && !smallWords[c] && strings.IndexFunc(c, func(r rune) bool { return !unicode.IsLetter(r) }) < 0:
			return title
		}
	}
	if caps < 2 {
		return title
	}
	for i := 1; i < len(words); i++ {
		if c := bare(words[i]); capital(c) && !asked[c] {
			words[i] = strings.Replace(words[i], c, strings.ToLower(c), 1)
		}
	}
	return strings.Join(words, " ")
}
