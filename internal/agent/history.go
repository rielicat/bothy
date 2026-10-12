package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/bothyhq/bothy/internal/core"
)

// notExecuted is the deterministic result synthesized (never persisted) for
// a tool call that has none, so history bytes stay identical across calls.
const notExecuted = "Not executed (run stopped or interrupted)."

const (
	imageMax    = 5 << 20
	imageTokens = 1000 // estimate per image for vision models
)

type region uint8

const (
	rHistory region = iota
	rToolResult
	rTurnContext
	rSummary
)

// histMsg is a model-ready message plus the breakdown region of each part.
type histMsg struct {
	msg     core.Message
	regions []region
	group   int // messages of one turn share a group (hard-fit drops whole groups)
}

// orderToolPairs puts every assistant tool call's result directly after its
// message, in call order, with a synthesized result for any call that has
// none. Results without a matching call are dropped. Messages that landed in
// between (steer turns persisted while tools ran) follow the results.
func orderToolPairs(msgs []*core.Message) []*core.Message {
	results := map[string]*core.Message{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.Type == core.PartToolResult && p.ToolResult != nil && m.Role == core.MsgTool {
				if _, dup := results[p.ToolResult.CallID]; !dup {
					results[p.ToolResult.CallID] = m
				}
			}
		}
	}
	used := map[*core.Message]bool{}
	out := make([]*core.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == core.MsgTool {
			continue // placed after their calls below
		}
		out = append(out, m)
		if m.Role != core.MsgAssistant {
			continue
		}
		for _, p := range m.Parts {
			if p.Type != core.PartToolCall || p.ToolCall == nil {
				continue
			}
			id := p.ToolCall.ID
			if res, ok := results[id]; ok && !used[res] {
				used[res] = true
				out = append(out, onlyResult(res, id))
				continue
			}
			out = append(out, &core.Message{Session: m.Session, Seq: m.Seq, Run: m.Run, Role: core.MsgTool,
				Kind: core.KindNormal, Provenance: core.ProvSystem,
				Parts: []core.Part{{Type: core.PartToolResult, ToolResult: &core.ToolResult{
					CallID: id, Name: p.ToolCall.Name, Content: notExecuted, IsError: true}}}})
		}
	}
	return out
}

// onlyResult narrows a tool message to the result for one call id.
func onlyResult(m *core.Message, id string) *core.Message {
	if len(m.Parts) == 1 {
		return m
	}
	cp := *m
	cp.Parts = nil
	for _, p := range m.Parts {
		if p.Type == core.PartToolResult && p.ToolResult != nil && p.ToolResult.CallID == id {
			cp.Parts = append(cp.Parts, p)
		}
	}
	return &cp
}

// loadHistory reads the session after its summary and renders it for the
// model: context parts become text, untrusted content is spotlighted,
// attachments are loaded or described, and everything is redacted.
func (r *Runner) loadHistory(ctx context.Context, rs *runState) ([]histMsg, error) {
	sess := rs.sess
	raw, err := r.d.Store.Messages().Range(ctx, core.SystemViewer(), sess.ID, sess.SummaryUpto+1, 0)
	if err != nil {
		return nil, fmt.Errorf("agent: history: %w", err)
	}
	kept := raw[:0:0]
	for _, m := range raw {
		if m.Kind == core.KindNotice || m.Kind == core.KindSummary {
			continue
		}
		kept = append(kept, m)
	}
	var out []histMsg
	if sess.Epoch > 0 && sess.Summary != "" {
		text := "<context-summary epoch=\"" + strconv.Itoa(sess.Epoch) + "\">\n" + r.redact(sess.Summary) + "\n</context-summary>"
		out = append(out, histMsg{msg: core.Message{Session: sess.ID, Role: core.MsgUser, Kind: core.KindSummary,
			Provenance: core.ProvSystem, Parts: []core.Part{{Type: core.PartText, Text: text}}},
			regions: []region{rSummary}, group: -1})
	}
	group := 0
	for _, m := range orderToolPairs(kept) {
		if m.Role == core.MsgUser && m.Provenance != core.ProvSystem {
			group++
		}
		out = append(out, r.renderMessage(rs, m, group))
	}
	return out, nil
}

func (r *Runner) renderMessage(rs *runState, m *core.Message, group int) histMsg {
	h := histMsg{group: group}
	cp := *m
	cp.Parts = make([]core.Part, 0, len(m.Parts))
	seq := strconv.FormatInt(m.Seq, 10)
	for i, p := range m.Parts {
		reg := rHistory
		switch p.Type {
		case core.PartContext:
			p = core.Part{Type: core.PartText, Text: r.redact(p.Text)}
			reg = rTurnContext
		case core.PartText:
			p.Text = r.redact(p.Text)
			if m.Role == core.MsgUser && m.Provenance == core.ProvUntrusted {
				p.Text = wrapUntrusted(r.spotID(string(m.Session), seq, strconv.Itoa(i)), p.Text,
					"source", "message", "sender", m.Sender)
			}
		case core.PartToolResult:
			if p.ToolResult == nil {
				continue
			}
			tr := *p.ToolResult
			tr.Content = r.redact(tr.Content)
			if tr.Untrusted {
				tr.Content = wrapUntrusted(r.spotID(string(m.Session), seq, strconv.Itoa(i), tr.CallID), tr.Content,
					"source", tr.Name)
			}
			p.ToolResult = &tr
			reg = rToolResult
		case core.PartToolCall:
			if p.ToolCall == nil {
				continue
			}
			if r.d.Redactor != nil {
				tc := *p.ToolCall
				tc.Args = r.d.Redactor.RedactBytes(tc.Args)
				p.ToolCall = &tc
			}
		case core.PartImage:
			p = r.loadImage(rs, p)
		case core.PartFile:
			p = fileNote(p)
		}
		cp.Parts = append(cp.Parts, p)
		h.regions = append(h.regions, reg)
	}
	h.msg = cp
	return h
}

func fileNote(p core.Part) core.Part {
	if p.Blob == nil {
		return core.Part{Type: core.PartText, Text: "[attached file]"}
	}
	return core.Part{Type: core.PartText, Text: fmt.Sprintf("[attached file %q (%s, %d bytes) at %s; read it with fs_read]",
		p.Blob.Name, p.Blob.MIME, p.Blob.Size, p.Blob.Ref)}
}

// loadImage fills Blob.Data from the principal's workspace. Data is never
// persisted; an unreadable or oversized image becomes a note.
func (r *Runner) loadImage(rs *runState, p core.Part) core.Part {
	if p.Blob == nil {
		return core.Part{Type: core.PartText, Text: "[image unavailable]"}
	}
	note := func(why string) core.Part {
		return core.Part{Type: core.PartText, Text: fmt.Sprintf("[image %q %s]", p.Blob.Name, why)}
	}
	dir := r.workspaceDir(rs.principal)
	if dir == "" || !filepath.IsLocal(p.Blob.Ref) {
		return note("unavailable")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return note("unavailable")
	}
	defer root.Close()
	st, err := root.Stat(p.Blob.Ref)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return note("was deleted")
	case err != nil:
		return note("unavailable")
	case st.Size() > imageMax:
		return note("is too large to show the model")
	}
	data, err := root.ReadFile(p.Blob.Ref)
	if err != nil {
		return note("unavailable")
	}
	b := *p.Blob
	b.Data = data
	p.Blob = &b
	return p
}

// forModel adapts history to one model: images are replaced by a note for
// models without vision.
func forModel(hist []histMsg, m core.ModelInfo) []histMsg {
	if m.Vision {
		return hist
	}
	out := make([]histMsg, len(hist))
	for i, h := range hist {
		out[i] = h
		var parts []core.Part
		for j, p := range h.msg.Parts {
			if p.Type != core.PartImage {
				continue
			}
			if parts == nil {
				parts = slices.Clone(h.msg.Parts)
			}
			name := ""
			if p.Blob != nil {
				name = p.Blob.Name
			}
			parts[j] = core.Part{Type: core.PartText, Text: fmt.Sprintf("[image %q omitted: this model cannot see images]", name)}
		}
		if parts != nil {
			out[i].msg.Parts = parts
		}
	}
	return out
}
