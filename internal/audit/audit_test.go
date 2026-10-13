package audit

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/store"
	"github.com/rielicat/bothy/internal/testkit/fakes"

	_ "modernc.org/sqlite"
)

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type env struct {
	t    *testing.T
	path string
	db   *store.DB
	w    *Writer
	red  *fakes.Redactor
}

func newEnv(t *testing.T) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bothy.db")
	e := &env{t: t, path: path, red: fakes.NewRedactor()}
	e.reopen()
	return e
}

func (e *env) reopen() {
	e.t.Helper()
	if e.db != nil {
		_ = e.db.Close()
	}
	db, err := store.Open(context.Background(), store.Options{Path: e.path})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = db.Close() })
	w, err := New(Deps{Store: db, Redactor: e.red})
	if err != nil {
		e.t.Fatal(err)
	}
	e.db, e.w = db, w
}

func (e *env) record(at time.Time, action string, detail string) {
	e.t.Helper()
	if err := e.w.Record(context.Background(), core.AuditEntry{At: at, Actor: "system", Action: action, Detail: json.RawMessage(detail)}); err != nil {
		e.t.Fatalf("Record %s: %v", action, err)
	}
}

func (e *env) rows() []*core.AuditEntry {
	e.t.Helper()
	var out []*core.AuditEntry
	if err := e.db.Audit().Range(context.Background(), 0, 0, func(a *core.AuditEntry, _, _ []byte) error {
		x := *a
		out = append(out, &x)
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *env) verify() int64 {
	e.t.Helper()
	bad, err := e.w.Verify(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return bad
}

// raw opens a second connection that can lift the append-only triggers, the
// way someone with write access to the file could.
func (e *env) raw() *sql.DB {
	e.t.Helper()
	db, err := sql.Open("sqlite", "file:"+e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRecordSyncAndBatched(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.record(t0, "tool.exec", `{"tool":"shell"}`)
	if n := len(e.rows()); n != 0 {
		t.Fatalf("a batched entry was written before a flush (%d rows)", n)
	}
	e.record(t0.Add(time.Second), "auth.login", `{}`)
	rows := e.rows()
	if len(rows) != 2 || rows[0].Action != "tool.exec" || rows[1].Action != "auth.login" {
		t.Fatalf("a sync entry must flush the queue in order first: %+v", rows)
	}
	for _, a := range []string{"config.set", "approval.decide", "secret.use"} {
		e.record(t0, a, `{}`)
		if got := e.rows(); got[len(got)-1].Action != a {
			t.Errorf("%s was not written synchronously", a)
		}
	}
	e.record(t0, "memory.write", `{}`)
	if err := e.w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(e.rows()); n != 6 {
		t.Errorf("rows after Flush = %d, want 6", n)
	}
	if bad := e.verify(); bad != 0 {
		t.Errorf("Verify = %d", bad)
	}
}

func TestRunFlushesBatches(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.w.Run(ctx) }()
	e.record(t0, "tool.exec", `{}`)
	deadline := time.Now().Add(5 * time.Second)
	for len(e.rows()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("Run did not flush a batched entry")
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.record(t0, "tool.denied", `{}`)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := len(e.rows()); n != 2 {
		t.Errorf("Run's final flush left %d rows, want 2", n)
	}
}

func TestRecordNormalisesAndRedacts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.red.Add("k", []byte("sk-live-secret-value"))
	if err := e.w.Record(ctx, core.AuditEntry{Actor: "user:usr_x", Action: "auth.login", ID: 99,
		Detail: json.RawMessage(`{"b": 1, "a": "token sk-live-secret-value"}`)}); err != nil {
		t.Fatal(err)
	}
	rows := e.rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	if strings.Contains(string(r.Detail), "sk-live") || string(r.Detail) != `{"a":"token [redacted]","b":1}` {
		t.Errorf("detail = %s, want redacted canonical JSON", r.Detail)
	}
	if r.Outcome != "ok" || r.At.IsZero() || r.ID == 99 {
		t.Errorf("defaults not applied: %+v", r)
	}
	for name, bad := range map[string]core.AuditEntry{
		"no actor":     {Action: "x"},
		"no action":    {Actor: "system"},
		"bad outcome":  {Actor: "system", Action: "x", Outcome: "maybe"},
		"invalid json": {Actor: "system", Action: "x", Detail: json.RawMessage(`{"a":`)},
	} {
		if err := e.w.Record(ctx, bad); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%s: Record = %v, want ErrInvalid", name, err)
		}
	}
	if err := e.w.Flush(ctx); err != nil {
		t.Fatalf("rejected entries poisoned the queue: %v", err)
	}
}

func TestAppendOnlyTriggers(t *testing.T) {
	e := newEnv(t)
	e.record(t0, "auth.login", `{}`)
	raw := e.raw()
	for _, q := range []string{`UPDATE audit_log SET actor = 'x'`, `DELETE FROM audit_log`} {
		if _, err := raw.Exec(q); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: %v, want the append-only abort", q, err)
		}
	}
}

func TestVerifyFindsTampering(t *testing.T) {
	for _, c := range []struct {
		name   string
		tamper string
		bad    int64
	}{
		{"changed actor", `UPDATE audit_log SET actor = 'user:mallory' WHERE id = 3`, 3},
		{"changed detail", `UPDATE audit_log SET detail = '{"n":99}' WHERE id = 2`, 2},
		{"changed time", `UPDATE audit_log SET ts = ts + 1 WHERE id = 4`, 4},
		{"deleted row", `DELETE FROM audit_log WHERE id = 3`, 4},
		{"forged prev_hash", `UPDATE audit_log SET prev_hash = zeroblob(32) WHERE id = 5`, 5},
		{"deleted head of log", `DELETE FROM audit_log WHERE id = 1`, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			for i := range 5 {
				e.record(t0.Add(time.Duration(i)*time.Second), "auth.login", fmt.Sprintf(`{"n":%d}`, i))
			}
			if bad := e.verify(); bad != 0 {
				t.Fatalf("Verify before tampering = %d", bad)
			}
			raw := e.raw()
			for _, q := range []string{`DROP TRIGGER audit_no_update`, `DROP TRIGGER audit_no_delete`, c.tamper} {
				if _, err := raw.Exec(q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			if bad := e.verify(); bad != c.bad {
				t.Errorf("Verify = %d, want %d", bad, c.bad)
			}
		})
	}
}

func TestConcurrentRecordKeepsChain(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			action := "tool.exec"
			if i%3 == 0 {
				action = "auth.login"
			}
			if err := e.w.Record(context.Background(), core.AuditEntry{Actor: "system", Action: action, Detail: json.RawMessage(fmt.Sprintf(`{"i":%d}`, i))}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if bad := e.verify(); bad != 0 {
		t.Errorf("Verify after concurrent records = %d", bad)
	}
	if n := len(e.rows()); n != 50 {
		t.Errorf("rows = %d, want 50", n)
	}
}

func TestWriterResumesChainAfterRestart(t *testing.T) {
	e := newEnv(t)
	e.record(t0, "auth.login", `{}`)
	e.reopen()
	e.record(t0.Add(time.Second), "auth.logout", `{}`)
	if bad := e.verify(); bad != 0 {
		t.Errorf("Verify across writers = %d", bad)
	}
}

func readArchive(t *testing.T, p string) []archiveLine {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []archiveLine
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var l archiveLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("archive line %q: %v", sc.Text(), err)
		}
		out = append(out, l)
	}
	return out
}

func TestArchiveKeepsChainVerifiable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i := range 10 {
		e.record(t0.Add(time.Duration(i)*time.Hour), "auth.login", fmt.Sprintf(`{"n":%d}`, i))
	}
	before := e.rows()
	backups := filepath.Join(filepath.Dir(e.path), "backups")
	clock := func() time.Time { return t0.Add(48 * time.Hour) }

	res, err := archive(ctx, e.db, backups, t0.Add(4*time.Hour), clock)
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 4 || res.FromID != before[0].ID || res.ToID != before[3].ID {
		t.Fatalf("result = %+v, want rows 1-4", res)
	}
	lines := readArchive(t, res.File)
	if len(lines) != 4 || lines[3].Hash == "" || lines[0].ID != before[0].ID {
		t.Fatalf("archive file = %+v", lines)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(res.File); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("archive mode = %v, %v", fi.Mode().Perm(), err)
		}
	}
	rows := e.rows()
	if len(rows) != 7 || rows[0].ID != before[4].ID {
		t.Fatalf("remaining rows start at %d (%d rows), want %d (7 rows)", rows[0].ID, len(rows), before[4].ID)
	}
	anchor := rows[len(rows)-1]
	var d anchorDetail
	if anchor.Action != ActionArchived || anchor.ID <= before[len(before)-1].ID || json.Unmarshal(anchor.Detail, &d) != nil ||
		d.Count != 4 || d.LastHash != lines[3].Hash || d.File != filepath.Base(res.File) {
		t.Fatalf("anchor = %+v %+v", anchor, d)
	}
	if bad := e.verify(); bad != 0 {
		t.Fatalf("Verify after archive = %d", bad)
	}
	if _, err := e.raw().Exec(`UPDATE audit_log SET actor = 'x' WHERE id = ?`, rows[0].ID); err == nil {
		t.Error("archive left the append-only triggers dropped")
	}

	e.reopen()
	e.record(t0.Add(50*time.Hour), "auth.logout", `{}`)
	if bad := e.verify(); bad != 0 {
		t.Fatalf("Verify after appending past the anchor = %d", bad)
	}

	res2, err := archive(ctx, e.db, backups, t0.Add(8*time.Hour), clock)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Count != 4 || res2.FromID != before[4].ID {
		t.Fatalf("second archive = %+v", res2)
	}
	if bad := e.verify(); bad != 0 {
		t.Fatalf("Verify after a second archive = %d", bad)
	}

	res3, err := archive(ctx, e.db, backups, t0.Add(1000*time.Hour), func() time.Time { return t0.Add(49 * time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	rows = e.rows()
	if len(rows) != 1 || rows[0].Action != ActionArchived || rows[0].ID <= res3.ToID {
		t.Fatalf("archiving everything leaves only a fresh anchor, got %+v", rows)
	}
	if bad := e.verify(); bad != 0 {
		t.Fatalf("Verify after archiving everything = %d", bad)
	}
	e.reopen()
	e.record(t0.Add(51*time.Hour), "auth.login", `{}`)
	if bad := e.verify(); bad != 0 {
		t.Fatalf("Verify after appending to an archived-out log = %d", bad)
	}

	none, err := archive(ctx, e.db, backups, t0, clock)
	if err != nil || none.Count != 0 {
		t.Errorf("archive with nothing old = %+v, %v", none, err)
	}
}

func TestArchiveRefusesBrokenChain(t *testing.T) {
	e := newEnv(t)
	for i := range 3 {
		e.record(t0.Add(time.Duration(i)*time.Hour), "auth.login", `{}`)
	}
	raw := e.raw()
	for _, q := range []string{`DROP TRIGGER audit_no_update`, `UPDATE audit_log SET actor = 'x' WHERE id = 2`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := archive(context.Background(), e.db, t.TempDir(), t0.Add(10*time.Hour), time.Now); err == nil ||
		!strings.Contains(err.Error(), "id 2") {
		t.Fatalf("archive of a tampered log = %v", err)
	}
	if n := len(e.rows()); n != 3 {
		t.Errorf("a refused archive deleted rows: %d left", n)
	}
}

func TestArchiveEntryPoint(t *testing.T) {
	e := newEnv(t)
	e.record(t0, "auth.login", `{}`)
	e.record(t0.Add(time.Hour), "auth.login", `{}`)
	if err := e.db.Close(); err != nil {
		t.Fatal(err)
	}
	res, err := Archive(context.Background(), e.path, t0.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 1 || filepath.Dir(res.File) != filepath.Join(filepath.Dir(e.path), "backups") {
		t.Fatalf("Archive = %+v", res)
	}
	e.db = nil
	e.reopen()
	if bad := e.verify(); bad != 0 {
		t.Errorf("Verify after Archive = %d", bad)
	}
	if h := hex.EncodeToString(genesis()); len(h) != 64 {
		t.Errorf("genesis = %s", h)
	}
}

func TestNewRequiresStore(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("New without a store succeeded")
	}
}
