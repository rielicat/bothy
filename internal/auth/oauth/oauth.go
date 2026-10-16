// Package oauth is the protocol half of signing in with Google and GitHub:
// PKCE, authorization addresses, the server-side code exchange and the
// sign-in list check. It holds no state and knows nothing of the
// store, the vault or the configuration; internal/auth runs the flows. Every
// request goes through the egress system client it is given, never follows
// a redirect, and no token appears in an error or a log line.
package oauth

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

const (
	callTimeout = 15 * time.Second
	maxToken    = 64 << 10
	maxAPI      = 1 << 20
	userAgent   = "Bothy"
)

// Endpoints is where one provider is reached.
type Endpoints struct {
	AuthURL  string // the browser-facing authorize endpoint
	TokenURL string
	APIBase  string   // GitHub REST base; "" for Google
	Hosts    []string // exact host:port pairs for the egress SystemDest
	Private  bool     // a loopback test override: SystemDest.AllowPrivate
}

// GoogleEndpoints returns Google's production endpoints for "", or a test
// server's: apiBase must then be http(s) to a loopback host.
func GoogleEndpoints(apiBase string) (Endpoints, error) {
	if apiBase == "" {
		return Endpoints{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token",
			Hosts: []string{"oauth2.googleapis.com:443"}}, nil
	}
	base, host, err := override(apiBase)
	if err != nil {
		return Endpoints{}, err
	}
	return Endpoints{AuthURL: base + "/o/oauth2/v2/auth", TokenURL: base + "/token", Hosts: []string{host}, Private: true}, nil
}

// GitHubEndpoints is GoogleEndpoints for GitHub: the authorize and token
// endpoints on github.com, the REST API on api.github.com.
func GitHubEndpoints(apiBase string) (Endpoints, error) {
	if apiBase == "" {
		return Endpoints{AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token",
			APIBase: "https://api.github.com", Hosts: []string{"github.com:443", "api.github.com:443"}}, nil
	}
	base, host, err := override(apiBase)
	if err != nil {
		return Endpoints{}, err
	}
	return Endpoints{AuthURL: base + "/login/oauth/authorize", TokenURL: base + "/login/oauth/access_token", APIBase: base,
		Hosts: []string{host}, Private: true}, nil
}

// override checks a test base URL the way config validation does, so a
// value that slipped past it still cannot point sign-in elsewhere.
func override(raw string) (base, hostport string, err error) {
	u, err := url.Parse(raw)
	switch {
	case err != nil, u.Scheme != "http" && u.Scheme != "https", u.Host == "":
		return "", "", fmt.Errorf("oauth: invalid api_base %q: %w", raw, core.ErrInvalid)
	case u.User != nil, u.RawQuery != "", u.Fragment != "":
		return "", "", fmt.Errorf("oauth: api_base must not carry credentials, a query or a fragment: %w", core.ErrInvalid)
	case !config.PlainHTTPAllowed(u.Hostname()):
		return "", "", fmt.Errorf("oauth: api_base must be a loopback address (tests only): %w", core.ErrInvalid)
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), net.JoinHostPort(u.Hostname(), port), nil
}

// AuthRequest is what goes into the authorize address.
type AuthRequest struct {
	ClientID, RedirectURI, State, Nonce, Challenge string
	DomainHint                                     string // Google: hd= when the only entry on the list is a domain
}

// Exchange is a code to trade for the person's identity.
type Exchange struct {
	Code, Verifier, RedirectURI, ClientID string
	Secret                                core.Secret
	Nonce                                 string // Google only
}

// Rules is who may sign in.
type Rules struct {
	// Allow is auth.sign_in.allow: exact addresses ("pat@gmail.com") and
	// domains ("acme.com"). Google matches an address against the verified
	// email and a domain against the hd claim. GitHub matches only addresses,
	// against the account's verified ones: GitHub confirms an address once and
	// never again, so former staff keep a confirmed company address, and
	// anyone who can read a shared inbox at a domain can confirm an address there.
	Allow []string
	Orgs  []string // GitHub: active members of any of these may also sign in
}

// Identity is who the provider says signed in. Google and GitHub return it
// filled in alongside ErrNotAllowed and ErrOrgAccess too, so the refusal
// can name the account.
type Identity struct {
	Subject string // Google sub; GitHub id as decimal
	// Email is Google's verified address. For GitHub it is the verified
	// address that matched Rules.Allow, else the primary verified one, or "".
	Email string
	Login string // GitHub login
	Name  string // the display name the provider reports; may be empty
}

// asciiKey is s lower-cased for comparing with the sign-in list, or false
// when s holds anything but printable ASCII. List entries are ASCII, and
// strings.EqualFold would match U+212A (KELVIN SIGN) to "k" and U+017F (long
// s) to "s", letting a provider address that only folds to a listed one in.
func asciiKey(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	for i := range len(s) {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return "", false
		}
	}
	return strings.ToLower(s), true
}

// listedAddress reports whether email is on allow as an exact address.
func listedAddress(allow []string, email string) bool {
	k, ok := asciiKey(email)
	return ok && strings.Contains(k, "@") && listed(allow, k, true)
}

// listedDomain reports whether domain is on allow as a domain entry.
func listedDomain(allow []string, domain string) bool {
	k, ok := asciiKey(domain)
	return ok && !strings.Contains(k, "@") && listed(allow, k, false)
}

func listed(allow []string, key string, address bool) bool {
	return slices.ContainsFunc(allow, func(e string) bool {
		ek, ok := asciiKey(e)
		return ok && strings.Contains(ek, "@") == address && ek == key
	})
}

// DomainHint is the Google hd= hint for allow: its only entry when that is a
// domain. With an address on the list the hint would hide the accounts
// people need to pick.
func DomainHint(allow []string) string {
	if len(allow) == 1 && !strings.Contains(allow[0], "@") {
		return allow[0]
	}
	return ""
}

// Client talks to the providers. HTTP is egress.SystemClient.
type Client struct {
	HTTP func(core.SystemDest) *http.Client
	Now  func() time.Time
	Log  *slog.Logger
}

func (c *Client) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c *Client) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log
}

// client returns the egress client for dest with redirects refused: a
// provider answering with one is never followed to wherever it points.
func (c *Client) client(name string, ep Endpoints) (*http.Client, error) {
	if c.HTTP == nil {
		return nil, fmt.Errorf("oauth: no HTTP client: %w", core.ErrNotImplemented)
	}
	hc := c.HTTP(core.SystemDest{Name: name, Hosts: ep.Hosts, AllowPrivate: ep.Private})
	if hc == nil {
		return nil, fmt.Errorf("oauth: no HTTP client: %w", core.ErrNotImplemented)
	}
	cp := *hc
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cp, nil
}

// do sends req with a per-call timeout and reads at most limit bytes of the
// answer.
func do(ctx context.Context, hc *http.Client, op string, req *http.Request, limit int64) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	req.Header.Set("User-Agent", userAgent)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, &ProviderError{Op: op, Err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return resp.StatusCode, nil, &ProviderError{Op: op, Status: resp.StatusCode, Err: err}
	}
	return resp.StatusCode, body, nil
}

func postForm(ctx context.Context, hc *http.Client, op, target string, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, &ProviderError{Op: op, Err: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return do(ctx, hc, op, req, maxToken)
}
