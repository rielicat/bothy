package agent

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	titlePrompt    = "Write a title of at most six words for this conversation. Reply with the title only."
	titleMaxTokens = 300
	titleTimeout   = time.Minute
)

var errTitled = errors.New("session already has a title")

// titleAsync names a session after its first run (fast slot, metered).
func (r *Runner) titleAsync(rs *runState) {
	if r.paused.Load() || r.closing.Load() {
		return
	}
	sid, p, u := rs.req.Session, rs.req.Principal, rs.req.User
	r.bg.Add(1)
	go func() {
		defer r.bg.Done()
		ctx, cancel := context.WithTimeout(r.bgCtx, titleTimeout)
		defer cancel()
		r.makeTitle(ctx, sid, p, u)
	}()
}

func (r *Runner) makeTitle(ctx context.Context, sid core.SessionID, p core.PrincipalID, u core.UserID) {
	if r.d.Budget.CheckDaily(ctx, u) != nil {
		return
	}
	msgs, err := r.d.Store.Messages().Range(ctx, core.SystemViewer(), sid, 0, 0)
	if err != nil {
		return
	}
	var ask, reply string
	for _, m := range msgs {
		switch {
		case ask == "" && m.Role == core.MsgUser && m.Kind == core.KindNormal && m.Provenance != core.ProvSystem:
			ask = truncateRunes(r.redact(plainText(m.Parts)), 2000)
			if m.Provenance == core.ProvUntrusted {
				ask = wrapUntrusted(randomID(), ask, "source", "message")
			}
		case ask != "" && reply == "" && m.Role == core.MsgAssistant && m.Kind == core.KindNormal:
			reply = truncateRunes(r.redact(plainText(m.Parts)), 1000)
		}
	}
	if strings.TrimSpace(ask) == "" {
		return
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
		if serr := r.d.Budget.RecordSpend(context.WithoutCancel(ctx), u, ci); serr != nil {
			r.log.Warn("record title spend", "err", serr)
		}
	}
	if err != nil {
		r.log.Debug("title generation failed", "session", sid, "err", err)
		return
	}
	title := cleanTitle(resp.Message.PlainText())
	if title == "" {
		return
	}
	s, err := r.mutateSession(ctx, sid, func(s *core.Session) error {
		if s.Title != "" {
			return errTitled
		}
		s.Title = title
		return nil
	})
	if err != nil {
		return
	}
	r.d.Bus.Publish(core.Event{Type: core.EvSessionUpdated, Principal: p, Session: sid,
		Data: core.SessionData{Title: s.Title, Taint: s.Taint, Epoch: s.Epoch}})
}

// cleanTitle keeps a single short line without quotes or markdown.
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimLeft(s, "#*-> ")
	s = strings.Trim(s, "\"'`*_“”‘’ ")
	s = strings.TrimRight(s, ".:;")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = strings.TrimSpace(string(r[:80])) + "…"
	}
	return s
}
