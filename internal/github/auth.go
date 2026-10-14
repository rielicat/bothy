package github

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

const (
	// secretTTL bounds how long a resolved PAT or App key stays in memory
	// before the vault is asked again (each ask is audited as secret.use).
	secretTTL = 5 * time.Minute
	// tokenMargin renews an installation token this long before it expires.
	tokenMargin = 5 * time.Minute
)

// secretFunc yields a connection's secret: resolved from the vault, or the
// raw value of a Test/Connect request that is not stored yet.
type secretFunc func(ctx context.Context) (core.Secret, error)

// tokenSource yields the bearer token for API calls of one connection.
type tokenSource interface {
	token(ctx context.Context) (string, error)
	invalidate()
}

type patSource struct {
	secret secretFunc
	now    func() time.Time

	mu    sync.Mutex
	val   string
	until time.Time
}

func (p *patSource) token(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.val != "" && p.now().Before(p.until) {
		return p.val, nil
	}
	s, err := p.secret(ctx)
	if err != nil {
		return "", fmt.Errorf("github: reading the token: %w", err)
	}
	tok := strings.TrimSpace(s.Reveal())
	if tok == "" || strings.ContainsAny(tok, " \t\r\n") {
		return "", fmt.Errorf("%w: the stored GitHub token is empty or malformed", core.ErrInvalid)
	}
	p.val, p.until = tok, p.now().Add(secretTTL)
	return tok, nil
}

func (p *patSource) invalidate() {
	p.mu.Lock()
	p.val = ""
	p.mu.Unlock()
}

// appSource signs App JWTs with the private key and mints installation
// tokens, cached until shortly before they expire. Minted tokens are
// registered with the redactor, since the vault never sees them.
type appSource struct {
	secret  secretFunc
	appID   string
	install int64
	ep      endpoint
	http    *http.Client
	now     func() time.Time
	red     core.Redactor
	redID   string
	observe func(rateInfo)

	keyMu    sync.Mutex
	key      *rsa.PrivateKey
	keyUntil time.Time

	mu   sync.Mutex
	tok  string
	exp  time.Time
	slot int // alternates the redactor id, so the previous token stays masked until the next renewal
}

func (a *appSource) privateKey(ctx context.Context) (*rsa.PrivateKey, error) {
	a.keyMu.Lock()
	defer a.keyMu.Unlock()
	if a.key != nil && a.now().Before(a.keyUntil) {
		return a.key, nil
	}
	s, err := a.secret(ctx)
	if err != nil {
		return nil, fmt.Errorf("github: reading the App private key: %w", err)
	}
	k, err := parsePrivateKey(s.Bytes())
	if err != nil {
		return nil, err
	}
	a.key, a.keyUntil = k, a.now().Add(secretTTL)
	return k, nil
}

func (a *appSource) jwt(ctx context.Context) (string, error) {
	k, err := a.privateKey(ctx)
	if err != nil {
		return "", err
	}
	return appJWT(k, a.appID, a.now())
}

// appClient calls the /app endpoints, authenticated as the App itself.
func (a *appSource) appClient() *apiClient {
	return &apiClient{ep: a.ep, http: a.http, auth: a.jwt, observe: a.observe, now: a.now}
}

type installationToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (a *appSource) token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tok != "" && a.now().Add(tokenMargin).Before(a.exp) {
		return a.tok, nil
	}
	if a.install <= 0 {
		return "", fmt.Errorf("%w: no App installation chosen", core.ErrInvalid)
	}
	var it installationToken
	path := "/app/installations/" + strconv.FormatInt(a.install, 10) + "/access_tokens"
	if err := a.appClient().call(ctx, http.MethodPost, path, nil, &it); err != nil {
		return "", fmt.Errorf("github: minting an installation token: %w", err)
	}
	if it.Token == "" || it.ExpiresAt.IsZero() {
		return "", errors.New("github: the installation token response has no token or expiry")
	}
	if a.red != nil {
		a.slot = 1 - a.slot
		a.red.Add(a.redID+":"+strconv.Itoa(a.slot), []byte(it.Token))
	}
	a.tok, a.exp = it.Token, it.ExpiresAt
	return a.tok, nil
}

// setInstallation picks the installation a probe found when none was
// given; the source may already be serving tool calls.
func (a *appSource) setInstallation(id int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.install != id {
		a.install, a.tok = id, ""
	}
}

func (a *appSource) invalidate() {
	a.mu.Lock()
	a.tok = ""
	a.mu.Unlock()
	a.keyMu.Lock()
	a.key = nil
	a.keyMu.Unlock()
}

// forget drops the cached token and unregisters it from the redactor.
func (a *appSource) forget() {
	a.invalidate()
	if a.red != nil {
		a.red.Remove(a.redID + ":0")
		a.red.Remove(a.redID + ":1")
	}
}
