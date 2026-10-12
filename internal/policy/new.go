package policy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

type Deps struct {
	Store  core.Store
	Config config.View
	Bus    core.Bus
	Audit  core.Auditor
	Clock  core.Clock
	Log    *slog.Logger
}

// Engine implements core.Policy and core.CheckProvider.
type Engine struct {
	store    core.Store
	cfg      config.View
	audit    core.Auditor
	clock    core.Clock
	log      *slog.Logger
	narrowed *recentSet
}

// Approvals implements core.Approvals.
type Approvals struct {
	store core.Store
	cfg   config.View
	bus   core.Bus
	audit core.Auditor
	clock core.Clock
	log   *slog.Logger
	key   []byte // HMAC key for button data (meta approval_mac_key)

	mu        sync.Mutex
	changed   map[core.ApprovalID]chan struct{}
	callbacks []func(*core.Approval)

	// transMu serialises timeout transitions so an Await timer and the
	// sweeper never park or expire the same approval twice.
	transMu sync.Mutex
}

var (
	_ core.Policy        = (*Engine)(nil)
	_ core.CheckProvider = (*Engine)(nil)
	_ core.Approvals     = (*Approvals)(nil)
)

func New(d Deps) (*Engine, *Approvals, error) {
	if d.Store == nil || d.Config == nil {
		return nil, nil, errors.New("policy: Store and Config are required")
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	key, err := loadMACKey(context.Background(), d.Store.Meta())
	if err != nil {
		return nil, nil, err
	}
	e := &Engine{store: d.Store, cfg: d.Config, audit: d.Audit, clock: d.Clock, log: d.Log, narrowed: newRecentSet(d.Clock.Now)}
	a := &Approvals{
		store: d.Store, cfg: d.Config, bus: d.Bus, audit: d.Audit, clock: d.Clock, log: d.Log, key: key,
		changed: map[core.ApprovalID]chan struct{}{},
	}
	return e, a, nil
}

// NextTaint implements core.Policy. Taint only ever grows: the manifest's
// legs, Untrusted for untrusted output or proxy traffic, and Send once an
// exfiltration-capable action has run.
func (e *Engine) NextTaint(prev core.Taint, spec core.ToolSpec, a *core.Action, res *core.ToolResult, boxEgressed bool) core.Taint {
	t := prev | spec.Manifest.Taint&(core.TaintPrivate|core.TaintUntrusted|core.TaintSend)
	if boxEgressed || res != nil && res.Untrusted {
		t |= core.TaintUntrusted
	}
	if spec.Manifest.SideEffect == core.SEExternalSend || a != nil && (a.SideEffect == core.SEExternalSend || a.Kind == core.ActNet) {
		t |= core.TaintSend
	}
	return t
}

func (e *Engine) record(ctx context.Context, en core.AuditEntry) {
	recordAudit(ctx, e.audit, e.log, en)
}

func recordAudit(ctx context.Context, au core.Auditor, log *slog.Logger, en core.AuditEntry) {
	if au == nil {
		return
	}
	if err := au.Record(ctx, en); err != nil {
		log.ErrorContext(ctx, "policy: audit write failed", "action", en.Action, "target", en.Target, "err", err)
	}
}
