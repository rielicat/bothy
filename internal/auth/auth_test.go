package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

const goodPassword = "violin staple orbit meadow"

type env struct {
	s     *Service
	store core.Store
	audit *fakes.Auditor
	bus   *events.Bus
	clock *fakes.Clock
	cfg   *config.Config
	dir   string
}

func newEnv(t *testing.T, mut ...func(*config.Config)) *env {
	t.Helper()
	cfg := config.Default()
	// The minimum the hasher accepts keeps tests fast; the timing test uses defaults.
	cfg.Auth.Argon2 = config.Argon2{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1}
	for _, m := range mut {
		m(cfg)
	}
	e := &env{store: memstore.New(), audit: &fakes.Auditor{}, clock: fakes.NewClock(time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)),
		cfg: cfg, dir: t.TempDir()}
	e.bus = events.New(events.Options{Clock: e.clock})
	s, err := New(Deps{Store: e.store, Config: config.Static(cfg), Audit: e.audit, Bus: e.bus, Redactor: fakes.NewRedactor(),
		DataDir: e.dir, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	s.getenv = func(string) string { return "" }
	e.s = s
	return e
}

var ctx = context.Background()

func isUnauthenticated(err error) bool {
	var u interface{ Unauthenticated() bool }
	return errors.As(err, &u) && u.Unauthenticated()
}

func retryAfter(err error) time.Duration {
	var l interface{ RetryAfter() time.Duration }
	if errors.As(err, &l) {
		return l.RetryAfter()
	}
	return 0
}

// owner runs the whole setup flow and returns the owner actor.
func (e *env) owner(t *testing.T) core.Actor {
	t.Helper()
	tok, created, err := e.s.EnsureSetupToken(ctx)
	if err != nil || !created {
		t.Fatalf("EnsureSetupToken = %v, %v", created, err)
	}
	grant, err := e.s.ClaimSetup(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	_, a, err := e.s.CreateOwner(ctx, grant, "owner", goodPassword, "Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (e *env) user(t *testing.T, by core.Actor, name string, role core.Role) *core.User {
	t.Helper()
	u, err := e.s.CreateUser(ctx, by, name, goodPassword, "", role)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestEnsureSetupTokenOnce(t *testing.T) {
	e := newEnv(t)
	tok, created, err := e.s.EnsureSetupToken(ctx)
	if err != nil || !created || len(tok) != 52 {
		t.Fatalf("first call = %q, %v, %v", tok, created, err)
	}
	b, err := os.ReadFile(filepath.Join(e.dir, "setup-token"))
	if err != nil || strings.TrimSpace(string(b)) != tok {
		t.Fatalf("token file = %q, %v", b, err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(filepath.Join(e.dir, "setup-token")); fi.Mode().Perm() != 0o600 {
			t.Fatalf("token file mode = %v", fi.Mode().Perm())
		}
	}
	if tok2, created, err := e.s.EnsureSetupToken(ctx); err != nil || created || tok2 != "" {
		t.Fatalf("second call = %q, %v, %v", tok2, created, err)
	}
	if h, _ := e.store.Meta().Get(ctx, metaTokenHash); h == "" || strings.Contains(h, tok) {
		t.Fatalf("meta must hold only the hash, got %q", h)
	}
	// Expired tokens are re-minted at boot.
	e.clock.Advance(25 * time.Hour)
	if tok3, created, err := e.s.EnsureSetupToken(ctx); err != nil || !created || tok3 == tok {
		t.Fatalf("after expiry = %q, %v, %v", tok3, created, err)
	}
}

func TestSetupLifecycle(t *testing.T) {
	e := newEnv(t)
	need, err := e.s.NeedsSetup(ctx)
	if err != nil || !need {
		t.Fatalf("NeedsSetup = %v, %v", need, err)
	}
	tok, _, err := e.s.EnsureSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ClaimSetup(ctx, "WRONG"+tok[5:]); !isUnauthenticated(err) {
		t.Fatalf("wrong token: %v", err)
	}
	grant, err := e.s.ClaimSetup(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ClaimSetup(ctx, tok); !isUnauthenticated(err) {
		t.Fatalf("second claim must fail (single use): %v", err)
	}
	csrf, err := e.s.SetupGrant(ctx, grant)
	if err != nil || len(csrf) != 43 {
		t.Fatalf("SetupGrant = %q, %v", csrf, err)
	}
	if _, err := e.s.SetupGrant(ctx, grant+"x"); !isUnauthenticated(err) {
		t.Fatalf("tampered grant: %v", err)
	}
	if _, _, err := e.s.CreateOwner(ctx, grant, "owner", "short", "UTC"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("short password: %v", err)
	}
	for _, tz := range []string{"Mars/Olympus", "Local"} {
		if _, _, err := e.s.CreateOwner(ctx, grant, "owner", goodPassword, tz); !errors.Is(err, core.ErrInvalid) {
			t.Fatalf("bad timezone %q: %v", tz, err)
		}
	}
	cookie, a, err := e.s.CreateOwner(ctx, grant, "owner", goodPassword, "Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	if a.Role != core.RoleOwner || a.Via != "cookie" || a.CSRF == "" || !a.Viewer.Admin || len(a.Viewer.Principals) != 1 {
		t.Fatalf("owner actor = %+v", a)
	}
	if got, err := e.s.FromCookie(ctx, cookie); err != nil || got.User != a.User {
		t.Fatalf("FromCookie = %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "setup-token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("token file should be gone: %v", err)
	}
	if _, _, err := e.s.CreateOwner(ctx, grant, "owner2", goodPassword, "UTC"); err == nil {
		t.Fatal("grant reused")
	}
	if _, err := e.s.RotateSetupToken(ctx); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("rotate with owner: %v", err)
	}
	if _, created, _ := e.s.EnsureSetupToken(ctx); created {
		t.Fatal("token minted although an owner exists")
	}
	if err := e.s.CompleteSetup(ctx, core.Actor{User: a.User, Role: core.RoleMember}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("member completing setup: %v", err)
	}
	if err := e.s.CompleteSetup(ctx, a); err != nil {
		t.Fatal(err)
	}
	if need, _ := e.s.NeedsSetup(ctx); need {
		t.Fatal("NeedsSetup after completion")
	}
	if err := e.s.CompleteSetup(ctx, a); err != nil {
		t.Fatalf("CompleteSetup is idempotent: %v", err)
	}
	want := []string{"auth.setup_claim", "auth.setup_claim", "auth.setup_claim", "auth.owner_created"}
	if got := e.audit.Actions(); !slices.Equal(got, want) {
		t.Fatalf("audit = %v, want %v", got, want)
	}
}

func TestSetupTokenAndGrantExpire(t *testing.T) {
	e := newEnv(t)
	tok, _, _ := e.s.EnsureSetupToken(ctx)
	e.clock.Advance(24*time.Hour + time.Second)
	if _, err := e.s.ClaimSetup(ctx, tok); !isUnauthenticated(err) {
		t.Fatalf("expired token: %v", err)
	}
	tok, err := e.s.RotateSetupToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := e.s.ClaimSetup(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(31 * time.Minute)
	if _, _, err := e.s.CreateOwner(ctx, grant, "owner", goodPassword, "UTC"); !isUnauthenticated(err) {
		t.Fatalf("expired grant: %v", err)
	}
}

func TestPasswordPolicy(t *testing.T) {
	for _, pw := range []string{
		"short", "Password1234!", "password12345", "qwertyuiop12", "aaaaaaaaaaaa", "dr4g0n2024!!", "monkeymonkey",
		"123456789012", "Iloveyou1234", "abcdefghijkl", "P@ssw0rd2026!", "correcthorsebatterystaple", "zxcvbnm,./12",
	} {
		if err := checkPassword(pw, 12); !errors.Is(err, core.ErrInvalid) {
			t.Errorf("%q accepted", pw)
		}
	}
	for _, pw := range []string{goodPassword, "Kx9#mP2$vL7!qR", "the cat sat on 7 mats", "Größenwahn-Kaffee-42"} {
		if err := checkPassword(pw, 12); err != nil {
			t.Errorf("%q rejected: %v", pw, err)
		}
	}
}

func TestPHCFormat(t *testing.T) {
	h := newHasher(argonParams{memory: 8 * 1024, time: 1, threads: 1})
	phc, err := h.hash(ctx, goodPassword)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatalf("phc = %s", phc)
	}
	if ok, err := h.verifyHeld(phc, goodPassword); !ok || err != nil {
		t.Fatalf("verify = %v, %v", ok, err)
	}
	if ok, _ := h.verifyHeld(phc, goodPassword+"x"); ok {
		t.Fatal("wrong password verified")
	}
	// Parameters come from the stored hash, not from the hasher.
	h2 := newHasher(argonParams{memory: 19456, time: 2, threads: 1})
	if ok, _ := h2.verifyHeld(phc, goodPassword); !ok {
		t.Fatal("old parameters no longer verify")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=8192,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"} {
		if _, err := h.verifyHeld(bad, "x"); !errors.Is(err, errPHC) {
			t.Errorf("verify(%q) = %v", bad, err)
		}
	}
}

func TestLoginAndLockout(t *testing.T) {
	e := newEnv(t)
	owner := e.owner(t)
	cookie, a, err := e.s.Login(ctx, "OWNER", goodPassword, "203.0.113.5")
	if err != nil || a.User != owner.User {
		t.Fatalf("login = %+v, %v", a, err)
	}
	if cookie == "" {
		t.Fatal("no cookie")
	}
	for i := 0; i < 4; i++ {
		if _, _, err := e.s.Login(ctx, "owner", "wrong password!", "203.0.113."+string(rune('6'+i))); !isUnauthenticated(err) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	_, _, err = e.s.Login(ctx, "owner", "wrong password!", "203.0.113.20")
	if !isUnauthenticated(err) {
		t.Fatalf("5th attempt: %v", err)
	}
	_, _, err = e.s.Login(ctx, "owner", goodPassword, "203.0.113.21")
	if d := retryAfter(err); d < 59*time.Second || d > time.Minute {
		t.Fatalf("locked login = %v (retry %v)", err, d)
	}
	e.clock.Advance(61 * time.Second)
	_, _, err = e.s.Login(ctx, "owner", "wrong again!!", "203.0.113.22")
	if !isUnauthenticated(err) {
		t.Fatalf("after lock: %v", err)
	}
	_, _, err = e.s.Login(ctx, "owner", goodPassword, "203.0.113.23")
	if d := retryAfter(err); d < 119*time.Second || d > 2*time.Minute {
		t.Fatalf("second lock should double: %v (%v)", err, d)
	}
	e.clock.Advance(2 * time.Minute)
	if _, _, err := e.s.Login(ctx, "owner", goodPassword, "203.0.113.24"); err != nil {
		t.Fatalf("login after lock: %v", err)
	}
	u, _ := e.store.Users().Get(ctx, owner.User)
	if u.FailedLogins != 0 || !u.LockedUntil.IsZero() {
		t.Fatalf("success must reset counters: %+v", u)
	}
	acts := e.audit.Actions()
	if !slices.Contains(acts, "auth.lockout") || !slices.Contains(acts, "auth.login_failed") {
		t.Fatalf("audit = %v", acts)
	}
}

// Guesses sent all at once, from many addresses, must not slip past the
// lock that the fifth failure sets.
func TestConcurrentGuessesHitLockout(t *testing.T) {
	e := newEnv(t)
	e.owner(t)
	const n = 24
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := e.s.Login(ctx, "owner", "wrong password!", "198.51.100."+strconv.Itoa(i+1))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	tried := 0
	for err := range errs {
		switch {
		case isUnauthenticated(err):
			tried++
		case retryAfter(err) == 0:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if tried != 5 {
		t.Fatalf("%d of %d concurrent guesses were checked, want 5 before the lock", tried, n)
	}
}

func TestLoginIPLockoutCoversUnknownUsers(t *testing.T) {
	e := newEnv(t)
	e.owner(t)
	for i := 0; i < 5; i++ {
		if _, _, err := e.s.Login(ctx, "ghost"+string(rune('a'+i)), "whatever pass", "198.51.100.7"); !isUnauthenticated(err) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, _, err := e.s.Login(ctx, "owner", goodPassword, "198.51.100.7"); retryAfter(err) == 0 {
		t.Fatalf("IP should be locked: %v", err)
	}
	if _, _, err := e.s.Login(ctx, "owner", goodPassword, "198.51.100.8"); err != nil {
		t.Fatalf("other IP: %v", err)
	}
	// Unknown usernames lock exactly like known ones, so lockouts do not
	// reveal which accounts exist.
	for i := 0; i < 5; i++ {
		_, _, _ = e.s.Login(ctx, "nobody", "whatever pass", "198.51.100."+string(rune('a'+i)))
	}
	if _, _, err := e.s.Login(ctx, "nobody", "whatever pass", "198.51.100.99"); retryAfter(err) == 0 {
		t.Fatalf("unknown username should lock: %v", err)
	}
}

func TestLoginTimingUnknownUser(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("wall-clock comparison: skipped with -short and under the race detector")
	}
	e := newEnv(t, func(c *config.Config) { c.Auth.Argon2 = config.Default().Auth.Argon2 })
	e.owner(t)
	owner := mustUser(t, e, "owner").ID
	once := func(name string, i int) time.Duration {
		runtime.GC()
		start := time.Now()
		_, _, _ = e.s.Login(ctx, name, "not the password", "192.0.2."+strconv.Itoa(i))
		d := time.Since(start)
		e.s.locks = newLockouts()
		_ = e.store.Users().RecordLogin(ctx, owner, true, time.Time{})
		return d
	}
	once("owner", 0)
	once("nosuchuser", 0)
	// Interleaving cancels drift (thermal, other load) between the two series.
	var known, unknown []time.Duration
	for i := 0; i < 15; i++ {
		known = append(known, once("owner", i))
		unknown = append(unknown, once("nosuchuser", i))
	}
	median := func(ds []time.Duration) time.Duration {
		slices.Sort(ds)
		return ds[len(ds)/2]
	}
	k, u := median(known), median(unknown)
	ratio := float64(u) / float64(k)
	if ratio < 0.9 || ratio > 1.1 {
		t.Fatalf("unknown/known median login time = %v/%v (ratio %.2f), want within 10%%", u, k, ratio)
	}
}

func mustUser(t *testing.T, e *env, name string) *core.User {
	t.Helper()
	u, err := e.store.Users().ByUsername(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestCookieExpiryAndLogout(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Auth.SessionIdle = config.Duration(time.Hour)
		c.Auth.SessionMax = config.Duration(3 * time.Hour)
	})
	e.owner(t)
	cookie, _, err := e.s.Login(ctx, "owner", goodPassword, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		e.clock.Advance(50 * time.Minute)
		if _, err := e.s.FromCookie(ctx, cookie); err != nil && i < 3 {
			t.Fatalf("activity keeps the session alive (step %d): %v", i, err)
		}
	}
	if _, err := e.s.FromCookie(ctx, cookie); !isUnauthenticated(err) {
		t.Fatalf("absolute expiry: %v", err)
	}
	cookie, _, _ = e.s.Login(ctx, "owner", goodPassword, "127.0.0.1")
	e.clock.Advance(61 * time.Minute)
	if _, err := e.s.FromCookie(ctx, cookie); !isUnauthenticated(err) {
		t.Fatalf("idle expiry: %v", err)
	}
	cookie, _, _ = e.s.Login(ctx, "owner", goodPassword, "127.0.0.1")
	if err := e.s.Logout(ctx, cookie); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FromCookie(ctx, cookie); !isUnauthenticated(err) {
		t.Fatalf("after logout: %v", err)
	}
	if _, err := e.s.FromCookie(ctx, "garbage"); !isUnauthenticated(err) {
		t.Fatalf("garbage cookie: %v", err)
	}
}

func TestTokens(t *testing.T) {
	e := newEnv(t)
	owner := e.owner(t)
	member := e.user(t, owner, "member", core.RoleMember)
	mActor := core.Actor{User: member.ID, Role: core.RoleMember, Via: "cookie"}
	if _, _, err := e.s.CreateToken(ctx, mActor, core.TokenSpec{Name: "x", Scopes: []string{"admin"}, TTL: time.Hour}); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("member admin token: %v", err)
	}
	if _, _, err := e.s.CreateToken(ctx, mActor, core.TokenSpec{Name: "x", Scopes: []string{"chat"}}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("no ttl: %v", err)
	}
	if _, _, err := e.s.CreateToken(ctx, mActor, core.TokenSpec{Name: "x", Scopes: []string{"root"}, TTL: time.Hour}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("unknown scope: %v", err)
	}
	plain, rec, err := e.s.CreateToken(ctx, mActor, core.TokenSpec{Name: "cli", Scopes: []string{"read", "chat", "chat"}, TTL: 90 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != patLen || !strings.HasPrefix(plain, "bothy_pat_") || rec.OnApproval != "deny" || rec.RatePerMin != 30 {
		t.Fatalf("token = %q %+v", plain, rec)
	}
	if !slices.Equal(rec.Scopes, []string{"chat", "read"}) {
		t.Fatalf("scopes = %v", rec.Scopes)
	}
	id, _, ok := parsePAT(plain)
	if !ok || core.PrefixToken+"_"+id != rec.ID {
		t.Fatalf("parse = %q %v", id, ok)
	}
	a, err := e.s.FromBearer(ctx, plain)
	if err != nil || a.User != member.ID || a.Via != "token" || a.TokenID != rec.ID || !a.HasScope("chat") || a.HasScope("admin") {
		t.Fatalf("FromBearer = %+v, %v", a, err)
	}
	// A flipped secret character fails the CRC; a re-CRC'd forgery fails the hash.
	flipped := []byte(plain)
	flipped[len(patPrefix)+patIDLen+5] ^= 1
	if _, err := e.s.FromBearer(ctx, string(flipped)); !isUnauthenticated(err) {
		t.Fatalf("tampered token: %v", err)
	}
	forged := formatPAT(id, strings.Repeat("A", patSecretLen))
	if _, err := e.s.FromBearer(ctx, forged); !isUnauthenticated(err) {
		t.Fatalf("forged token: %v", err)
	}
	if err := e.s.RevokeToken(ctx, owner, rec.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("revoking someone else's token: %v", err)
	}
	if err := e.s.RevokeToken(ctx, mActor, rec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FromBearer(ctx, plain); !isUnauthenticated(err) {
		t.Fatalf("revoked token: %v", err)
	}
	plain, _, _ = e.s.CreateToken(ctx, mActor, core.TokenSpec{Name: "short", Scopes: []string{"chat"}, TTL: time.Hour, OnApproval: "wait", RatePerMin: 5})
	if a, _ := e.s.FromBearer(ctx, plain); a.OnApproval != "wait" || a.RatePerMin != 5 {
		t.Fatalf("token options = %+v", a)
	}
	e.clock.Advance(time.Hour)
	if _, err := e.s.FromBearer(ctx, plain); !isUnauthenticated(err) {
		t.Fatalf("expired token: %v", err)
	}
}

// strictAuditor and ctxUsers refuse canceled contexts, as the synchronous
// SQLite writes behind them would.
type strictAuditor struct{ *fakes.Auditor }

func (a strictAuditor) Record(c context.Context, e core.AuditEntry) error {
	if err := c.Err(); err != nil {
		return err
	}
	return a.Auditor.Record(c, e)
}

type ctxUsers struct{ core.UserRepo }

func (u ctxUsers) RecordLogin(c context.Context, id core.UserID, ok bool, until time.Time) error {
	if err := c.Err(); err != nil {
		return err
	}
	return u.UserRepo.RecordLogin(c, id, ok, until)
}

type ctxStore struct{ core.Store }

func (s ctxStore) Users() core.UserRepo { return ctxUsers{s.Store.Users()} }

// A client that hangs up once the hash has run must still be charged the
// failure, and the audit trail must keep it.
func TestFailureRecordsSurviveCancellation(t *testing.T) {
	e := newEnv(t)
	owner := e.owner(t)
	e.s.audit = strictAuditor{e.audit}
	e.s.store = ctxStore{e.store}
	gone, cancel := context.WithCancel(ctx)
	cancel()
	u, err := e.store.Users().Get(ctx, owner.User)
	if err != nil {
		t.Fatal(err)
	}
	e.s.loginFailed(gone, u, "owner", "203.0.113.5", e.clock.Now())
	if u, _ = e.store.Users().Get(ctx, owner.User); u.FailedLogins != 1 {
		t.Fatalf("failed logins = %d, want 1", u.FailedLogins)
	}
	if !slices.Contains(e.audit.Actions(), "auth.login_failed") {
		t.Fatalf("audit = %v", e.audit.Actions())
	}
}

func TestUpdateUserRevokes(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	m := e.user(t, owner, "member", core.RoleMember)
	cookie, ma, err := e.s.Login(ctx, "member", goodPassword, "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	plain, _, _ := e.s.CreateToken(ctx, ma, core.TokenSpec{Name: "t", Scopes: []string{"chat"}, TTL: time.Hour})
	up := *m
	up.Role = core.RoleAdmin
	if err := e.s.UpdateUser(ctx, owner, &up); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FromCookie(ctx, cookie); !isUnauthenticated(err) {
		t.Fatalf("role change must end sessions: %v", err)
	}
	if a, err := e.s.FromBearer(ctx, plain); err != nil || a.Role != core.RoleAdmin {
		t.Fatalf("token after promotion = %+v, %v", a, err)
	}
	cookie, _, _ = e.s.Login(ctx, "member", goodPassword, "10.0.0.2")
	up.Disabled = true
	if err := e.s.UpdateUser(ctx, owner, &up); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FromCookie(ctx, cookie); !isUnauthenticated(err) {
		t.Fatalf("disabled cookie: %v", err)
	}
	if _, err := e.s.FromBearer(ctx, plain); !isUnauthenticated(err) {
		t.Fatalf("disabled token: %v", err)
	}
	if _, _, err := e.s.Login(ctx, "member", goodPassword, "10.0.0.3"); !isUnauthenticated(err) {
		t.Fatalf("disabled login: %v", err)
	}
	ou, _ := e.store.Users().Get(ctx, owner.User)
	ou.Disabled = true
	if err := e.s.UpdateUser(ctx, owner, ou); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("disabling the owner: %v", err)
	}
	ou.Disabled, ou.Role = false, core.RoleMember
	if err := e.s.UpdateUser(ctx, owner, ou); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("demoting the owner: %v", err)
	}
	if err := e.s.UpdateUser(ctx, core.Actor{User: m.ID, Role: core.RoleMember, Via: "cookie"}, &up); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("member editing users: %v", err)
	}
}

func TestSetAndResetPassword(t *testing.T) {
	e := newEnv(t)
	owner := e.owner(t)
	cookie, _, _ := e.s.Login(ctx, "owner", goodPassword, "127.0.0.1")
	if err := e.s.SetPassword(ctx, owner.User, "not it at all", "a brand new passphrase"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("wrong old password: %v", err)
	}
	if u, _ := e.store.Users().Get(ctx, owner.User); u.FailedLogins != 1 {
		t.Fatalf("wrong old password must count: %d", u.FailedLogins)
	}
	if err := e.s.SetPassword(ctx, owner.User, goodPassword, "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.FromCookie(ctx, cookie); !isUnauthenticated(err) {
		t.Fatalf("sessions must be revoked: %v", err)
	}
	if _, _, err := e.s.Login(ctx, "owner", "a brand new passphrase", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ResetPassword(ctx, owner.User, "qwertyuiop123"); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("reset to a common password: %v", err)
	}
	if err := e.s.ResetPassword(ctx, owner.User, "another fresh passphrase"); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.Join(e.audit.Actions(), " "), "auth.password_changed"); n != 2 {
		t.Fatalf("password_changed audited %d times", n)
	}
}

func TestPairingRedeem(t *testing.T) {
	e := newEnv(t)
	owner := e.owner(t)
	sub := e.bus.Subscribe(core.Filter{Principals: []core.PrincipalID{core.UserPrincipal(owner.User)}}, 8)
	defer sub.Close()
	pc, err := e.s.NewPairingCode(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(pc.Code) != 9 || pc.Code[4] != '-' || !pc.ExpiresAt.Equal(e.clock.Now().Add(15*time.Minute)) {
		t.Fatalf("code = %+v", pc)
	}
	ident := core.ChannelIdentity{Channel: "telegram", ExternalID: "42", Username: "alice", DMChat: "42"}
	// Lowercase, no dash and the O/0 alias all normalise to the same code.
	sloppy := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(pc.Code, "-", ""), "0", "o"))
	linked, err := e.s.RedeemPairing(ctx, ident, sloppy)
	if err != nil {
		t.Fatal(err)
	}
	if linked.Status != core.IdentityLinked || linked.User != owner.User {
		t.Fatalf("linked = %+v", linked)
	}
	select {
	case ev := <-sub.C():
		if ev.Type != core.EvPairingLinked || ev.Data.(core.PairingLinkedData).Display != "alice" {
			t.Fatalf("event = %+v", ev)
		}
	default:
		t.Fatal("no pairing.linked event")
	}
	res, _ := e.s.ResolveIdentity(ctx, "telegram", "42")
	if res.Status != core.IdentityLinked || res.User != owner.User || res.Principal != core.UserPrincipal(owner.User) || res.Role != core.RoleOwner {
		t.Fatalf("resolution = %+v", res)
	}
	if _, err := e.s.RedeemPairing(ctx, core.ChannelIdentity{Channel: "telegram", ExternalID: "43"}, pc.Code); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("code reused: %v", err)
	}
	pc2, _ := e.s.NewPairingCode(ctx, owner)
	e.clock.Advance(16 * time.Minute)
	if _, err := e.s.RedeemPairing(ctx, core.ChannelIdentity{Channel: "telegram", ExternalID: "44"}, pc2.Code); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("expired code: %v", err)
	}
}

func TestPairingAttemptLimit(t *testing.T) {
	e := newEnv(t)
	owner := e.owner(t)
	ident := core.ChannelIdentity{Channel: "discord", ExternalID: "999"}
	for i := 0; i < 5; i++ {
		if _, err := e.s.RedeemPairing(ctx, ident, "ZZZZ-ZZZZ"); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	pc, _ := e.s.NewPairingCode(ctx, owner)
	if _, err := e.s.RedeemPairing(ctx, ident, pc.Code); retryAfter(err) == 0 {
		t.Fatalf("6th attempt within the hour must be refused: %v", err)
	}
	e.clock.Advance(61 * time.Minute)
	pc, _ = e.s.NewPairingCode(ctx, owner)
	if _, err := e.s.RedeemPairing(ctx, ident, pc.Code); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

func TestSenderNoticeAndApprove(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	bob := e.user(t, owner, "bob", core.RoleMember)
	stranger := core.ChannelIdentity{Channel: "telegram", ExternalID: "777", DisplayName: "Stranger", DMChat: "777"}
	n, err := e.s.SenderNotice(ctx, stranger)
	if err != nil || !n.Reply || n.Code.Code == "" {
		t.Fatalf("first notice = %+v, %v", n, err)
	}
	if n2, _ := e.s.SenderNotice(ctx, stranger); n2.Reply {
		t.Fatal("second notice within 24h replied")
	}
	if res, _ := e.s.ResolveIdentity(ctx, "telegram", "777"); res.Status != core.IdentityPending {
		t.Fatalf("pending resolution = %+v", res)
	}
	if _, err := e.s.ApproveSender(ctx, core.Actor{User: bob.ID, Role: core.RoleMember, Via: "cookie"}, n.Code.Code, "guest", ""); !errors.Is(err, core.ErrForbidden) {
		t.Fatalf("member approving: %v", err)
	}
	id, err := e.s.ApproveSender(ctx, owner, n.Code.Code, "guest", "")
	if err != nil {
		t.Fatal(err)
	}
	if id.Status != core.IdentityGuest || !core.ValidID(core.PrefixSpace, string(id.Principal)) {
		t.Fatalf("guest = %+v", id)
	}
	sp, err := e.store.Spaces().ByChat(ctx, "telegram", "777")
	if err != nil || sp.Kind != "guest" || sp.Owner != owner.User || !sp.Enabled {
		t.Fatalf("space = %+v, %v", sp, err)
	}
	p, _ := e.store.Principals().Get(ctx, core.SystemViewer(), id.Principal)
	if !p.Restricted() {
		t.Fatalf("guest principal must be restricted: %+v", p)
	}
	if v, _ := e.s.ViewerFor(ctx, owner.User); !slices.Contains(v.Principals, id.Principal) {
		t.Fatalf("owner viewer misses the guest space: %+v", v)
	}
	if res, _ := e.s.ResolveIdentity(ctx, "telegram", "777"); res.Status != core.IdentityGuest || res.Principal != id.Principal {
		t.Fatalf("guest resolution = %+v", res)
	}

	e.clock.Advance(25 * time.Hour)
	other := core.ChannelIdentity{Channel: "slack", ExternalID: "U1", Username: "carol"}
	n, _ = e.s.SenderNotice(ctx, other)
	if _, err := e.s.ApproveSender(ctx, owner, n.Code.Code, "link", ""); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("link without user: %v", err)
	}
	id, err = e.s.ApproveSender(ctx, owner, n.Code.Code, "link", bob.ID)
	if err != nil || id.Status != core.IdentityLinked || id.User != bob.ID {
		t.Fatalf("link = %+v, %v", id, err)
	}
	if got, err := e.s.SetIdentityStatus(ctx, owner, "slack", "U1", core.IdentityBlocked, ""); err != nil || got.Status != core.IdentityBlocked {
		t.Fatalf("block = %+v, %v", got, err)
	}
	if res, _ := e.s.ResolveIdentity(ctx, "slack", "U1"); res.Status != core.IdentityBlocked {
		t.Fatalf("blocked resolution = %+v", res)
	}
	if err := e.s.UnlinkIdentity(ctx, core.Actor{User: bob.ID, Role: core.RoleMember}, "telegram", "777"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unlinking someone else's identity: %v", err)
	}
	if err := e.s.UnlinkIdentity(ctx, owner, "slack", "U1"); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.s.ResolveIdentity(ctx, "slack", "U1"); res.Status != "" {
		t.Fatalf("unlinked resolution = %+v", res)
	}
}

func TestResolveDisabledUserIsBlocked(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Mode = "multi" })
	owner := e.owner(t)
	m := e.user(t, owner, "member", core.RoleMember)
	if _, err := e.store.Identities().Touch(ctx, &core.ChannelIdentity{Channel: "telegram", ExternalID: "5", LastSeen: e.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SetIdentityStatus(ctx, owner, "telegram", "5", core.IdentityLinked, m.ID); err != nil {
		t.Fatal(err)
	}
	m.Disabled = true
	if err := e.s.UpdateUser(ctx, owner, m); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.s.ResolveIdentity(ctx, "telegram", "5"); res.Status != core.IdentityBlocked {
		t.Fatalf("resolution = %+v", res)
	}
}

func TestChecks(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*config.Server)
		env  string
		want []string
	}{
		{"loopback", func(*config.Server) {}, "", nil},
		{"lan listen no tls", func(s *config.Server) { s.Listen = "0.0.0.0:8080" }, "", []string{"server.exposure"}},
		{"behind proxy", func(s *config.Server) {
			s.Listen = "0.0.0.0:8080"
			s.TrustedProxies = []string{"10.0.0.0/8"}
		}, "", nil},
		{"container without https url", func(s *config.Server) {
			s.Listen = "0.0.0.0:8080"
			s.TrustedProxies = []string{"10.0.0.0/8"}
		}, "container", []string{"server.exposure"}},
		{"container with https url", func(s *config.Server) {
			s.Listen = "0.0.0.0:8080"
			s.TrustedProxies = []string{"10.0.0.0/8"}
			s.PublicURL = "https://bothy.example.com"
		}, "container", nil},
		{"native tls", func(s *config.Server) {
			s.Listen = "0.0.0.0:8443"
			s.TLS = config.TLS{CertFile: "c.pem", KeyFile: "k.pem"}
		}, "", nil},
		{"insecure lan", func(s *config.Server) { s.InsecureLAN = true }, "", []string{"server.insecure_lan"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, func(c *config.Config) { tc.mut(&c.Server) })
			e.s.getenv = func(k string) string {
				if k == "BOTHY_DEPLOYMENT" {
					return tc.env
				}
				return ""
			}
			var got []string
			for _, c := range e.s.Checks() {
				for _, f := range c.Run(ctx) {
					if !f.Security || f.ID != c.ID() {
						t.Errorf("finding %+v from check %s", f, c.ID())
					}
					got = append(got, f.ID)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("findings = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTokenChecksAndFix(t *testing.T) {
	e := newEnv(t)
	owner := e.owner(t)
	_, _, _ = e.s.CreateToken(ctx, owner, core.TokenSpec{Name: "adm", Scopes: []string{"admin"}, TTL: time.Hour})
	_, _, _ = e.s.CreateToken(ctx, owner, core.TokenSpec{Name: "old", Scopes: []string{"read"}, TTL: time.Minute})
	run := func(id string) []core.Finding {
		for _, c := range e.s.Checks() {
			if c.ID() == id {
				return c.Run(ctx)
			}
		}
		t.Fatalf("no check %s", id)
		return nil
	}
	if fs := run("tokens.admin_scope"); len(fs) != 1 || fs[0].Severity != core.SevLow {
		t.Fatalf("admin scope = %+v", fs)
	}
	if fs := run("tokens.expired_unrevoked"); len(fs) != 0 {
		t.Fatalf("nothing expired yet: %+v", fs)
	}
	e.clock.Advance(2 * time.Minute)
	fs := run("tokens.expired_unrevoked")
	// The distroless image has no bothy on PATH: the container form names /bothy.
	if len(fs) != 1 || !fs[0].AutoFix || !strings.Contains(fs[0].Fix, "docker compose exec bothy /bothy security-audit --fix") {
		t.Fatalf("expired = %+v", fs)
	}
	for _, c := range e.s.Checks() {
		if c.ID() == "tokens.expired_unrevoked" {
			if err := c.Fix(ctx, fs[0]); err != nil {
				t.Fatal(err)
			}
		} else if err := c.Fix(ctx, core.Finding{}); !errors.Is(err, core.ErrNotImplemented) {
			t.Fatalf("%s Fix = %v", c.ID(), err)
		}
	}
	if fs := run("tokens.expired_unrevoked"); len(fs) != 0 {
		t.Fatalf("after fix: %+v", fs)
	}
}
