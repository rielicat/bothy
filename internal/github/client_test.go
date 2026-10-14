package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) (*apiClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ep, err := newEndpoint(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &apiClient{ep: ep, http: &http.Client{CheckRedirect: noRedirects},
		auth: func(context.Context) (string, error) { return "tok", nil }}, srv
}

func TestEndpoint(t *testing.T) {
	for _, tc := range []struct {
		base    string
		hosts   []string
		private bool
	}{
		{"https://api.github.com", []string{"api.github.com:443", "codeload.github.com:443"}, false},
		{"https://api.github.com/", []string{"api.github.com:443", "codeload.github.com:443"}, false},
		{"https://GHE.example.com/api/v3", []string{"ghe.example.com:443", "codeload.ghe.example.com:443"}, true},
		{"http://127.0.0.1:8080", []string{"127.0.0.1:8080"}, true},
	} {
		ep, err := newEndpoint(tc.base)
		if err != nil || !slices.Equal(ep.hosts, tc.hosts) || ep.private != tc.private {
			t.Errorf("%s: %+v %v", tc.base, ep, err)
		}
	}
	for _, bad := range []string{"", "ftp://x", "https://", "https://u:p@ghe.example.com", "https://ghe.example.com?x=1"} {
		if _, err := newEndpoint(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	ep, _ := newEndpoint("https://ghe.example.com/api/v3")
	for raw, want := range map[string]bool{
		"https://codeload.ghe.example.com/x":   true,
		"https://ghe.example.com/_codeload/x":  true,
		"http://codeload.ghe.example.com/x":    false,
		"https://evil.example/x":               false,
		"https://u@ghe.example.com/x":          false,
		"https://ghe.example.com:8443/x":       false,
		"https://codeload.ghe.example.com:443": true,
	} {
		u, _ := url.Parse(raw)
		if got := ep.allowed(u); got != want {
			t.Errorf("allowed(%s) = %v", raw, got)
		}
	}
	if got := ep.url("/repos/o/r/commits/"+escapeRef("feat/a#1"), url.Values{"x": {"1"}}).String(); got != "https://ghe.example.com/api/v3/repos/o/r/commits/feat/a%231?x=1" {
		t.Errorf("url = %s", got)
	}
}

func TestClientHeadersAndErrors(t *testing.T) {
	var seen http.Header
	cl, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte(`{"login":"octocat"}`))
		case "/missing":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`))
		case "/forbidden":
			w.Header().Set("X-RateLimit-Remaining", "4000")
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token","status":"403"}`))
		case "/invalid":
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"message":"Validation Failed","errors":[{"resource":"PullRequest","field":"head","code":"invalid"}]}`))
		case "/boom":
			w.WriteHeader(502)
		}
	})
	var u ghUser
	if _, err := cl.getJSON(context.Background(), "/ok", nil, &u); err != nil || u.Login != "octocat" {
		t.Fatalf("ok: %+v %v", u, err)
	}
	if seen.Get("Authorization") != "Bearer tok" || seen.Get("Accept") != acceptJSON ||
		seen.Get("X-GitHub-Api-Version") != apiVersion || !strings.HasPrefix(seen.Get("User-Agent"), "Bothy") {
		t.Errorf("headers %v", seen)
	}
	for path, want := range map[string]string{
		"/missing":   "GitHub found nothing at GET /missing (it does not exist, or this connection cannot see it): Not Found",
		"/forbidden": "GitHub refused GET /forbidden: the token or app lacks the permission for it: Resource not accessible by personal access token",
		"/invalid":   "GitHub could not process GET /invalid: Validation Failed; head invalid",
		"/boom":      "GitHub is having trouble (HTTP 502 for GET /boom); try again later",
	} {
		_, err := cl.getJSON(context.Background(), path, nil, &u)
		var re *rateLimitError
		if err == nil || err.Error() != want || errors.As(err, &re) {
			t.Errorf("%s: %v", path, err)
		}
	}
	if statusOf(fmt.Errorf("wrapped: %w", &apiError{status: 404})) != 404 {
		t.Error("statusOf through wrapping")
	}
}

func TestClientRateLimits(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	reset := now.Add(17 * time.Minute)
	cl, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/primary":
			w.Header().Set("X-RateLimit-Limit", "5000")
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Used", "5000")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for user ID 1."}`))
		case "/secondary":
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit."}`))
		case "/secondary429":
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`))
		}
	})
	cl.now = func() time.Time { return now }
	var seen []rateInfo
	cl.observe = func(ri rateInfo) { seen = append(seen, ri) }
	for path, wait := range map[string]time.Duration{"/primary": 17 * time.Minute, "/secondary": 30 * time.Second, "/secondary429": time.Minute} {
		_, err := cl.getJSON(context.Background(), path, nil, &struct{}{})
		var re *rateLimitError
		if !errors.As(err, &re) || re.RetryAfter() != wait || !strings.Contains(err.Error(), "rate limit") {
			t.Errorf("%s: %v (wait %v)", path, err, wait)
		}
	}
	_, err := cl.getJSON(context.Background(), "/primary", nil, &struct{}{})
	if !strings.Contains(err.Error(), "resets at 12:17 UTC") {
		t.Errorf("primary text: %v", err)
	}
	if len(seen) == 0 || seen[0].limit != 5000 || seen[0].remaining != 0 || !seen[0].reset.Equal(reset) {
		t.Errorf("observed %+v", seen)
	}
}

func TestClientRetriesOnceAfter401(t *testing.T) {
	var calls atomic.Int32
	cl, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 || r.URL.Path == "/always" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})
	var invalidated int
	cl.authFailed = func() { invalidated++ }
	if _, err := cl.getJSON(context.Background(), "/once", nil, &struct{}{}); err != nil || invalidated != 1 || calls.Load() != 2 {
		t.Fatalf("retry: %v invalidated=%d calls=%d", err, invalidated, calls.Load())
	}
	_, err := cl.getJSON(context.Background(), "/always", nil, &struct{}{})
	if statusOf(err) != 401 || !strings.Contains(err.Error(), "rejected the credentials") || invalidated != 2 {
		t.Fatalf("401 twice: %v invalidated=%d", err, invalidated)
	}
}

func TestClientRedirects(t *testing.T) {
	var auths []string
	cl, srv := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/old/name":
			http.Redirect(w, r, "/repos/new/name", http.StatusMovedPermanently)
		case "/repos/new/name":
			_, _ = w.Write([]byte(`{"name":"name"}`))
		case "/away":
			http.Redirect(w, r, "https://evil.example/steal", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	})
	var out ghRepo
	if _, err := cl.getJSON(context.Background(), "/repos/old/name", nil, &out); err != nil || out.Name != "name" {
		t.Fatalf("same-host redirect: %+v %v", out, err)
	}
	if !slices.Equal(auths, []string{"Bearer tok", "Bearer tok"}) {
		t.Errorf("auth on redirect: %q", auths)
	}
	auths = nil
	if _, err := cl.getJSON(context.Background(), "/away", nil, &out); err == nil || len(auths) != 1 {
		t.Errorf("off-host redirect: %v, %d requests", err, len(auths))
	}
	resp, err := cl.send(context.Background(), http.MethodGet, cl.ep.url("/away", nil), nil, "", true)
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("send returns the off-host redirect: %v", err)
	}
	resp.Body.Close()
	if _, err := cl.getJSON(context.Background(), "/loop", nil, &out); err == nil {
		t.Error("redirect loop accepted")
	}
	_ = srv
}

func TestPaged(t *testing.T) {
	total := 250
	var other bool
	cl, srv := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		page = max(page, 1)
		lo, hi := (page-1)*per, min(page*per, total)
		items := []map[string]int{}
		for i := lo; i < hi; i++ {
			items = append(items, map[string]int{"id": i})
		}
		if hi < total {
			next := fmt.Sprintf("http://%s%s?per_page=%d&page=%d", r.Host, r.URL.Path, per, page+1)
			if other {
				next = "https://elsewhere.example/next"
			}
			w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next", <http://%s/x?page=99>; rel="last"`, next, r.Host))
		}
		_ = json.NewEncoder(w).Encode(items)
	})
	type item struct{ ID int }
	got, more, err := paged(context.Background(), cl, "/items", nil, 1000, jsonArray[item])
	if err != nil || len(got) != 250 || more || got[249].ID != 249 {
		t.Fatalf("all pages: %d %v %v", len(got), more, err)
	}
	got, more, err = paged(context.Background(), cl, "/items", nil, 120, jsonArray[item])
	if err != nil || len(got) != 120 || !more {
		t.Fatalf("capped: %d %v %v", len(got), more, err)
	}
	other = true
	got, more, err = paged(context.Background(), cl, "/items", nil, 1000, jsonArray[item])
	if err != nil || len(got) != 100 || !more {
		t.Fatalf("off-host next link must stop: %d %v %v", len(got), more, err)
	}
	_ = srv
}

func TestNextLink(t *testing.T) {
	for h, want := range map[string]string{
		`<https://api.github.com/user/repos?page=2>; rel="next", <https://api.github.com/user/repos?page=5>; rel="last"`: "https://api.github.com/user/repos?page=2",
		`<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=3>; rel="next"`:                   "https://api.github.com/x?page=3",
		`<https://api.github.com/x?page=1>; rel="first"`:                                                                 "",
		`<https://a/b>; rel="last next"`:                                                                                 "https://a/b",
		`garbage`:                                                                                                        "",
		``:                                                                                                               "",
	} {
		if got := nextLink(h); got != want {
			t.Errorf("nextLink(%q) = %q, want %q", h, got, want)
		}
	}
}

func TestTransportErrorHidesURL(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()
	ep, _ := newEndpoint(addr)
	cl := &apiClient{ep: ep, http: &http.Client{CheckRedirect: noRedirects}}
	u := ep.url("/_codeload/o/r/legacy.tar.gz/abc", url.Values{"token": {"SECRETDOWNLOADTOKEN"}})
	_, err := cl.send(context.Background(), http.MethodGet, u, nil, "", true)
	if err == nil || strings.Contains(err.Error(), "SECRETDOWNLOADTOKEN") || !strings.Contains(err.Error(), "could not reach GitHub") {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if _, err := cl.send(ctx, http.MethodGet, u, nil, "", true); !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("timeout err = %v", err)
	}
}

func TestTokenExpiry(t *testing.T) {
	if got := tokenExpiry("2026-11-01 10:00:00 UTC"); !got.Equal(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("UTC form: %v", got)
	}
	if got := tokenExpiry("2026-11-01 10:00:00 +0200"); !got.Equal(time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("offset form: %v", got)
	}
	if !tokenExpiry("").IsZero() || !tokenExpiry("soon").IsZero() {
		t.Error("garbage parsed")
	}
}
