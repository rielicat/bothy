package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

const (
	searchLimit    = 20
	searchLimitMax = 100
	searchQueryMax = 500
	// searchChatScan is how many message hits are read per chat returned:
	// a chat is listed once, by its best message.
	searchChatScan = 5
)

// search is GET /api/search?q=&limit=: chats, project notes and code, each
// group holding at most limit plain-text results.
func (s *Server) search(w http.ResponseWriter, r *http.Request, a core.Actor) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := searchLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > searchLimitMax {
			return badRequest("limit must be between 1 and %d", searchLimitMax)
		}
		limit = n
	}
	out := dto.SearchResp{Chats: []dto.ChatSearchHit{}, Notes: []dto.NoteSearchHit{}, Code: []dto.CodeSearchHit{}}
	if utf8.RuneCountInString(q) > searchQueryMax {
		return badRequest("query too long")
	}
	terms := searchTerms(q)
	if len(terms) == 0 {
		return ok(w, out)
	}
	ctx := r.Context()
	chats, err := s.searchChats(ctx, a.Viewer, q, terms, limit)
	if err != nil {
		return err
	}
	out.Chats = chats
	if s.d.Knowledge != nil {
		out.Notes = s.searchNotes(ctx, a.Viewer, terms, limit)
		out.Code = s.searchCode(ctx, a.Viewer, q, terms, limit)
	}
	return ok(w, out)
}

func (s *Server) searchChats(ctx context.Context, v core.Viewer, q string, terms [][]rune, limit int) ([]dto.ChatSearchHit, error) {
	hits, err := s.d.Store.Messages().Search(ctx, v, q, min(limit*searchChatScan, 500))
	if err != nil {
		return nil, err
	}
	n := s.namer(ctx, v)
	out := []dto.ChatSearchHit{}
	listed := map[core.SessionID]bool{}
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		if listed[h.Session] {
			continue
		}
		listed[h.Session] = true
		sess, err := s.d.Store.Sessions().Get(ctx, v, h.Session)
		if err != nil || sess.Archived {
			continue // deleted chats are not found again
		}
		msgs, err := s.d.Store.Messages().Range(ctx, v, h.Session, h.Seq, 1)
		if err != nil || len(msgs) == 0 || msgs[0].Seq != h.Seq {
			continue
		}
		text, ranges := snippet(markdownPlain(msgs[0].PlainText()), terms, true)
		out = append(out, dto.ChatSearchHit{Session: h.Session, Title: n.title(sess), Seq: h.Seq, At: h.At, Snippet: text,
			Ranges: ranges, Trusted: h.Trusted})
	}
	return out, nil
}

var noteHeading = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)

// noteSection is a part of a notes file under one heading.
type noteSection struct {
	title, body string
}

func noteSections(content string) []noteSection {
	var out []noteSection
	cur := noteSection{}
	var body []string
	fenced := false
	flush := func() {
		cur.body = strings.Join(body, "\n")
		if strings.TrimSpace(cur.body) != "" || cur.title != "" {
			out = append(out, cur)
		}
		body = nil
	}
	for _, line := range strings.Split(content, "\n") {
		if mdFence.MatchString(line) {
			fenced = !fenced
		}
		if m := noteHeading.FindStringSubmatch(line); m != nil && !fenced {
			flush()
			cur = noteSection{title: markdownPlain(m[1])}
			continue
		}
		body = append(body, line)
	}
	flush()
	return out
}

// searchNotes looks through the project notes of the viewer's repositories:
// sections holding every word first, then sections holding any.
func (s *Server) searchNotes(ctx context.Context, v core.Viewer, terms [][]rune, limit int) []dto.NoteSearchHit {
	repos, err := s.d.Knowledge.Repos(ctx, v, "")
	if err != nil {
		s.log.Warn("search: list knowledge repositories", "err", err)
		return []dto.NoteSearchHit{}
	}
	type scored struct {
		hit   dto.NoteSearchHit
		words int
	}
	var found []scored
	for _, repo := range repos {
		if repo.Notes.Status == core.NotesNone {
			continue
		}
		notes, err := s.d.Knowledge.Notes(ctx, v, repo.ID)
		if err != nil || notes.Content == "" {
			continue
		}
		for _, sec := range noteSections(notes.Content) {
			text := markdownPlain(sec.body)
			words := 0
			rs := []rune(sec.title + " " + text)
			for _, t := range terms {
				if len(matchRanges(rs, [][]rune{t}, true)) > 0 {
					words++
				}
			}
			if words == 0 {
				continue
			}
			snip, ranges := snippet(text, terms, true)
			found = append(found, scored{dto.NoteSearchHit{RepoID: repo.ID, Repo: repo.Repo, Title: sec.title, Snippet: snip, Ranges: ranges}, words})
		}
	}
	slices.SortStableFunc(found, func(a, b scored) int { return b.words - a.words })
	out := []dto.NoteSearchHit{}
	for _, f := range found {
		if len(out) == limit || f.words < len(terms) && found[0].words == len(terms) {
			break
		}
		out = append(out, f.hit)
	}
	return out
}

// searchCode runs the knowledge index search; each result shows its first
// line with a match, matched inside identifiers too.
func (s *Server) searchCode(ctx context.Context, v core.Viewer, q string, terms [][]rune, limit int) []dto.CodeSearchHit {
	hits, err := s.d.Knowledge.Search(ctx, v, core.KnowledgeQuery{Text: q, Limit: min(limit, knowledgeSearchMax)})
	if err != nil {
		if !errors.Is(err, core.ErrInvalid) {
			s.log.Warn("search: knowledge search", "err", err)
		}
		return []dto.CodeSearchHit{}
	}
	out := make([]dto.CodeSearchHit, 0, len(hits))
	for _, h := range hits {
		line := ""
		for _, l := range strings.Split(h.Snippet, "\n") {
			l = strings.TrimSpace(l)
			if line == "" {
				line = l
			}
			if len(matchRanges([]rune(l), terms, false)) > 0 {
				line = l
				break
			}
		}
		snip, ranges := snippet(strings.Join(strings.Fields(line), " "), terms, false)
		out = append(out, dto.CodeSearchHit{RepoID: h.RepoID, Repo: h.Repo, Path: h.Path, StartLine: h.StartLine, EndLine: h.EndLine,
			Citation: h.Citation, Snippet: snip, Ranges: ranges})
	}
	return out
}
