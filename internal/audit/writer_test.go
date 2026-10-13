package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/core"
	"github.com/bothyhq/bothy/internal/store"
)

// TestWritersInTwoProcessesShareChain: a CLI process appends with its own
// Writer while the gateway's Writer keeps running; neither may chain to a
// head it remembered from before the other's append.
func TestWritersInTwoProcessesShareChain(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cli, err := store.Open(ctx, store.Options{Path: e.path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	other, err := New(Deps{Store: cli})
	if err != nil {
		t.Fatal(err)
	}
	for i, w := range []*Writer{e.w, other, e.w, other, e.w} {
		if err := w.Record(ctx, core.AuditEntry{Actor: "system", Action: "auth.login", Detail: json.RawMessage(fmt.Sprintf(`{"i":%d}`, i))}); err != nil {
			t.Fatal(err)
		}
	}
	if bad := e.verify(); bad != 0 {
		t.Errorf("Verify with two writers = %d", bad)
	}
	if n := len(e.rows()); n != 5 {
		t.Errorf("rows = %d, want 5", n)
	}
}

// TestRedactionKeepsDetailJSON: a registered value that also occurs inside a
// number or a key must not make the detail invalid JSON, or every sync entry
// mentioning it (secret.use, auth.*) would be refused.
func TestRedactionKeepsDetailJSON(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.red.Add("pin", []byte("1234"))
	if err := e.w.Record(ctx, core.AuditEntry{Actor: "system", Action: "secret.use",
		Detail: json.RawMessage(`{"n":12345,"s":"a1234b","1234":[true,1234.5],"ok":true}`)}); err != nil {
		t.Fatalf("Record = %v", err)
	}
	rows := e.rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	const want = `{"[redacted]":[true,"[redacted].5"],"n":"[redacted]5","ok":true,"s":"a[redacted]b"}`
	if got := string(rows[0].Detail); got != want {
		t.Errorf("detail = %s\nwant     %s", got, want)
	}
	if err := e.w.Record(ctx, core.AuditEntry{Actor: "system", Action: "x", Detail: json.RawMessage(`{"a":1} {"b":2}`)}); !errors.Is(err, core.ErrInvalid) {
		t.Errorf("trailing JSON value accepted: %v", err)
	}
}

// flakyStore fails every transaction while fail is set.
type flakyStore struct {
	core.Store
	fail atomic.Bool
}

func (s *flakyStore) Tx(ctx context.Context, fn func(core.Store) error) error {
	if s.fail.Load() {
		return errors.New("database unavailable")
	}
	return s.Store.Tx(ctx, fn)
}

// TestFailedSyncRecordIsNotReplayed: a synchronous entry whose write failed
// was reported to the caller, which acted on it, so a later flush must not
// write it after all; batched entries are retried, within a bound.
func TestFailedSyncRecordIsNotReplayed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	fs := &flakyStore{Store: e.db}
	w, err := New(Deps{Store: fs})
	if err != nil {
		t.Fatal(err)
	}
	fs.fail.Store(true)
	if err := w.Record(ctx, core.AuditEntry{Actor: "system", Action: "tool.exec"}); err != nil {
		t.Fatalf("batched Record = %v", err)
	}
	if err := w.Record(ctx, core.AuditEntry{Actor: "system", Action: "secret.use", Detail: json.RawMessage(`{"name":"k"}`)}); err == nil {
		t.Fatal("sync Record succeeded although the database refused it")
	}
	fs.fail.Store(false)
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	rows := e.rows()
	if len(rows) != 1 || rows[0].Action != "tool.exec" {
		t.Fatalf("rows after recovery = %+v, want only the batched entry", rows)
	}

	fs.fail.Store(true)
	for range maxHeld + 50 {
		_ = w.Record(ctx, core.AuditEntry{At: t0, Actor: "system", Action: "tool.exec"})
	}
	w.mu.Lock()
	n := len(w.queue)
	w.mu.Unlock()
	if n != maxHeld {
		t.Errorf("queue holds %d entries after the database refused writes, want the bound %d", n, maxHeld)
	}
}

// TestVerifyCatchesRenumberedRow: the hash covers the row id.
func TestVerifyCatchesRenumberedRow(t *testing.T) {
	e := newEnv(t)
	for i := range 3 {
		e.record(t0.Add(time.Duration(i)*time.Second), "auth.login", `{}`)
	}
	raw := e.raw()
	for _, q := range []string{`DROP TRIGGER audit_no_update`, `UPDATE audit_log SET id = 10 WHERE id = 3`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if bad := e.verify(); bad != 10 {
		t.Errorf("Verify = %d, want 10", bad)
	}
}

// TestVerifyReportsUnparseableDetail: a detail edited into invalid JSON (with
// CHECK constraints ignored) is reported as the bad row, not as an error.
func TestVerifyReportsUnparseableDetail(t *testing.T) {
	e := newEnv(t)
	for i := range 3 {
		e.record(t0.Add(time.Duration(i)*time.Second), "auth.login", `{}`)
	}
	raw := e.raw()
	raw.SetMaxOpenConns(1)
	for _, q := range []string{`DROP TRIGGER audit_no_update`, `PRAGMA ignore_check_constraints = ON`,
		`UPDATE audit_log SET detail = '{"broken"' WHERE id = 2`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if bad := e.verify(); bad != 2 {
		t.Errorf("Verify = %d, want 2", bad)
	}
}
