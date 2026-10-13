package auth

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/bothyhq/bothy/internal/core"
)

type check struct {
	id  string
	run func(ctx context.Context) []core.Finding
	fix func(ctx context.Context, f core.Finding) error
}

func (c check) ID() string                             { return c.id }
func (c check) Run(ctx context.Context) []core.Finding { return c.run(ctx) }

func (c check) Fix(ctx context.Context, f core.Finding) error {
	if c.fix == nil {
		return core.ErrNotImplemented
	}
	return c.fix(ctx, f)
}

// Checks returns server.exposure, server.insecure_lan, tokens.admin_scope and
// tokens.expired_unrevoked (auto-fix: revoke).
func (s *Service) Checks() []core.Check {
	return []core.Check{
		check{id: "server.exposure", run: s.checkExposure},
		check{id: "server.insecure_lan", run: s.checkInsecureLAN},
		check{id: "tokens.admin_scope", run: s.checkAdminTokens},
		check{id: "tokens.expired_unrevoked", run: s.checkExpiredTokens, fix: s.fixExpiredTokens},
	}
}

func loopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = listen
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func unspecifiedListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "" || (ip != nil && ip.IsUnspecified())
}

func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func (s *Service) checkExposure(context.Context) []core.Finding {
	c := s.conf().Server
	tls := c.TLS.CertFile != "" && c.TLS.KeyFile != ""
	proxied := len(c.TrustedProxies) > 0
	var why string
	switch {
	case tls || loopbackListen(c.Listen):
		return nil
	// Docker's userland proxy can make remote clients appear to come from the
	// bridge gateway, which the exposure gate treats as local (SPEC §13.1).
	case s.getenv("BOTHY_DEPLOYMENT") == "container" && unspecifiedListen(c.Listen) && !httpsURL(c.PublicURL):
		why = "container mode listens on " + c.Listen + " without an https public_url"
	case !proxied:
		why = "server.listen is " + c.Listen + " with neither TLS nor a trusted reverse proxy"
	default:
		return nil
	}
	return []core.Finding{{
		ID: "server.exposure", Area: "server", Severity: core.SevHigh, Security: true,
		Title:  "The web UI may be reachable without TLS",
		Detail: why + ". Plain HTTP is refused for public peers, but the listener is still exposed beyond this host.",
		Fix:    "Publish only on 127.0.0.1 (compose: 127.0.0.1:8080:8080), or terminate TLS (server.tls, or Caddy/Tailscale listed in server.trusted_proxies with an https server.public_url).",
	}}
}

func (s *Service) checkInsecureLAN(context.Context) []core.Finding {
	if !s.conf().Server.InsecureLAN {
		return nil
	}
	return []core.Finding{{
		ID: "server.insecure_lan", Area: "server", Severity: core.SevMedium, Security: true,
		Title:  "Plain HTTP is allowed from the local network",
		Detail: "server.insecure_lan lets RFC1918/ULA peers sign in over unencrypted HTTP; anyone on the network can read session cookies.",
		Fix:    "Set server.insecure_lan: false and use TLS (Caddy, Tailscale or server.tls).",
	}}
}

func (s *Service) checkAdminTokens(ctx context.Context) []core.Finding {
	ts, err := s.allTokens(ctx)
	if err != nil {
		return []core.Finding{checkError("tokens.admin_scope", "tokens", err)}
	}
	now := s.now()
	n := 0
	for _, t := range ts {
		if tokenActive(t, now) && slices.Contains(t.Scopes, "admin") {
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return []core.Finding{{
		ID: "tokens.admin_scope", Area: "tokens", Severity: core.SevLow, Security: true,
		Title:  "API tokens with the admin scope",
		Detail: fmt.Sprintf("%d active token(s) can change configuration, users and secrets. Prefer chat, llm or read scopes.", n),
		Fix:    "/settings/tokens",
	}}
}

func (s *Service) expiredTokens(ctx context.Context) ([]*core.TokenRecord, error) {
	ts, err := s.allTokens(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	return slices.DeleteFunc(ts, func(t *core.TokenRecord) bool { return !t.Revoked.IsZero() || now.Before(t.Expires) }), nil
}

func (s *Service) checkExpiredTokens(ctx context.Context) []core.Finding {
	ts, err := s.expiredTokens(ctx)
	if err != nil {
		return []core.Finding{checkError("tokens.expired_unrevoked", "tokens", err)}
	}
	if len(ts) == 0 {
		return nil
	}
	return []core.Finding{{
		ID: "tokens.expired_unrevoked", Area: "tokens", Severity: core.SevLow, Security: true, AutoFix: true,
		Title:  "Expired API tokens are not revoked",
		Detail: fmt.Sprintf("%d expired token(s) no longer work but are still listed as active records.", len(ts)),
		Fix:    "bothy security-audit --fix",
	}}
}

func (s *Service) fixExpiredTokens(ctx context.Context, _ core.Finding) error {
	ts, err := s.expiredTokens(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for _, t := range ts {
		if err := s.store.Tokens().Revoke(ctx, t.User, t.ID, now); err != nil {
			return err
		}
		s.record(ctx, "system", "token.revoke", "token:"+t.ID, "ok", map[string]any{"reason": "expired"})
	}
	return nil
}

func checkError(id, area string, err error) core.Finding {
	return core.Finding{ID: id, Area: area, Severity: core.SevMedium, Title: "Check could not run", Detail: err.Error()}
}
