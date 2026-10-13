package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// TestConcurrentWriters: 200 goroutines writing at once never see
// SQLITE_BUSY, because every write queues for the single writer connection.
func TestConcurrentWriters(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleMember)
	s := w.session(core.UserPrincipal(u.ID))
	const writers = 200
	var wg sync.WaitGroup
	errs := make(chan error, writers*4)
	for i := range writers {
		wg.Go(func() {
			m := &core.Message{Session: s.ID, Role: core.MsgUser, Kind: core.KindNormal, Provenance: core.ProvUser,
				Parts: []core.Part{{Type: core.PartText, Text: fmt.Sprintf("writer %d", i)}}, CreatedAt: t0}
			errs <- w.db.Messages().Append(w.ctx, m)
			errs <- w.db.Meta().Set(w.ctx, fmt.Sprintf("k%d", i%7), "v")
			errs <- w.db.Tx(w.ctx, func(tx core.Store) error {
				_, err := tx.Dedup().Seen(w.ctx, "telegram", fmt.Sprintf("m%d", i), t0)
				return err
			})
		})
		wg.Go(func() {
			_, err := w.db.Messages().Range(w.ctx, core.SystemViewer(), s.ID, 1, 10)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	all, err := w.db.Messages().Range(w.ctx, core.SystemViewer(), s.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, m := range all {
		seen[m.Seq] = true
	}
	if len(all) != writers || len(seen) != writers || !seen[1] || !seen[writers] {
		t.Fatalf("%d messages with %d distinct seqs, want 1..%d", len(all), len(seen), writers)
	}
}

func TestAppendThroughput(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleMember)
	s := w.session(core.UserPrincipal(u.ID))
	start := time.Now()
	for i := range 1000 {
		w.message(s.ID, core.ProvUser, fmt.Sprintf("message number %d about nothing in particular", i))
	}
	if d := time.Since(start); d > budget(time.Second) {
		t.Errorf("1000 appends took %v, budget %v", d, budget(time.Second))
	}
}

func TestSearchLatency(t *testing.T) {
	w := newWorld(t)
	u := w.user(core.RoleMember)
	other := w.user(core.RoleMember)
	sessions := []*core.Session{w.session(core.UserPrincipal(u.ID)), w.session(core.UserPrincipal(u.ID)), w.session(core.UserPrincipal(other.ID))}
	// Bulk-load in one statement: through Append the corpus alone takes ~20s
	// under the race detector. The FTS triggers index every row either way.
	words, _ := json.Marshal(strings.Fields("alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa"))
	ids, _ := json.Marshal([]core.SessionID{sessions[0].ID, sessions[1].ID, sessions[2].ID})
	_, err := w.db.w.ExecContext(w.ctx, `WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < 9999)
		INSERT INTO messages(session_id, seq, role, kind, provenance, parts, text, created_at)
		SELECT json_extract(?2, '$[' || (i % 3) || ']'), i + 1, 'user', 'normal', 'user', '[]',
			json_extract(?1, '$[' || (i % 16) || ']') || ' ' || json_extract(?1, '$[' || (i * 7 % 16) || ']') || ' ' ||
			json_extract(?1, '$[' || (i * 3 % 16) || ']') || ' note ' || i || IIF(i % 100 = 0, ' lisbon flight', ''), ?3
		FROM n`, string(words), string(ids), ms(t0))
	w.must(err)
	v := viewerOf(u)
	for _, q := range []string{"lisbon flight", "alpha", "golf kilo"} {
		if _, err := w.db.Messages().Search(w.ctx, v, q, 20); err != nil {
			t.Fatal(err)
		}
		// The first query warms the page cache; time the best of a few.
		best := time.Hour
		var hits []core.SearchHit
		for range 3 {
			start := time.Now()
			var err error
			if hits, err = w.db.Messages().Search(w.ctx, v, q, 20); err != nil {
				t.Fatal(err)
			}
			best = min(best, time.Since(start))
		}
		if len(hits) == 0 {
			t.Errorf("%q found nothing", q)
		}
		t.Logf("search %q: %v, %d hits", q, best, len(hits))
		if best > budget(50*time.Millisecond) {
			t.Errorf("search %q over 10K messages took %v, budget %v", q, best, budget(50*time.Millisecond))
		}
	}
}
