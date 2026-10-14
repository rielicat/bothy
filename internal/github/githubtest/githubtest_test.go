package githubtest

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func get(t *testing.T, s *Server, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// The fake speaks the documented wire details the client relies on.
func TestFakeWireDetails(t *testing.T) {
	s := New()
	defer s.Close()
	for i := 0; i < 3; i++ {
		s.AddRepo("octocat", "r"+string(rune('a'+i)), true, map[string]string{"f": "x"})
	}

	resp := get(t, s, "/user/repos?per_page=2", s.PAT)
	link := resp.Header.Get("Link")
	if !strings.Contains(link, `page=2&per_page=2>; rel="next"`) || !strings.Contains(link, `rel="last"`) {
		t.Errorf("Link = %q", link)
	}
	if resp.Header.Get("X-RateLimit-Limit") != "5000" || resp.Header.Get("X-RateLimit-Remaining") != "4999" || resp.Header.Get("X-RateLimit-Reset") == "" {
		t.Errorf("rate headers = %v", resp.Header)
	}
	var page []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil || len(page) != 2 || page[0]["full_name"] != "octocat/ra" {
		t.Errorf("page = %v %v", page, err)
	}

	resp = get(t, s, "/user", "github_pat_wrong")
	var e map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if resp.StatusCode != 401 || e["message"] != "Bad credentials" || e["status"] != "401" || e["documentation_url"] == nil {
		t.Errorf("401 body = %v", e)
	}

	resp = get(t, s, "/repos/octocat/ra/tarball/main", s.PAT)
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || !strings.Contains(loc, "/_codeload/octocat/ra/legacy.tar.gz/") || !strings.Contains(loc, "token=") {
		t.Fatalf("tarball redirect = %d %q", resp.StatusCode, loc)
	}
	resp = get(t, s, strings.TrimPrefix(loc, s.URL), "")
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || len(b) < 2 || b[0] != 0x1f || b[1] != 0x8b {
		t.Errorf("archive = %d %d bytes", resp.StatusCode, len(b))
	}

	s.SetRateLimit(0, s.reset)
	resp = get(t, s, "/user", s.PAT)
	if resp.StatusCode != 403 || resp.Header.Get("X-RateLimit-Remaining") != "0" {
		t.Errorf("exhausted = %d %v", resp.StatusCode, resp.Header)
	}
}
