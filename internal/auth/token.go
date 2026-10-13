package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"hash/crc32"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

// Personal access tokens: bothy_pat_<26-char id>_<43-char base62 secret>_<6-char
// base62 CRC32>. The id is the tok_<26> record id without its prefix; fields
// are parsed by fixed length because neither field may be split on "_". The
// CRC lets secret scanners recognise the format without a database.
const (
	patPrefix    = "bothy_pat_"
	patIDLen     = 26
	patSecretLen = 43
	patCRCLen    = 6
	patLen       = len(patPrefix) + patIDLen + 1 + patSecretLen + 1 + patCRCLen

	base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

	defaultRatePerMin = 30
	maxRatePerMin     = 10000
)

// Scopes a token may carry (SPEC §8.1).
var tokenScopes = []string{"chat", "llm", "read", "admin"}

func randomBase62(n int) string {
	out := make([]byte, 0, n)
	buf := make([]byte, n*2)
	for len(out) < n {
		_, _ = rand.Read(buf)
		for _, b := range buf {
			// 248 = 4*62: rejecting larger bytes keeps every symbol equally likely.
			if b < 248 && len(out) < n {
				out = append(out, base62[b%62])
			}
		}
	}
	return string(out)
}

func crc62(s string) string {
	v := crc32.ChecksumIEEE([]byte(s))
	out := make([]byte, patCRCLen)
	for i := patCRCLen - 1; i >= 0; i-- {
		out[i] = base62[v%62]
		v /= 62
	}
	return string(out)
}

func formatPAT(id, secret string) string {
	body := patPrefix + id + "_" + secret
	return body + "_" + crc62(body)
}

// parsePAT splits a token by fixed field widths and checks the CRC.
func parsePAT(tok string) (id, secret string, ok bool) {
	if len(tok) != patLen || !strings.HasPrefix(tok, patPrefix) {
		return "", "", false
	}
	rest := tok[len(patPrefix):]
	id, rest = rest[:patIDLen], rest[patIDLen:]
	if rest[0] != '_' {
		return "", "", false
	}
	secret, rest = rest[1:1+patSecretLen], rest[1+patSecretLen:]
	if rest[0] != '_' {
		return "", "", false
	}
	crc := rest[1:]
	if !core.ValidID(core.PrefixToken, core.PrefixToken+"_"+id) || strings.Trim(secret, base62) != "" {
		return "", "", false
	}
	if subtle.ConstantTimeCompare([]byte(crc62(tok[:len(tok)-patCRCLen-1])), []byte(crc)) != 1 {
		return "", "", false
	}
	return id, secret, true
}

// CreateToken issues a personal access token for by.User. The plaintext is
// returned once; only sha256(secret) is stored.
func (s *Service) CreateToken(ctx context.Context, by core.Actor, spec core.TokenSpec) (string, *core.TokenRecord, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 || strings.ContainsFunc(name, isControl) {
		return "", nil, fmt.Errorf("%w: token name must be 1-64 printable characters", core.ErrInvalid)
	}
	if len(spec.Scopes) == 0 {
		return "", nil, fmt.Errorf("%w: choose at least one scope (chat, llm, read, admin)", core.ErrInvalid)
	}
	scopes := slices.Clone(spec.Scopes)
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	for _, sc := range scopes {
		if !slices.Contains(tokenScopes, sc) {
			return "", nil, fmt.Errorf("%w: unknown scope %q", core.ErrInvalid, sc)
		}
		if sc == "admin" && !by.Role.IsAdmin() {
			return "", nil, fmt.Errorf("%w: only admins can create admin-scoped tokens", core.ErrForbidden)
		}
	}
	if spec.TTL <= 0 {
		return "", nil, fmt.Errorf("%w: ttl is required (tokens without expiry are not allowed; the UI default is 90 days)", core.ErrInvalid)
	}
	onApproval := spec.OnApproval
	switch onApproval {
	case "":
		onApproval = "deny"
	case "deny", "wait":
	default:
		return "", nil, fmt.Errorf("%w: on_approval must be deny or wait", core.ErrInvalid)
	}
	rate := spec.RatePerMin
	switch {
	case rate == 0:
		rate = defaultRatePerMin
	case rate < 0 || rate > maxRatePerMin:
		return "", nil, fmt.Errorf("%w: rate_per_min must be between 1 and %d", core.ErrInvalid, maxRatePerMin)
	}
	user, err := s.store.Users().Get(ctx, by.User)
	if err != nil {
		return "", nil, err
	}
	if user.Disabled {
		return "", nil, fmt.Errorf("%w: account disabled", core.ErrForbidden)
	}
	id := core.NewID(core.PrefixToken)
	secret := randomBase62(patSecretLen)
	now := s.now()
	rec := &core.TokenRecord{ID: id, User: by.User, Name: name, SecretHash: sha(secret), Scopes: scopes, OnApproval: onApproval,
		RatePerMin: rate, CreatedAt: now, Expires: now.Add(spec.TTL)}
	if err := s.store.Tokens().Create(ctx, rec); err != nil {
		return "", nil, err
	}
	s.record(ctx, actorOf(by), "token.create", "token:"+id, "ok",
		map[string]any{"name": name, "scopes": scopes, "expires_at": rec.Expires, "on_approval": onApproval})
	return formatPAT(strings.TrimPrefix(id, core.PrefixToken+"_"), secret), rec, nil
}

// RevokeToken revokes one of by.User's tokens (ErrNotFound for anyone else's).
func (s *Service) RevokeToken(ctx context.Context, by core.Actor, id string) error {
	if err := s.store.Tokens().Revoke(ctx, by.User, id, s.now()); err != nil {
		return err
	}
	s.record(ctx, actorOf(by), "token.revoke", "token:"+id, "ok", nil)
	return nil
}

// FromBearer resolves an API token. Every failure is the same error.
func (s *Service) FromBearer(ctx context.Context, token string) (core.Actor, error) {
	id, secret, ok := parsePAT(token)
	if !ok {
		return core.Actor{}, errToken
	}
	rec, err := s.store.Tokens().Get(ctx, core.PrefixToken+"_"+id)
	if errors.Is(err, core.ErrNotFound) {
		return core.Actor{}, errToken
	}
	if err != nil {
		return core.Actor{}, err
	}
	now := s.now()
	if subtle.ConstantTimeCompare(sha(secret), rec.SecretHash) != 1 || !rec.Revoked.IsZero() || !now.Before(rec.Expires) {
		return core.Actor{}, errToken
	}
	u, err := s.store.Users().Get(ctx, rec.User)
	if errors.Is(err, core.ErrNotFound) {
		return core.Actor{}, errToken
	}
	if err != nil {
		return core.Actor{}, err
	}
	if u.Disabled {
		return core.Actor{}, errToken
	}
	if now.Sub(rec.LastUsed) >= touchEvery {
		if err := s.store.Tokens().Touch(ctx, rec.ID, now); err != nil {
			s.log.Warn("touch token", "err", err)
		}
	}
	v, err := s.viewerOf(ctx, u)
	if err != nil {
		return core.Actor{}, err
	}
	return core.Actor{User: u.ID, Role: u.Role, Via: "token", TokenID: rec.ID, Scopes: slices.Clone(rec.Scopes), Viewer: v,
		OnApproval: rec.OnApproval, RatePerMin: rec.RatePerMin}, nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) }

// allTokens lists every token of every user (doctor checks).
func (s *Service) allTokens(ctx context.Context) ([]*core.TokenRecord, error) {
	users, err := s.store.Users().List(ctx)
	if err != nil {
		return nil, err
	}
	var out []*core.TokenRecord
	for _, u := range users {
		ts, err := s.store.Tokens().List(ctx, u.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, ts...)
	}
	return out, nil
}

func tokenActive(t *core.TokenRecord, now time.Time) bool {
	return t.Revoked.IsZero() && now.Before(t.Expires)
}
