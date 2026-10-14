package knowledge

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	summaryMax     = 160
	summaryReadMax = 16 << 10
	// progressEvery paces the knowledge.updated events a running index
	// sends with its progress.
	progressEvery = time.Second
)

var (
	mdLinkRE    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	mdMarkRE    = regexp.MustCompile("[`*_~]")
	listMarkRE  = regexp.MustCompile(`^([-*+]|\d+\.)\s+`)
	sentenceEnd = regexp.MustCompile(`^.+?[.!?](\s|$)`)
)

// summaryOf is the first sentence of the notes' first paragraph as plain
// text, for the project list: headings, quotes, tables, rules, front
// matter and code fences are passed over; links keep their text.
func summaryOf(md string) string {
	if strings.HasPrefix(md, "---\n") {
		if end := strings.Index(md[4:], "\n---\n"); end >= 0 {
			md = md[4+end+5:]
		}
	}
	var para []string
	fence := false
	for _, l := range strings.Split(md, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "```") || strings.HasPrefix(l, "~~~") {
			fence = !fence
			if len(para) > 0 {
				break
			}
			continue
		}
		if fence {
			continue
		}
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, ">") || strings.HasPrefix(l, "|") || isRule(l) {
			if len(para) > 0 {
				break
			}
			continue
		}
		para = append(para, listMarkRE.ReplaceAllString(l, ""))
	}
	text := mdMarkRE.ReplaceAllString(mdLinkRE.ReplaceAllString(strings.Join(para, " "), "$1"), "")
	// Notes Bothy drafted come from repository content: a bidi override
	// there would reorder the project list line around the summary.
	text = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if m := sentenceEnd.FindString(text); m != "" {
		text = strings.TrimSpace(m)
	}
	if utf8.RuneCountInString(text) > summaryMax {
		r := []rune(text)
		text = strings.TrimRight(string(r[:summaryMax-1]), " ") + "…"
	}
	return text
}

func isRule(l string) bool {
	if len(l) < 3 {
		return false
	}
	for _, c := range l {
		if c != '-' && c != '*' && c != '_' {
			return false
		}
	}
	return true
}

// readSummary is summaryOf the start of the repo's notes.
func (s *Service) readSummary(r *core.IndexedRepo) string {
	if r.Notes.Status == core.NotesNone {
		return ""
	}
	p, err := s.notesPath(r)
	if err != nil {
		return ""
	}
	f, err := os.Open(p)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Debug("knowledge: read notes for the summary", "repo", r.ID, "err", err)
		}
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, summaryReadMax))
	if err != nil {
		return ""
	}
	return summaryOf(strings.ToValidUTF8(string(b), ""))
}

// fill adds what a repo row is read with but never stores: the notes'
// summary and a running index's progress.
func (s *Service) fill(r *core.IndexedRepo) {
	r.Summary = s.readSummary(r)
	r.Progress = nil
	if r.Status != core.IndexQueued && r.Status != core.IndexRunning {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.progress[r.ID]; ok {
		r.Progress = &p
	}
}

func (s *Service) setProgress(id int64, p core.IndexProgress) {
	s.mu.Lock()
	s.progress[id] = p
	s.mu.Unlock()
}

func (s *Service) clearProgress(id int64) {
	s.mu.Lock()
	delete(s.progress, id)
	delete(s.progressAt, id)
	s.mu.Unlock()
}

// stepProgress records done files and reports whether it is time to tell
// the UI.
func (s *Service) stepProgress(id int64, done int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.progress[id]
	p.Done = done
	if p.Total < done {
		p.Total = done
	}
	s.progress[id] = p
	now := s.clock.Now()
	if now.Sub(s.progressAt[id]) < progressEvery {
		return false
	}
	s.progressAt[id] = now
	return true
}
