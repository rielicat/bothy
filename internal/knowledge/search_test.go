package knowledge

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/core"
)

func TestCitationFormat(t *testing.T) {
	for _, c := range []struct {
		repo, sha, path string
		start, end      int
		want            string
	}{
		{"acme/api", "0123456789abcdef0123", "internal/x.go", 10, 42, "acme/api@0123456789ab:internal/x.go#L10-L42"},
		{"acme/api", "abc1234", "README.md", 7, 7, "acme/api@abc1234:README.md#L7"},
		{"local/proj", "", "a/b c.txt", 1, 3, "local/proj:a/b c.txt#L1-L3"},
	} {
		if got := citation(c.repo, c.sha, c.path, c.start, c.end); got != c.want {
			t.Errorf("citation = %q, want %q", got, c.want)
		}
	}
}

func TestRefineNarrowsToTheMatch(t *testing.T) {
	var body []string
	for i := range 100 {
		body = append(body, fmt.Sprintf("line %d", i+101))
	}
	body[59] = "func Allow() { // the needle }"
	body[70] = strings.Repeat("x", 1000)
	h := refine(core.KnowledgeHit{Repo: "acme/api", SHA: "abcdef1234567", Path: "a.go", StartLine: 101, EndLine: 200,
		Snippet: strings.Join(body, "\n")}, queryWords("Needle"))
	if h.StartLine != 157 || h.EndLine != 186 || !strings.HasPrefix(h.Snippet, "line 157\n") {
		t.Fatalf("window = %d-%d %q", h.StartLine, h.EndLine, h.Snippet[:20])
	}
	if h.Citation != "acme/api@abcdef123456:a.go#L157-L186" {
		t.Fatalf("citation = %q", h.Citation)
	}
	if len(h.Snippet) > snippetBytes || !strings.Contains(h.Snippet, strings.Repeat("x", snippetLineMax)+"…") {
		t.Fatalf("snippet = %d bytes", len(h.Snippet))
	}
	if lines := strings.Count(h.Snippet, "\n") + 1; lines != h.EndLine-h.StartLine+1 {
		t.Fatalf("snippet has %d lines for citation %s", lines, h.Citation)
	}
	// The index matched another form of the word; the window still lands on it.
	body[59] = "return Response.redirect(next, 302);"
	h = refine(core.KnowledgeHit{Repo: "acme/api", Path: "a.go", StartLine: 101, EndLine: 200, Snippet: strings.Join(body, "\n")},
		queryWords("redirects"))
	if h.StartLine != 157 {
		t.Fatalf("stemmed window starts at %d, want 157", h.StartLine)
	}
	short := refine(core.KnowledgeHit{Repo: "r/x", Path: "b.md", StartLine: 5, EndLine: 7, Snippet: "a\nneedle\nc"}, []string{"needle"})
	if short.StartLine != 5 || short.EndLine != 7 || short.Snippet != "a\nneedle\nc" || short.Citation != "r/x:b.md#L5-L7" {
		t.Fatalf("short chunk = %+v", short)
	}
}

func TestQueryValidation(t *testing.T) {
	for _, bad := range []string{"", "   ", `"*()^`, strings.Repeat("a", queryMaxRunes+1), "\xff\xfe"} {
		if _, err := normalizeQuery(bad); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("normalizeQuery(%.20q) = %v", bad, err)
		}
	}
	if q, err := normalizeQuery("  rate\n\tlimiter  "); err != nil || q != "rate limiter" {
		t.Errorf("normalizeQuery = %q %v", q, err)
	}
}

func TestSearchHostileInputThroughTheService(t *testing.T) {
	h := newHarness(t)
	h.writeRepo()
	h.index("repos/acme/api", "acme/api", "")
	for _, q := range []string{`NEAR(rate limiter)`, `"rate" OR`, `limit.go:rate`, `rate*`, `^limiter`, `(rate`, `-rate +limiter`} {
		hits, err := h.svc.Search(h.ctx, h.viewer(), core.KnowledgeQuery{Text: q})
		if err != nil {
			t.Errorf("Search(%q) = %v", q, err)
			continue
		}
		for _, hit := range hits {
			if hit.Citation == "" || hit.StartLine < 1 || hit.EndLine < hit.StartLine {
				t.Errorf("Search(%q) hit = %+v", q, hit)
			}
		}
	}
	if out := formatHits("x", nil); !strings.Contains(out, "No indexed code") {
		t.Errorf("formatHits(nil) = %q", out)
	}
}
