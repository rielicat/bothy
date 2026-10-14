package egress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

func TestSearchCheck(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, func(c *config.Config) { c.Search.Provider = "none" })
	srv := &searchServer{}
	ok := httptest.NewServer(srv.handler(`{"results":[]}`))
	defer ok.Close()
	e.res.set("searxng", []string{"172.18.0.5"})
	e.dial.route("172.18.0.5:8080", ok.Listener.Addr().String())

	if err := e.s.TestSearch(ctx, "searxng", "http://searxng:8080", true, core.Secret{}); err != nil {
		t.Fatalf("searxng = %v", err)
	}
	if req, _ := srv.last(); req.URL.Path != "/search" || req.URL.Query().Get("format") != "json" {
		t.Fatalf("searxng request = %s", req.URL)
	}
	err := e.s.TestSearch(ctx, "searxng", "http://searxng:8080", false, core.Secret{})
	if core.CodeOf(err) != core.FailPrivateAddress {
		t.Fatalf("private searxng = %v (%s)", err, core.CodeOf(err))
	}

	var status int
	var token string
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get("X-Subscription-Token")
		http.Error(w, "nope "+token, status)
	}))
	defer brave.Close()
	e.res.set("brave.test", []string{publicIP})
	e.dial.route(publicIP+":80", brave.Listener.Addr().String())
	e.tool("web_search").(*webSearch).brave = "http://brave.test/"
	for _, tc := range []struct {
		status int
		key    string
		want   core.FailureCode
		sent   string
	}{
		{http.StatusUnauthorized, "BSA-pasted-key", core.FailKeyRejected, "BSA-pasted-key"},
		{http.StatusTooManyRequests, "", core.FailRateLimited, "BSA-secret-key"},
		{http.StatusBadGateway, "", core.FailProviderError, "BSA-secret-key"},
	} {
		status = tc.status
		err := e.s.TestSearch(ctx, "brave", "", false, core.NewSecret([]byte(tc.key)))
		if core.CodeOf(err) != tc.want || token != tc.sent || strings.Contains(err.Error(), "BSA-") {
			t.Fatalf("brave %d = %v (%s), sent %q", tc.status, err, core.CodeOf(err), token)
		}
	}

	none := newEnv(t, func(c *config.Config) { c.Search.TavilyAPIKey = "" })
	if err := none.s.TestSearch(ctx, "tavily", "", false, core.Secret{}); core.CodeOf(err) != core.FailKeyRejected {
		t.Fatalf("tavily without a key = %v", err)
	}
	if err := e.s.TestSearch(ctx, "google", "", false, core.Secret{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("unknown provider = %v", err)
	}
	if err := e.s.TestSearch(ctx, "searxng", "", false, core.Secret{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("searxng without an address = %v", err)
	}
}
