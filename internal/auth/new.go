package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

type Deps struct {
	Store    core.Store
	Config   config.View
	Audit    core.Auditor
	Bus      core.Bus
	Redactor core.Redactor
	DataDir  string // setup-token file location
	Clock    core.Clock
	Log      *slog.Logger
}

// Service implements core.Auth and core.CheckProvider.
type Service struct {
	store    core.Store
	cfg      config.View
	audit    core.Auditor
	bus      core.Bus
	redactor core.Redactor
	dataDir  string
	clock    core.Clock
	log      *slog.Logger
	getenv   func(string) string

	hash  *hasher
	locks *lockouts
	pairs *attempts

	signIn atomic.Pointer[SignInDeps]
	flows  *flowStore
}

var (
	_ core.Auth          = (*Service)(nil)
	_ core.CheckProvider = (*Service)(nil)
)

const (
	pairAttempts       = 5
	pairAttemptsWindow = time.Hour
	touchEvery         = time.Minute
)

func New(d Deps) (*Service, error) {
	if d.Config == nil {
		d.Config = config.Static(config.Default())
	}
	if d.Clock == nil {
		d.Clock = core.SystemClock{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{
		store:    d.Store,
		cfg:      d.Config,
		audit:    d.Audit,
		bus:      d.Bus,
		redactor: d.Redactor,
		dataDir:  d.DataDir,
		clock:    d.Clock,
		log:      d.Log,
		getenv:   os.Getenv,
		hash:     newHasher(paramsFrom(d.Config.Current().Auth.Argon2)),
		locks:    newLockouts(),
		pairs:    newAttempts(pairAttempts, pairAttemptsWindow),
		flows:    newFlowStore(d.Clock),
	}, nil
}

func (s *Service) now() time.Time { return s.clock.Now() }

func (s *Service) conf() *config.Config { return s.cfg.Current() }

// record writes an audit row; failures are logged, never returned, so a
// broken audit sink cannot lock every user out. A detail "ip" goes to the
// row's ip column. The event already happened, so the caller's cancellation
// (a client hanging up) does not drop the row.
func (s *Service) record(ctx context.Context, actor, action, target, outcome string, detail map[string]any) {
	if s.audit == nil {
		return
	}
	e := core.AuditEntry{At: s.now(), Actor: actor, Action: action, Target: target, Outcome: outcome, Detail: json.RawMessage("{}")}
	if ip, ok := detail["ip"].(string); ok {
		e.IP = ip
		delete(detail, "ip")
	}
	if len(detail) > 0 {
		if b, err := json.Marshal(detail); err == nil {
			e.Detail = b
		}
	}
	if err := s.audit.Record(context.WithoutCancel(ctx), e); err != nil {
		s.log.Error("audit record failed", "action", action, "err", err)
	}
}

func (s *Service) publish(e core.Event) {
	if s.bus != nil {
		s.bus.Publish(e)
	}
}

func actorOf(a core.Actor) string {
	if a.Via == "token" && a.TokenID != "" {
		return "token:" + a.TokenID
	}
	return "user:" + string(a.User)
}

func userTarget(u core.UserID) string { return "user:" + string(u) }

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
