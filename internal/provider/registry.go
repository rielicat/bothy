package provider

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/provider/anthropic"
	"github.com/rielicat/bothy/internal/provider/gemini"
	"github.com/rielicat/bothy/internal/provider/ollama"
	"github.com/rielicat/bothy/internal/provider/openai"
	"github.com/rielicat/bothy/internal/provider/wire"
)

// secretTTL bounds how long a resolved key is reused. Re-resolving on every
// call would write a synchronous secret.use audit row per LLM call; an auth
// failure drops the cached value at once, so a rotated key is picked up on
// the next attempt.
const secretTTL = 5 * time.Minute

// client is what the wire packages implement beyond core.Provider.
type client interface {
	core.Provider
	Discover(ctx context.Context) ([]core.ModelInfo, error)
}

// entry is a built client for one provider config. Providers config is
// reload:"hot": every call re-reads Current() and rebuilds an entry whose
// config changed, so providers added by the setup wizard work at once.
type entry struct {
	id    string
	kind  core.ProviderKind
	api   string            // openai kinds: openai.APIResponses or openai.APIChat
	sig   core.ProviderKind // the SigKind the client writes and accepts back
	base  string
	cfg   config.Provider
	fp    string
	hc    *http.Client
	cl    client
	creds *credentials
}

func defaultBase(kind core.ProviderKind) string {
	switch kind {
	case core.KindAnthropic:
		return anthropic.DefaultBaseURL
	case core.KindOpenAI:
		return openai.DefaultBaseURL
	case core.KindGemini:
		return gemini.DefaultBaseURL
	case core.KindOllama:
		return ollama.DefaultBaseURL
	}
	return ""
}

func validKind(k core.ProviderKind) bool {
	switch k {
	case core.KindAnthropic, core.KindOpenAI, core.KindGemini, core.KindOllama, core.KindOpenAICompat:
		return true
	}
	return false
}

func enabled(p config.Provider) bool { return p.Enabled == nil || *p.Enabled }

// providerConfig returns the enabled provider config with this id.
func (f *Facade) providerConfig(id string) (config.Provider, *config.Config, bool) {
	cfg := f.d.Config.Current()
	if cfg == nil {
		return config.Provider{}, nil, false
	}
	for _, p := range cfg.Providers {
		if p.ID == id && enabled(p) && validKind(core.ProviderKind(p.Type)) {
			return p, cfg, true
		}
	}
	return config.Provider{}, cfg, false
}

// entry returns the client for an enabled provider, building or rebuilding
// it when its config changed.
func (f *Facade) entry(id string) (*entry, error) {
	p, _, ok := f.providerConfig(id)
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured or not enabled: %w", id, core.ErrNotFound)
	}
	// APIKey and Headers hold "secret:<name>" references (config rejects
	// plaintext), so the fingerprint carries no secret value.
	fpb, _ := json.Marshal(p) //nolint:gosec // G117: see above
	fp := string(fpb)
	f.mu.Lock()
	old := f.entries[id]
	f.mu.Unlock()
	if old != nil && old.fp == fp {
		return old, nil
	}
	// Built without f.mu: Egress is another module and must not be called
	// under the facade's lock.
	e, err := f.build(p, fp)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	if cur := f.entries[id]; cur != nil && cur.fp == fp {
		f.mu.Unlock()
		e.hc.CloseIdleConnections()
		return cur, nil
	}
	old = f.entries[id]
	f.entries[id] = e
	delete(f.disc, id)
	// A changed config (a new key, another base URL) deserves a fresh start:
	// the old breaker and auth state described the old settings.
	delete(f.health, id)
	f.mu.Unlock()
	f.effort.Forget(id)
	if old != nil {
		// Each entry owns a cloned transport; the replaced one's idle
		// connections would otherwise linger.
		old.hc.CloseIdleConnections()
		f.updateBanner()
	}
	return e, nil
}

func (f *Facade) build(p config.Provider, fp string) (*entry, error) {
	kind := core.ProviderKind(p.Type)
	base := strings.TrimRight(p.BaseURL, "/")
	if base == "" {
		base = defaultBase(kind)
	}
	if base == "" {
		return nil, fmt.Errorf("provider %q: base_url is required: %w", p.ID, core.ErrInvalid)
	}
	hc, err := f.httpClient("provider:"+p.ID, base, p.AllowPrivate)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", p.ID, err)
	}
	creds := &credentials{f: f, id: p.ID, keyRef: p.APIKey, headers: p.Headers}
	o := wire.Options{
		ID: p.ID, Kind: kind, BaseURL: base, HTTP: hc,
		Key: creds.key, Redact: creds.redact, Discover: p.Discover,
	}
	if len(p.Headers) > 0 {
		o.Headers = creds.headerValues
	}
	for _, m := range p.Models {
		o.Models = append(o.Models, core.ModelInfo{Ref: core.ModelRef{Provider: p.ID, Model: m.ID}, Source: "config"})
	}
	api := apiFor(kind, p.API)
	cl := f.newClient(o, api, f.effort)
	return &entry{id: p.ID, kind: kind, api: api, sig: sigKind(cl), base: base, cfg: p, fp: fp, hc: hc, creds: creds, cl: cl}, nil
}

// apiFor resolves providers[].api for the OpenAI kinds; other kinds have one
// wire API and report "".
func apiFor(kind core.ProviderKind, api string) string {
	if kind != core.KindOpenAI && kind != core.KindOpenAICompat {
		return ""
	}
	return openai.ResolveAPI(kind, api)
}

func (f *Facade) newClient(o wire.Options, api string, effort *openai.EffortMemory) client {
	switch o.Kind {
	case core.KindAnthropic:
		return anthropic.New(o)
	case core.KindGemini:
		return gemini.New(o)
	case core.KindOllama:
		return ollama.New(o)
	}
	if api == openai.APIResponses {
		cp := f.cat.Find(o.Kind, o.BaseURL)
		return openai.NewResponses(o, func(model string) bool {
			m, ok := cp.Model(model)
			return ok && !m.Thinking
		})
	}
	return openai.NewChat(o, effort)
}

// sigKind is the SigKind a client's parts carry: its provider kind, unless
// the client writes signatures another client of that kind cannot read.
func sigKind(cl client) core.ProviderKind {
	if s, ok := cl.(interface{ SigKind() core.ProviderKind }); ok {
		return s.SigKind()
	}
	return cl.Kind()
}

// hostPort returns the exact "host:port" Egress must allow for a base URL.
func hostPort(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid base_url %q: %w", base, core.ErrInvalid)
	}
	port := u.Port()
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("base_url %q: scheme must be http or https: %w", base, core.ErrInvalid)
	case u.User != nil:
		return "", fmt.Errorf("base_url must not carry credentials: %w", core.ErrInvalid)
	case u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return "", fmt.Errorf("base_url %q must not have a query or fragment: %w", base, core.ErrInvalid)
	case port == "" && u.Scheme == "https":
		port = "443"
	case port == "":
		port = "80"
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port), nil
}

// httpClient gets the egress-guarded client for base and hardens a copy: no
// proxy, TLS 1.2 or newer, no cookie jar, no client-wide timeout (streams
// outlive any fixed one; the caller's context bounds them) and no redirects,
// since the key headers would follow a redirect to another host. The copy
// has its own connection pool; short-lived users close its idle connections.
func (f *Facade) httpClient(name, base string, allowPrivate bool) (*http.Client, error) {
	hp, err := hostPort(base)
	if err != nil {
		return nil, err
	}
	c := f.d.Egress.SystemClient(core.SystemDest{Name: name, Hosts: []string{hp}, AllowPrivate: allowPrivate})
	if c == nil {
		return nil, errors.New("egress returned no client")
	}
	return harden(c), nil
}

func harden(c *http.Client) *http.Client {
	out := *c
	out.Jar = nil
	out.Timeout = 0
	out.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	rt := c.Transport
	if rt == nil {
		// A nil transport means http.DefaultTransport, which honours
		// HTTP(S)_PROXY from the environment.
		rt = http.DefaultTransport
	}
	if t, ok := rt.(*http.Transport); ok {
		t = t.Clone()
		t.Proxy = nil
		if t.TLSClientConfig == nil {
			t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else if t.TLSClientConfig.MinVersion < tls.VersionTLS12 {
			t.TLSClientConfig.MinVersion = tls.VersionTLS12
		}
		out.Transport = t
	}
	return &out
}

// credentials resolves a provider's secret references through the vault
// (global scope: providers are instance-wide) and caches them briefly.
type credentials struct {
	f       *Facade
	id      string
	keyRef  string
	headers map[string]string

	mu      sync.Mutex
	secret  core.Secret
	hdrs    map[string]core.Secret
	expires time.Time
}

func (c *credentials) resolve(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.f.clock.Now()
	if now.Before(c.expires) {
		return nil
	}
	if c.f.d.Vault == nil && (c.keyRef != "" || len(c.headers) > 0) {
		return errors.New("vault unavailable")
	}
	purpose := "provider:" + c.id
	var key core.Secret
	if c.keyRef != "" {
		s, err := c.f.d.Vault.Resolve(ctx, c.keyRef, "", purpose)
		if err != nil {
			return err
		}
		key = s
	}
	hdrs := make(map[string]core.Secret, len(c.headers))
	for name, ref := range c.headers {
		s, err := c.f.d.Vault.Resolve(ctx, ref, "", purpose)
		if err != nil {
			return fmt.Errorf("header %s: %w", name, err)
		}
		hdrs[name] = s
	}
	c.secret, c.hdrs, c.expires = key, hdrs, now.Add(secretTTL)
	return nil
}

func (c *credentials) key(ctx context.Context) (core.Secret, error) {
	if err := c.resolve(ctx); err != nil {
		return core.Secret{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.secret, nil
}

func (c *credentials) headerValues(ctx context.Context) (map[string]core.Secret, error) {
	if err := c.resolve(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hdrs, nil
}

func (c *credentials) invalidate() {
	c.mu.Lock()
	c.expires = time.Time{}
	c.mu.Unlock()
}

// redact masks the resolved values in provider messages (some vendors echo
// part of a rejected key) on top of the vault's redactor.
func (c *credentials) redact(s string) string {
	c.mu.Lock()
	vals := make([]string, 0, 1+len(c.hdrs))
	if !c.secret.IsZero() {
		vals = append(vals, c.secret.Reveal())
	}
	for _, h := range c.hdrs {
		vals = append(vals, h.Reveal())
	}
	c.mu.Unlock()
	for _, v := range vals {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	if c.f.d.Vault != nil {
		if r := c.f.d.Vault.Redactor(); r != nil {
			s = r.Redact(s)
		}
	}
	return s
}
