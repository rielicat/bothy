package egress

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

// plainUpstream starts an HTTP server reachable as publicIP:80.
func (e *testEnv) plainUpstream(h http.Handler) *httptest.Server {
	ts := httptest.NewServer(h)
	e.t.Cleanup(ts.Close)
	e.dial.route(publicIP+":80", ts.Listener.Addr().String())
	return ts
}

func TestDNSRebindingDialsThePinnedIP(t *testing.T) {
	e := newEnv(t, nil)
	e.plainUpstream(http.HandlerFunc(hello))
	e.res.set("rebind.test", []string{publicIP}, []string{"127.0.0.1"})

	resp, err := e.s.PublicClient(e.sess, e.principal()).Get("http://rebind.test/a")
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); got != "hello /a" {
		t.Fatalf("body = %q", got)
	}
	if d := e.dial.calls(); len(d) != 1 || d[0] != publicIP+":80" {
		t.Fatalf("dialed %v, want only the pinned literal %s:80", d, publicIP)
	}
	if n := e.res.count("rebind.test"); n != 1 {
		t.Fatalf("resolved %d times, want once", n)
	}
	// Within the TTL the pinned answer is reused, never the rebinding one.
	resp, err = e.s.PublicClient(e.sess, e.principal()).Get("http://rebind.test/b")
	if err != nil {
		t.Fatal(err)
	}
	body(t, resp)

	// With caching off the second answer is used, and refused.
	e2 := newEnv(t, func(c *config.Config) { c.Egress.DNS.MaxTTL = 0 })
	e2.plainUpstream(http.HandlerFunc(hello))
	e2.res.set("rebind.test", []string{publicIP}, []string{"127.0.0.1"})
	c := e2.s.PublicClient(e2.sess, e2.principal())
	resp, err = c.Get("http://rebind.test/")
	if err != nil {
		t.Fatal(err)
	}
	body(t, resp)
	if _, err := c.Get("http://rebind.test/"); !errors.Is(err, errBlocked) {
		t.Fatalf("rebound answer: err = %v, want errBlocked", err)
	}
	for _, d := range e2.dial.calls() {
		if d != publicIP+":80" {
			t.Fatalf("dialed %s", d)
		}
	}
}

func TestRedirectToPrivateAddressRefused(t *testing.T) {
	e := newEnv(t, nil)
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the private server was reached")
	}))
	defer local.Close()
	e.res.set("example.com", []string{publicIP})
	e.res.set("internal.test", []string{"10.0.0.7"})
	e.plainUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loopback":
			http.Redirect(w, r, local.URL+"/", http.StatusFound)
		case "/name":
			http.Redirect(w, r, "http://internal.test/", http.StatusFound)
		case "/metadata":
			http.Redirect(w, r, "http://[::ffff:169.254.169.254]/latest/", http.StatusMovedPermanently)
		case "/scheme":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		}
	}))
	for _, p := range []string{"/loopback", "/name", "/metadata", "/scheme"} {
		resp, err := e.s.PublicClient(e.sess, e.principal()).Get("http://example.com" + p)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("redirect %s was followed", p)
		}
	}
	for _, d := range e.dial.calls() {
		if d != publicIP+":80" {
			t.Fatalf("redirect dialed %s", d)
		}
	}
}

func TestRedirectLimit(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.WebFetch.MaxRedirects = 2 })
	e.res.set("example.com", []string{publicIP})
	e.plainUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n int
		_, _ = fmt.Sscanf(r.URL.Path, "/%d", &n)
		if n == 0 {
			fmt.Fprint(w, "done")
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/%d", n-1), http.StatusFound)
	}))
	c := e.s.PublicClient(e.sess, e.principal())
	resp, err := c.Get("http://example.com/2")
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); got != "done" {
		t.Fatalf("body = %q", got)
	}
	if resp, err := c.Get("http://example.com/3"); err == nil {
		resp.Body.Close()
		t.Fatal("followed more redirects than web_fetch.max_redirects")
	}
}

func TestPublicClientStripsCredentialsAndLogs(t *testing.T) {
	e := newEnv(t, nil)
	var mu sync.Mutex
	var seen []string
	e.res.set("example.com", []string{publicIP})
	e.plainUpstream(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization")+"|"+r.Header.Get("Cookie")+"|"+r.Header.Get("Proxy-Authorization"))
		mu.Unlock()
		if r.URL.Path == "/r" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/r", nil)
	req.Header.Set("Authorization", "Bearer sk-secret")
	req.Header.Set("Cookie", "session=1")
	req.Header.Set("Proxy-Authorization", "Basic eDp5")
	resp, err := e.s.PublicClient(e.sess, e.principal()).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body(t, resp)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("requests = %v", seen)
	}
	for _, s := range seen {
		if s != "||" {
			t.Fatalf("credential headers reached the server: %q", s)
		}
	}
	recs := e.records(2)
	for _, r := range recs {
		if r.Dialer != "public" || r.Decision != "allow" || r.Session != e.sess || r.Principal != e.principal() ||
			r.Host != "example.com" || r.Port != 80 || r.IP != publicIP || r.Down == 0 || r.Up == 0 {
			t.Fatalf("record = %+v", r)
		}
	}
}

func TestPublicClientDenialIsLoggedAndPublished(t *testing.T) {
	e := newEnv(t, nil)
	sub := e.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvEgressBlocked}}, 4)
	defer sub.Close()
	e.res.set("evil.test", []string{"192.168.1.1"})
	if resp, err := e.s.PublicClient(e.sess, e.principal()).Get("http://evil.test/"); err == nil {
		resp.Body.Close()
		t.Fatal("private destination fetched")
	}
	r := e.records(1)[0]
	if r.Dialer != "public" || r.Decision != "deny" || r.IP != "192.168.1.1" || !strings.Contains(r.Reason, "192.168.0.0/16") {
		t.Fatalf("record = %+v", r)
	}
	select {
	case ev := <-sub.C():
		if ev.Principal != e.principal() || ev.Data.(core.EgressBlockedData).Host != "evil.test" {
			t.Fatalf("event = %+v", ev)
		}
	default:
		t.Fatal("no egress.blocked event")
	}
}

func TestSystemClientExactHostsOnly(t *testing.T) {
	e := newEnv(t, nil)
	e.res.set("api.example.com", []string{publicIP})
	e.res.set("other.example.com", []string{publicIP})
	e.res.set("ollama.lan", []string{"192.168.1.20"})
	e.res.set("meta.lan", []string{"169.254.169.254"})
	ts := e.plainUpstream(http.HandlerFunc(hello))
	e.dial.route("192.168.1.20:80", ts.Listener.Addr().String())

	d := core.SystemDest{Name: "provider:test", Hosts: []string{"API.example.com:80"}}
	c := e.s.SystemClient(d)
	if c != e.s.SystemClient(d) {
		t.Fatal("SystemClient is not cached per destination")
	}
	if tr := c.Transport.(*http.Transport); tr.Proxy != nil || tr.TLSClientConfig.MinVersion < 0x0303 {
		t.Fatal("system transport must have Proxy nil and TLS >= 1.2")
	}
	resp, err := c.Get("http://api.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); got != "hello /v1" {
		t.Fatalf("body = %q", got)
	}
	for _, u := range []string{"http://other.example.com/", "http://api.example.com:8080/", "http://" + publicIP + "/"} {
		if resp, err := c.Get(u); !errors.Is(err, errDenied) {
			if err == nil {
				resp.Body.Close()
			}
			t.Fatalf("%s: err = %v, want errDenied", u, err)
		}
	}

	lan := core.SystemDest{Name: "provider:ollama", Hosts: []string{"ollama.lan:80"}}
	if resp, err := e.s.SystemClient(lan).Get("http://ollama.lan/"); !errors.Is(err, errBlocked) {
		if err == nil {
			resp.Body.Close()
		}
		t.Fatalf("private host without AllowPrivate: %v", err)
	}
	lan.AllowPrivate = true
	resp, err = e.s.SystemClient(lan).Get("http://ollama.lan/tags")
	if err != nil {
		t.Fatalf("private host with AllowPrivate: %v", err)
	}
	body(t, resp)
	meta := core.SystemDest{Name: "mcp:x", Hosts: []string{"meta.lan:80"}, AllowPrivate: true}
	if resp, err := e.s.SystemClient(meta).Get("http://meta.lan/"); !errors.Is(err, errBlocked) {
		if err == nil {
			resp.Body.Close()
		}
		t.Fatalf("metadata with AllowPrivate: %v", err)
	}

	c.CloseIdleConnections()
	e.s.SystemClient(lan).CloseIdleConnections()
	recs := e.records(7)
	var allowed, denied int
	for _, r := range recs {
		if r.Dialer != "system" || r.Principal != "" {
			t.Fatalf("record = %+v", r)
		}
		switch r.Decision {
		case "allow":
			allowed++
		case "deny":
			denied++
		}
	}
	if allowed != 2 || denied != 5 {
		t.Fatalf("allowed %d denied %d: %+v", allowed, denied, recs)
	}
	if !slices.ContainsFunc(recs, func(r core.EgressRecord) bool { return r.Dest == "provider:test" && r.Decision == "allow" }) {
		t.Fatalf("no allowed provider:test row: %+v", recs)
	}
}
