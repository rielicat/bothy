package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
)

// marked returns the matched pieces of a snippet, read the way a browser
// slices a string.
func marked(snip string, rs []dto.TextRange) []string {
	u := utf16.Encode([]rune(snip))
	var out []string
	for _, r := range rs {
		out = append(out, string(utf16.Decode(u[r.Start:r.End])))
	}
	return out
}

func TestPlainSnippets(t *testing.T) {
	text := markdownPlain("## Launch\n\nHere's a **launch checklist** for [v2.3](https://x.example/v2.3):\n\n- Freeze `main`\n- Tag 🚀 the release\n```sh\nmake release\n```")
	if text != "Launch Here's a launch checklist for v2.3: Freeze main Tag 🚀 the release make release" {
		t.Fatalf("plain = %q", text)
	}
	if got := markdownPlain("| Step | Owner |\n|------|:-----:|\n| Tag the release | Sam |"); got != "Step Owner Tag the release Sam" {
		t.Fatalf("table = %q", got)
	}
	snip, rs := snippet(text, searchTerms("Release CHECKLIST"), true)
	if got := marked(snip, rs); len(got) != 3 || got[0] != "checklist" || got[1] != "release" || got[2] != "release" {
		t.Fatalf("marks %q in %q", got, snip)
	}
	// Ranges count UTF-16 code units: the emoji before "the release" is two.
	if got := marked(snip, rs); got[1] != "release" {
		t.Fatalf("ranges are off after an astral character: %q", got)
	}
	if _, rs := snippet("prerelease notes", searchTerms("release"), true); len(rs) != 0 {
		t.Fatalf("chat matches are whole words: %v", rs)
	}
	if snip, rs := snippet("return safeRedirect(next)", searchTerms("redirect"), false); len(marked(snip, rs)) != 1 || marked(snip, rs)[0] != "Redirect" {
		t.Fatalf("code matches inside identifiers: %q %v", snip, rs)
	}
	if snip, rs := snippet("Café menu", searchTerms("cafe"), true); len(rs) != 1 || marked(snip, rs)[0] != "Café" {
		t.Fatalf("accents fold: %q %v", snip, rs)
	}
	long := ""
	for range 60 {
		long += "filler "
	}
	snip, rs = snippet(long+"the deploy key rotated "+long, searchTerms("deploy"), true)
	if snip[:3] != "…" || snip[len(snip)-3:] != "…" || len(marked(snip, rs)) != 1 || marked(snip, rs)[0] != "deploy" {
		t.Fatalf("long text snippet %q %v", snip, rs)
	}
}

type searchKnowledge struct {
	*fakeKnowledge
	hits []core.KnowledgeHit
}

func (k *searchKnowledge) Search(_ context.Context, v core.Viewer, q core.KnowledgeQuery) ([]core.KnowledgeHit, error) {
	var out []core.KnowledgeHit
	for _, h := range k.hits {
		if v.Can(h.Principal) {
			out = append(out, h)
		}
	}
	return out, nil
}

func TestSearchGroupsChatsNotesAndCode(t *testing.T) {
	h := newHarness(t).setup()
	ctx := context.Background()
	mp := core.UserPrincipal(h.member.user)
	chat := func(title string, archived bool, texts ...string) *core.Session {
		s := &core.Session{ID: core.SessionID(core.NewID(core.PrefixSession)), Principal: mp, Origin: "web", Title: title,
			Archived: archived, CreatedAt: time.Now(), UpdatedAt: time.Now(), LastMessageAt: time.Now()}
		if err := h.store.Sessions().Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		for _, text := range texts {
			m := &core.Message{Session: s.ID, Role: core.MsgAssistant, Kind: core.KindNormal, Provenance: core.ProvSystem,
				Parts: []core.Part{{Type: core.PartText, Text: text}}, CreatedAt: time.Now()}
			if err := h.store.Messages().Append(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		return s
	}
	launch := chat("v2.3 launch checklist", false, "Here's a **launch checklist** for v2.3.", "The launch checklist again.")
	chat("Deleted chat", true, "an old launch checklist")
	k := &searchKnowledge{fakeKnowledge: newFakeKnowledge()}
	repo := k.add(mp, "acme/web")
	repo.Notes.Status = core.NotesReady
	k.notes[repo.ID] = "# acme/web\n\nA web app.\n\n## How to release\n\nRun the **launch checklist**, then `make release`.\n\n## Tests\n\nRun pnpm test."
	other := k.add(core.UserPrincipal(h.owner.user), "acme/secret")
	other.Notes.Status = core.NotesReady
	k.notes[other.ID] = "## Launch checklist\n\nsecret launch checklist"
	k.hits = []core.KnowledgeHit{
		{RepoID: repo.ID, Repo: "acme/web", Principal: mp, Path: "docs/release.md", StartLine: 10, EndLine: 42,
			Citation: "acme/web@abc:docs/release.md#L10-L42", Snippet: "# Release\n\n  Follow the launch checklist before tagging.\n"},
		{RepoID: other.ID, Repo: "acme/secret", Principal: core.UserPrincipal(h.owner.user), Path: "x.md", Snippet: "launch checklist"},
	}
	h.srv.d.Knowledge = k

	res := decodeAs[dto.SearchResp](t, h.do("GET", "/api/search?q=launch+checklist", nil, h.member))
	if len(res.Chats) != 1 || res.Chats[0].Session != launch.ID || res.Chats[0].Title != "v2.3 launch checklist" {
		t.Fatalf("chats = %+v (one row per chat, deleted chats left out)", res.Chats)
	}
	c := res.Chats[0]
	if c.Snippet != "Here's a launch checklist for v2.3." && c.Snippet != "The launch checklist again." {
		t.Fatalf("chat snippet %q is not one message in plain text", c.Snippet)
	}
	if got := marked(c.Snippet, c.Ranges); len(got) != 2 || got[0] != "launch" || got[1] != "checklist" {
		t.Fatalf("chat snippet %q marks %q", c.Snippet, got)
	}
	if len(res.Notes) != 1 || res.Notes[0].Title != "How to release" || res.Notes[0].Repo != "acme/web" ||
		res.Notes[0].Snippet != "Run the launch checklist, then make release." {
		t.Fatalf("notes = %+v", res.Notes)
	}
	if len(res.Code) != 1 || res.Code[0].Snippet != "Follow the launch checklist before tagging." || res.Code[0].StartLine != 10 ||
		res.Code[0].Citation != "acme/web@abc:docs/release.md#L10-L42" {
		t.Fatalf("code = %+v", res.Code)
	}
	if got := marked(res.Code[0].Snippet, res.Code[0].Ranges); len(got) != 2 {
		t.Fatalf("code marks %q", got)
	}
	res = decodeAs[dto.SearchResp](t, h.do("GET", "/api/search?q=pnpm+release", nil, h.member))
	if len(res.Notes) != 2 || res.Notes[0].Title != "How to release" && res.Notes[0].Title != "Tests" {
		t.Fatalf("no section has every word: sections with any = %+v", res.Notes)
	}
	expect(t, h.do("GET", "/api/search?q=x&limit=0", nil, h.member), http.StatusBadRequest)
}
