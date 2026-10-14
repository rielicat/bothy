package knowledge

import (
	"context"
	"errors"
	"strings"

	"github.com/rielicat/bothy/internal/core"
)

const (
	notesMoreTail = "\n… (more with the notes tool, op read)"
	notesMinRoom  = 120 // below this a summary is not worth its header
)

type injected struct {
	repo int64
	hash string
}

// TurnContext adds the notes summaries of the repos this session worked on
// (most recent first) to the user turn's <turn-context>, within
// knowledge.context_bytes. A repo's notes enter once per epoch and again
// only when they change: the turn-context is persisted with the turn, so
// earlier prompts stay byte-identical (cache-stable) and later turns do not
// repeat it. Untrusted notes are spotlighted and taint the session.
func (s *Service) TurnContext(ctx context.Context, in core.TurnContextInput) (core.TurnContextBlock, error) {
	c := s.cfg()
	if !c.Enabled || c.ContextBytes <= 0 || in.Session == "" {
		return core.TurnContextBlock{}, nil
	}
	v := core.Viewer{Principals: []core.PrincipalID{in.Principal.ID}}
	touches, err := s.d.Repo.Touched(ctx, v, in.Session)
	if errors.Is(err, core.ErrNotFound) {
		return core.TurnContextBlock{}, nil
	}
	if err != nil {
		return core.TurnContextBlock{}, err
	}
	var b strings.Builder
	var taint core.Taint
	var marks []injected
	for _, t := range touches {
		r, err := s.d.Repo.Get(ctx, v, t.Repo)
		if err != nil {
			continue
		}
		content, err := s.readNotes(r)
		if err != nil {
			s.log.Warn("knowledge: read notes for turn-context", "repo", r.ID, "err", err)
			continue
		}
		if strings.TrimSpace(content) == "" {
			continue
		}
		hash := etagOf(content)
		if t.InjectedEpoch == in.Epoch && t.InjectedHash == hash {
			continue
		}
		block, ok := notesBlock(r, content, c.ContextBytes-b.Len())
		if !ok {
			break
		}
		b.WriteString(block)
		if r.Notes.Untrusted {
			taint |= core.TaintUntrusted
		}
		marks = append(marks, injected{r.ID, hash})
	}
	if len(marks) == 0 {
		return core.TurnContextBlock{}, nil
	}
	sess, epoch := in.Session, in.Epoch
	return core.TurnContextBlock{Text: strings.TrimRight(b.String(), "\n"), Taint: taint, Commit: func(ctx context.Context) error {
		var errs []error
		for _, m := range marks {
			if err := s.d.Repo.MarkInjected(ctx, sess, m.repo, epoch, m.hash); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}}, nil
}

// notesBlock renders one repo's summary in at most room bytes.
func notesBlock(r *core.IndexedRepo, content string, room int) (string, bool) {
	head := "project notes for " + r.Repo + " (Bothy's own notes, kept outside the repo; the notes tool reads and updates them):\n"
	overhead := len(head) + len(notesMoreTail) + 1
	if r.Notes.Untrusted {
		overhead += len(spotlight("", "source", "project_notes", "repo", r.Repo))
	}
	if room-overhead < notesMinRoom {
		return "", false
	}
	sum, more := summarize(content, room-overhead)
	if strings.TrimSpace(sum) == "" {
		return "", false
	}
	if more {
		sum += notesMoreTail
	}
	if r.Notes.Untrusted {
		sum = spotlight(sum, "source", "project_notes", "repo", r.Repo)
	}
	return head + sum + "\n", true
}
