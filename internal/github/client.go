package github

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rielicat/bothy/internal/core"
)

const (
	apiVersion   = "2022-11-28"
	userAgent    = "Bothy (+https://bothy.dev)"
	acceptJSON   = "application/vnd.github+json"
	maxJSONBody  = 16 << 20
	maxErrorBody = 64 << 10
	maxAPIHops   = 3
)

// endpoint is the configured GitHub: github.com or an Enterprise Server.
type endpoint struct {
	base  *url.URL // API base without a trailing slash
	hosts []string // host:port the system client may dial
	// private admits private addresses: a non-default api_base is a locked,
	// operator-set value that may sit on an internal network.
	private bool
}

func newEndpoint(apiBase string) (endpoint, error) {
	u, err := url.Parse(strings.TrimRight(apiBase, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return endpoint{}, fmt.Errorf("%w: github.api_base %q is not an http(s) URL", core.ErrInvalid, apiBase)
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	host := strings.ToLower(u.Hostname())
	ep := endpoint{base: u, private: host != "api.github.com"}
	ep.hosts = append(ep.hosts, net.JoinHostPort(host, port))
	// Archive downloads redirect to codeload: codeload.github.com for
	// github.com; Enterprise Server serves them from its own host or, with
	// subdomain isolation, from codeload.<host>.
	switch {
	case host == "api.github.com":
		ep.hosts = append(ep.hosts, net.JoinHostPort("codeload.github.com", port))
	case net.ParseIP(host) == nil:
		ep.hosts = append(ep.hosts, net.JoinHostPort("codeload."+host, port))
	}
	return ep, nil
}

func (e endpoint) allowed(u *url.URL) bool {
	if u.User != nil || u.Scheme != "https" && (u.Scheme != "http" || e.base.Scheme != "http") {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	hp := net.JoinHostPort(strings.ToLower(u.Hostname()), port)
	for _, h := range e.hosts {
		if h == hp {
			return true
		}
	}
	return false
}

// sameAPI reports whether u is on the API host, where the token may go.
func (e endpoint) sameAPI(u *url.URL) bool {
	return u.User == nil && u.Scheme == e.base.Scheme && strings.EqualFold(u.Host, e.base.Host)
}

func (e endpoint) url(path string, q url.Values) *url.URL {
	u := *e.base
	u.Path = e.base.Path + path
	u.RawPath = ""
	if strings.Contains(path, "%") {
		u.RawPath = e.base.EscapedPath() + path
		if p, err := url.PathUnescape(u.RawPath); err == nil {
			u.Path = p
		}
	}
	u.RawQuery = q.Encode()
	return &u
}

func (e endpoint) apiHost() (string, int) {
	h, p, _ := net.SplitHostPort(e.hosts[0])
	port, _ := strconv.Atoi(p)
	return h, port
}

// rateInfo is what the x-ratelimit-* headers said.
type rateInfo struct {
	limit, remaining int
	reset            time.Time
	resource         string
	ok               bool
}

func parseRate(h http.Header) rateInfo {
	var r rateInfo
	lim, err1 := strconv.Atoi(h.Get("X-RateLimit-Limit"))
	rem, err2 := strconv.Atoi(h.Get("X-RateLimit-Remaining"))
	if err1 != nil || err2 != nil {
		return r
	}
	r.limit, r.remaining, r.ok = lim, rem, true
	if s, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && s > 0 {
		r.reset = time.Unix(s, 0).UTC()
	}
	r.resource = h.Get("X-RateLimit-Resource")
	return r
}

// apiError is a GitHub error response, worded for the model and the admin;
// it never carries a URL with credentials.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

// UpstreamStatus marks a failure GitHub answered, so the HTTP API answers
// 502 with GitHub's plain reason rather than an internal error.
func (e *apiError) UpstreamStatus() int { return e.status }

// rateLimitError carries the wait GitHub asked for; the HTTP API turns it
// into 429 with Retry-After.
type rateLimitError struct {
	apiError
	wait time.Duration
}

func (e *rateLimitError) RetryAfter() time.Duration { return e.wait }

func statusOf(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status
	}
	var re *rateLimitError
	if errors.As(err, &re) {
		return re.status
	}
	return 0
}

type apiClient struct {
	ep   endpoint
	http *http.Client // follows no redirects by itself
	// auth returns the bearer token; nil sends none.
	auth func(ctx context.Context) (string, error)
	// authFailed drops a cached token after a 401, so one retry can mint a
	// fresh one (installation tokens may be revoked before they expire).
	authFailed func()
	observe    func(rateInfo)
	// health learns whether the credentials work: false after a 401 that
	// survived the retry, true after any 2xx.
	health func(ok bool)
	now    func() time.Time
}

func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

type ghErrorBody struct {
	Message string `json:"message"`
	Errors  []struct {
		Resource string `json:"resource"`
		Field    string `json:"field"`
		Code     string `json:"code"`
		Message  string `json:"message"`
	} `json:"errors"`
}

// request sends one API call and returns a 2xx response. GET redirects that
// stay on the API host (renamed repositories) are followed with the token;
// anything else is the caller's to handle when it asks for raw redirects.
func (c *apiClient) request(ctx context.Context, method, path string, q url.Values, body any, accept string) (*http.Response, error) {
	resp, err := c.send(ctx, method, c.ep.url(path, q), body, accept, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, c.statusError(method, path, resp)
	}
	return resp, nil
}

// send returns whatever status GitHub answered. With follow, redirects that
// stay on the API host are followed with the token; any other redirect
// comes back to the caller as is.
func (c *apiClient) send(ctx context.Context, method string, u *url.URL, body any, accept string, follow bool) (*http.Response, error) {
	var payload []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("github: encode request: %w", err)
		}
		payload = b
	}
	retried := false
	for hop := 0; ; hop++ {
		req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("github: %w", err)
		}
		req.Header.Set("Accept", cmp.Or(accept, acceptJSON))
		req.Header.Set("X-GitHub-Api-Version", apiVersion)
		req.Header.Set("User-Agent", userAgent)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.auth != nil {
			tok, err := c.auth(ctx)
			if err != nil {
				// An App's installation token is minted before the call: GitHub
				// answers 401 for a key it no longer accepts and 404 for an
				// installation that is gone, and no response below sees either.
				if s := statusOf(err); c.health != nil && (s == http.StatusUnauthorized || s == http.StatusNotFound) {
					c.health(false)
				}
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, transportError(err)
		}
		if c.observe != nil {
			if ri := parseRate(resp.Header); ri.ok {
				c.observe(ri)
			}
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			if !retried && c.authFailed != nil {
				retried = true
				c.authFailed()
				drain(resp)
				hop--
				continue
			}
			if c.health != nil {
				c.health(false)
			}
		case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			loc, lerr := resp.Location()
			if follow && lerr == nil && hop < maxAPIHops && c.ep.sameAPI(loc) &&
				(method == http.MethodGet || resp.StatusCode >= http.StatusTemporaryRedirect) {
				drain(resp)
				u = loc
				continue
			}
		}
		if c.health != nil && resp.StatusCode < 300 {
			c.health(true)
		}
		return resp, nil
	}
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
	resp.Body.Close()
}

// getJSON decodes a 2xx JSON response into out.
func (c *apiClient) getJSON(ctx context.Context, path string, q url.Values, out any) (http.Header, error) {
	resp, err := c.request(ctx, http.MethodGet, path, q, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return resp.Header, decodeJSON(resp, out)
}

// call sends a JSON body and decodes the JSON answer into out (nil skips it).
func (c *apiClient) call(ctx context.Context, method, path string, body, out any) error {
	resp, err := c.request(ctx, method, path, nil, body, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return nil
	}
	return decodeJSON(resp, out)
}

// getText returns a capped non-JSON body (sha and diff media types) and
// whether it was cut.
func (c *apiClient) getText(ctx context.Context, path string, q url.Values, accept string, limit int) (string, bool, error) {
	resp, err := c.request(ctx, http.MethodGet, path, q, nil, accept)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return "", false, transportError(err)
	}
	if len(b) > limit {
		return string(b[:limit]), true, nil
	}
	return string(b), false, nil
}

func decodeJSON(resp *http.Response, out any) error {
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody+1))
	if err != nil {
		return transportError(err)
	}
	if len(b) > maxJSONBody {
		return errors.New("github: response larger than 16 MB")
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("github: unexpected response: %w", err)
	}
	return nil
}

// paged collects up to max items from a list endpoint, following Link
// rel="next" on the API host. extract reads one page. truncated reports
// that GitHub had more.
func paged[T any](ctx context.Context, c *apiClient, path string, q url.Values, max int, extract func([]byte) ([]T, error)) ([]T, bool, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("per_page", strconv.Itoa(min(max, 100)))
	u := c.ep.url(path, q)
	var out []T
	for page := 0; ; page++ {
		resp, err := c.send(ctx, http.MethodGet, u, nil, "", true)
		if err != nil {
			return nil, false, err
		}
		if resp.StatusCode >= 300 {
			err := c.statusError(http.MethodGet, path, resp)
			resp.Body.Close()
			return nil, false, err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody+1))
		resp.Body.Close()
		if err != nil {
			return nil, false, transportError(err)
		}
		if len(b) > maxJSONBody {
			return nil, false, errors.New("github: response larger than 16 MB")
		}
		items, err := extract(b)
		if err != nil {
			return nil, false, fmt.Errorf("github: unexpected response: %w", err)
		}
		out = append(out, items...)
		next := nextLink(resp.Header.Get("Link"))
		if next == "" {
			return out, false, nil
		}
		if len(out) >= max {
			return out[:max], true, nil
		}
		nu, perr := u.Parse(next)
		if perr == nil && c.ep.sameAPI(nu) {
			u = nu
			continue
		}
		return out, true, nil
	}
}

func jsonArray[T any](b []byte) ([]T, error) {
	var v []T
	err := json.Unmarshal(b, &v)
	return v, err
}

// nextLink extracts rel="next" from a Link header (RFC 8288), as GitHub
// sends it: <url>; rel="next", <url>; rel="last".
func nextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, p := range strings.Split(params, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
			if strings.EqualFold(k, "rel") && slicesContainsField(strings.Trim(v, `"`), "next") {
				return target[1 : len(target)-1]
			}
		}
	}
	return ""
}

func slicesContainsField(s, want string) bool {
	for _, f := range strings.Fields(s) {
		if f == want {
			return true
		}
	}
	return false
}

func (c *apiClient) statusError(method, path string, resp *http.Response) error {
	var body ghErrorBody
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	_ = json.Unmarshal(b, &body)
	msg := oneLine(body.Message, 300)
	for _, e := range body.Errors {
		if d := oneLine(cmp.Or(e.Message, strings.Trim(e.Field+" "+e.Code, " ")), 200); d != "" {
			msg += "; " + d
		}
		if len(msg) > 600 {
			break
		}
	}
	what := method + " " + path
	st := resp.StatusCode
	ri := parseRate(resp.Header)
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	if st == http.StatusForbidden || st == http.StatusTooManyRequests {
		if wait, ok := retryAfter(resp.Header, ri, body.Message, now()); ok {
			text := fmt.Sprintf("GitHub rate limit reached (%s); try again in %s", what, wait.Round(time.Second))
			if ri.ok && ri.remaining == 0 && !ri.reset.IsZero() {
				text = fmt.Sprintf("GitHub rate limit reached (%d requests per hour); it resets at %s UTC, in %s",
					ri.limit, ri.reset.Format("15:04"), wait.Round(time.Second))
			}
			return &rateLimitError{apiError: apiError{status: st, msg: text}, wait: wait}
		}
	}
	var text string
	switch {
	case st == http.StatusUnauthorized:
		text = "GitHub rejected the credentials (wrong, expired or revoked token)"
	case st == http.StatusForbidden:
		text = "GitHub refused " + what + ": the token or app lacks the permission for it"
	case st == http.StatusNotFound:
		text = "GitHub found nothing at " + what + " (it does not exist, or this connection cannot see it)"
	case st == http.StatusConflict:
		text = "GitHub reported a conflict for " + what
	case st == http.StatusUnprocessableEntity:
		text = "GitHub could not process " + what
	case st >= 500:
		text = fmt.Sprintf("GitHub is having trouble (HTTP %d for %s); try again later", st, what)
	default:
		text = fmt.Sprintf("GitHub answered HTTP %d for %s", st, what)
	}
	if msg != "" {
		text += ": " + msg
	}
	return &apiError{status: st, msg: text}
}

// retryAfter decides whether a 403/429 is a rate limit and how long to wait
// (GitHub's REST docs: retry-after first, then x-ratelimit-reset when
// remaining is 0, else at least a minute for a secondary limit).
func retryAfter(h http.Header, ri rateInfo, message string, now time.Time) (time.Duration, bool) {
	if s := h.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return time.Duration(n) * time.Second, true
		}
	}
	if ri.ok && ri.remaining == 0 {
		if ri.reset.After(now) {
			return ri.reset.Sub(now), true
		}
		return time.Minute, true
	}
	if strings.Contains(strings.ToLower(message), "rate limit") {
		return time.Minute, true
	}
	return 0, false
}

// transportError drops the request URL from client errors: an archive URL
// carries a short-lived download token in its query.
func transportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("github: request timed out: %w", context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return err
	}
	return fmt.Errorf("github: could not reach GitHub: %w", err)
}

func oneLine(s string, n int) string {
	return clip(strings.Join(strings.Fields(s), " "), n)
}

// clip cuts s to at most n bytes on a rune boundary, marking the cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
