package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bothyhq/bothy/internal/core"
)

func searchKeys(t *testing.T, s core.Store, v core.Viewer, q string) map[string]bool {
	t.Helper()
	hits, err := s.Messages().Search(context.Background(), v, q, 50)
	if err != nil {
		t.Fatalf("Search %q: %v", q, err)
	}
	out := map[string]bool{}
	for _, h := range hits {
		out[string(h.Session)+"#"+h.Snippet] = true
	}
	return out
}

func TestSearchFindsAndScopes(t *testing.T) {
	w := newWorld(t)
	alice, bob := w.user(core.RoleMember), w.user(core.RoleAdmin)
	sp := w.space(alice)
	mine := w.session(core.UserPrincipal(alice.ID))
	group := w.session(sp.Principal)
	hidden := w.session(core.UserPrincipal(alice.ID), func(s *core.Session) { s.Origin, s.Hidden = "api", true })
	foreign := w.session(core.UserPrincipal(bob.ID))
	w.message(mine.ID, core.ProvUser, "Book the Lisbon flight for Thursday")
	w.message(group.ID, core.ProvUntrusted, "lisbon meetup in the group")
	w.message(hidden.ID, core.ProvUser, "lisbon through the hidden API session")
	w.message(foreign.ID, core.ProvUser, "lisbon in bob's private notes")
	w.message(mine.ID, core.ProvUser, "Le café près de la gare")

	v := viewerOf(alice, sp.Principal)
	hits, err := w.db.Messages().Search(w.ctx, v, "lisbon", 10)
	if err != nil {
		t.Fatal(err)
	}
	got := map[core.SessionID]core.SearchHit{}
	for _, h := range hits {
		got[h.Session] = h
	}
	if len(hits) != 2 || got[mine.ID].Seq != 1 || got[group.ID].Seq != 1 {
		t.Fatalf("hits = %+v, want the own and group sessions only", hits)
	}
	if h := got[mine.ID]; !h.Trusted || !strings.Contains(h.Snippet, "[Lisbon]") || h.Title != mine.Title || !h.At.Equal(t0) {
		t.Errorf("own hit = %+v", h)
	}
	if got[group.ID].Trusted {
		t.Error("an untrusted message was reported as trusted")
	}
	for name, viewer := range map[string]core.Viewer{
		"foreign admin": func() core.Viewer { v := viewerOf(bob); v.Admin = true; return v }(),
		"empty":         {},
		"user only":     {User: alice.ID},
	} {
		hits, err := w.db.Messages().Search(w.ctx, viewer, "lisbon", 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hits {
			if h.Session == mine.ID || h.Session == group.ID || h.Session == hidden.ID {
				t.Errorf("%s viewer found %s", name, h.Session)
			}
		}
	}
	if keys := searchKeys(t, w.db, core.SystemViewer(), "lisbon"); len(keys) != 3 {
		t.Errorf("SystemViewer sees %d sessions, want every non-hidden one (3)", len(keys))
	}
	if keys := searchKeys(t, w.db, v, "cafe pres"); len(keys) != 1 {
		t.Errorf("diacritics are folded (remove_diacritics 2): %v", keys)
	}
	if keys := searchKeys(t, w.db, v, "LISBON thursday"); len(keys) != 1 {
		t.Errorf("terms are ANDed and case-folded: %v", keys)
	}
}

func TestSearchHostileInput(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleMember)
	s := w.session(core.UserPrincipal(u.ID))
	w.message(s.ID, core.ProvUser, `near the "quoted" lisbon*star text`)
	v := viewerOf(u)
	for _, q := range []string{`"`, `""`, `NEAR(`, `NEAR(a b, 2)`, `*`, `lisbon*`, `"lisbon`, `lisbon"`, `a OR b`, `AND`, `NOT x`,
		`(`, `)`, `-x`, `^lisbon`, `text:lisbon`, `{text} : x`, `'`, `\`, `%`, `_`, "\x00", "lisbon\x00star", ``, `   `,
		strings.Repeat("lisbon ", 500), strings.Repeat(`"(`, 1000), "日本語 テキスト", "💥"} {
		if _, err := w.db.Messages().Search(w.ctx, v, q, 10); err != nil {
			t.Errorf("Search(%.40q) = %v", q, err)
		}
	}
	if keys := searchKeys(t, w.db, v, `text:lisbon NEAR("quoted")`); len(keys) != 1 {
		t.Errorf("FTS syntax is treated as words: %v", keys)
	}
	if hits, _ := w.db.Messages().Search(w.ctx, v, "", 10); len(hits) != 0 {
		t.Errorf("empty query returned %d hits", len(hits))
	}
}

func TestMatchQuery(t *testing.T) {
	for in, want := range map[string]string{
		`lisbon flight`:      `"lisbon" "flight"`,
		`"lisbon" OR NEAR(x`: `"lisbon" "OR" "NEAR" "x"`,
		`col:val*`:           `"col" "val"`,
		`café`:               `"café"`,
		`  `:                 ``,
		`a_b-c.d`:            `"a" "b" "c" "d"`,
	} {
		if got := matchQuery(in); got != want {
			t.Errorf("matchQuery(%q) = %q, want %q", in, got, want)
		}
	}
	many := matchQuery(strings.Repeat("w ", 100))
	if n := strings.Count(many, `"w"`); n != maxSearchTerms {
		t.Errorf("terms capped at %d, got %d", maxSearchTerms, n)
	}
}

// TestSearchSurvivesVacuum guards the INTEGER PRIMARY KEY choice: FTS5
// external content keys on messages.id, which VACUUM and VACUUM INTO must
// not renumber.
func TestSearchSurvivesVacuum(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleMember)
	s := w.session(core.UserPrincipal(u.ID))
	v := viewerOf(u)
	for i := range 50 {
		w.message(s.ID, core.ProvUser, "filler message "+strings.Repeat("x", i))
	}
	target := w.message(s.ID, core.ProvUser, "the zanzibar itinerary")
	if _, err := w.db.w.ExecContext(w.ctx, `DELETE FROM messages WHERE seq % 2 = 0`); err != nil {
		t.Fatal(err)
	}
	check := func(st core.Store, when string) {
		t.Helper()
		hits, err := st.Messages().Search(context.Background(), v, "zanzibar", 10)
		if err != nil || len(hits) != 1 || hits[0].Seq != target.Seq {
			t.Fatalf("%s: hits = %+v, %v; want seq %d", when, hits, err, target.Seq)
		}
		if hits, _ := st.Messages().Search(context.Background(), v, "filler", 50); len(hits) != 25 {
			t.Fatalf("%s: %d filler hits, want 25", when, len(hits))
		}
	}
	check(w.db, "before VACUUM")
	if _, err := w.db.w.ExecContext(w.ctx, `VACUUM`); err != nil {
		t.Fatal(err)
	}
	check(w.db, "after VACUUM")

	copyPath := filepath.Join(t.TempDir(), "restored", "bothy.db")
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.w.ExecContext(w.ctx, `VACUUM INTO ?`, copyPath); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(context.Background(), Options{Path: copyPath})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	check(restored, "restored VACUUM INTO copy")
	var bad string
	if err := restored.r.QueryRow(`PRAGMA integrity_check`).Scan(&bad); err != nil || bad != "ok" {
		t.Fatalf("integrity_check = %q, %v", bad, err)
	}
	if _, err := restored.w.Exec(`INSERT INTO messages_fts(messages_fts) VALUES ('integrity-check')`); err != nil {
		t.Fatalf("FTS integrity-check on the restored copy: %v", err)
	}
}
