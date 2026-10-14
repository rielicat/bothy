package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
)

// credsRoutes stands in for the network behind the credentials dialer: it
// sends each fixed endpoint to a local server and records what was dialed.
type credsRoutes struct {
	mu     sync.Mutex
	routes map[netip.AddrPort]string
	dialed []netip.AddrPort
}

func (r *credsRoutes) dial(ctx context.Context, ap netip.AddrPort) (net.Conn, error) {
	r.mu.Lock()
	r.dialed = append(r.dialed, ap)
	to, ok := r.routes[ap]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("no route to %s", ap)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", to)
}

func (r *credsRoutes) calls() []netip.AddrPort {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]netip.AddrPort(nil), r.dialed...)
}

// fakeIMDS answers the IMDSv2 flow the AWS SDK's role provider uses.
func fakeIMDS(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == imdsTokenPath:
			fmt.Fprint(w, "TOKEN")
		case r.Header.Get(imdsTokenHeader) != "TOKEN":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == imdsRolesPath:
			fmt.Fprint(w, "bothy-role\n")
		case r.URL.Path == imdsRolesPath+"bothy-role":
			fmt.Fprint(w, `{"Code":"Success","AccessKeyId":"AKIDEXAMPLE","SecretAccessKey":"s","Token":"t","Expiration":"2026-10-14T12:00:00Z"}`)
		case r.URL.Path == imdsRegionPath:
			fmt.Fprint(w, "us-west-2")
		default:
			t.Errorf("fake IMDS reached at %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func do(c *http.Client, method, u string, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return c.Do(req)
}

func TestCredentialsClientIMDSv2(t *testing.T) {
	e := newEnv(t, nil)
	imds := fakeIMDS(t)
	routes := &credsRoutes{routes: map[netip.AddrPort]string{
		netip.MustParseAddrPort("169.254.169.254:80"): imds.Listener.Addr().String(),
		netip.MustParseAddrPort("[fd00:ec2::254]:80"): imds.Listener.Addr().String(),
	}}
	e.s.dialCreds = routes.dial
	c := e.s.CredentialsClient("", "")

	resp, err := do(c, http.MethodPut, "http://169.254.169.254/latest/api/token", map[string]string{imdsTTLHeader: "21600"})
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); got != "TOKEN" {
		t.Fatalf("token = %q", got)
	}
	tok := map[string]string{imdsTokenHeader: "TOKEN"}
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/bothy-role",
		"http://169.254.169.254/latest/meta-data/placement/region",
		"http://[fd00:ec2::254]/latest/meta-data/iam/security-credentials/bothy-role",
	} {
		resp, err := do(c, http.MethodGet, u, tok)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %s", u, resp.Status)
		}
		body(t, resp)
	}

	refused := []struct {
		method, url string
		hdr         map[string]string
	}{
		{http.MethodGet, "http://169.254.169.254/latest/meta-data/iam/security-credentials/bothy-role", nil}, // IMDSv1
		{http.MethodGet, "http://169.254.169.254/latest/user-data", tok},
		{http.MethodGet, "http://169.254.169.254/latest/meta-data/", tok},
		{http.MethodGet, "http://169.254.169.254/latest/dynamic/instance-identity/document", tok},
		{http.MethodGet, "http://169.254.169.254/latest/meta-data/iam/security-credentials/a/b", tok},
		{http.MethodGet, "http://169.254.169.254/latest/meta-data/iam/security-credentials/..", tok},
		{http.MethodGet, "http://169.254.169.254/latest/meta-data/iam/security-credentials/.", tok},
		{http.MethodGet, "http://169.254.169.254/latest/meta-data/iam/security-credentials/bothy-role?x=1", tok},
		{http.MethodPut, "http://169.254.169.254/latest/api/token", nil},
		{http.MethodPost, "http://169.254.169.254/latest/api/token", map[string]string{imdsTTLHeader: "60"}},
		{http.MethodGet, "http://169.254.169.254:8080/latest/meta-data/placement/region", tok},
		{http.MethodGet, "https://169.254.169.254/latest/meta-data/placement/region", tok},
		{http.MethodGet, "http://169.254.170.2/v2/credentials/x", nil}, // no container endpoint configured
		{http.MethodGet, "http://100.100.100.200/latest/meta-data/", tok},
		{http.MethodGet, "http://10.0.0.1/", nil},
		{http.MethodGet, "https://sts.us-west-2.amazonaws.com/", nil},
	}
	before := len(routes.calls())
	for _, r := range refused {
		resp, err := do(c, r.method, r.url, r.hdr)
		if err == nil {
			resp.Body.Close()
			t.Errorf("%s %s was allowed", r.method, r.url)
			continue
		}
		if !errors.Is(err, errDenied) {
			t.Errorf("%s %s: err = %v, want errDenied", r.method, r.url, err)
		}
	}
	if n := len(routes.calls()); n != before {
		t.Fatalf("refused requests were dialed: %v", routes.calls()[before:])
	}

	recs := e.records(5 + len(refused))
	var allowed, denied int
	for _, r := range recs {
		if r.Dialer != "system" || r.Dest != credsDest || r.Host == "" || r.Port == 0 {
			t.Fatalf("record %+v", r)
		}
		switch r.Decision {
		case "allow":
			allowed++
		case "deny":
			denied++
		}
	}
	if allowed != 5 || denied != len(refused) {
		t.Fatalf("allowed %d denied %d: %+v", allowed, denied, recs)
	}
}

func TestCredentialsClientContainerEndpoints(t *testing.T) {
	e := newEnv(t, nil)
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		if r.Header.Get("Authorization") != "agent-token" && strings.HasPrefix(r.URL.Path, "/v1/") {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprint(w, `{"AccessKeyId":"AKIDEXAMPLE","SecretAccessKey":"s","Token":"t","Expiration":"2026-10-14T12:00:00Z"}`)
	}))
	defer srv.Close()
	routes := &credsRoutes{routes: map[netip.AddrPort]string{
		netip.MustParseAddrPort("169.254.170.2:80"):  srv.Listener.Addr().String(),
		netip.MustParseAddrPort("169.254.170.23:80"): srv.Listener.Addr().String(),
	}}
	e.s.dialCreds = routes.dial

	ecs := e.s.CredentialsClient("/v2/credentials/0b1d-task", "")
	// The AWS SDK builds requests with an empty Method, which net/http sends as GET.
	resp, err := ecs.Do((&http.Request{URL: mustURL("http://169.254.170.2/v2/credentials/0b1d-task"), Header: http.Header{}}).WithContext(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	body(t, resp)
	for _, u := range []string{"http://169.254.170.2/v2/credentials/other", "http://169.254.170.2/v2/metadata", "http://169.254.170.23/v1/credentials"} {
		if resp, err := ecs.Get(u); !errors.Is(err, errDenied) {
			if err == nil {
				resp.Body.Close()
			}
			t.Errorf("%s: err = %v, want errDenied", u, err)
		}
	}
	if resp, err := do(ecs, http.MethodPost, "http://169.254.170.2/v2/credentials/0b1d-task", nil); !errors.Is(err, errDenied) {
		if err == nil {
			resp.Body.Close()
		}
		t.Errorf("POST to the container endpoint: %v", err)
	}

	// EKS Pod Identity: AWS_CONTAINER_CREDENTIALS_FULL_URI on 169.254.170.23.
	eks := e.s.CredentialsClient("", "http://169.254.170.23/v1/credentials")
	resp, err = do(eks, http.MethodGet, "http://169.254.170.23/v1/credentials", map[string]string{"Authorization": "agent-token"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorization header not forwarded: %s", resp.Status)
	}
	body(t, resp)
	if resp, err := eks.Get("http://169.254.170.2/v2/credentials/0b1d-task"); !errors.Is(err, errDenied) {
		if err == nil {
			resp.Body.Close()
		}
		t.Errorf("relative endpoint reachable without being configured: %v", err)
	}

	// A loopback full URI (a local credential agent) reaches exactly its port and path.
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	routes.mu.Lock()
	routes.routes[netip.MustParseAddrPort("127.0.0.1:"+port)] = srv.Listener.Addr().String()
	routes.mu.Unlock()
	local := e.s.CredentialsClient("", "http://127.0.0.1:"+port+"/creds?role=a")
	resp, err = local.Get("http://127.0.0.1:" + port + "/creds?role=a")
	if err != nil {
		t.Fatal(err)
	}
	body(t, resp)
	if resp, err := local.Get("http://127.0.0.1:" + port + "/creds?role=b"); !errors.Is(err, errDenied) {
		if err == nil {
			resp.Body.Close()
		}
		t.Errorf("another query on the full URI host: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Join(paths, " ") != "/v2/credentials/0b1d-task /v1/credentials /creds?role=a" {
		t.Fatalf("server saw %v", paths)
	}
}

func TestCredentialsClientRejectsBadContainerURIs(t *testing.T) {
	e := newEnv(t, nil)
	routes := &credsRoutes{routes: map[netip.AddrPort]string{}}
	e.s.dialCreds = routes.dial
	for _, full := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/x", // IMDS is not a container endpoint
		"ftp://169.254.170.23/creds",
		"http://user:pw@169.254.170.23/creds",
		"::not a url",
	} {
		c := e.s.CredentialsClient("relative-without-slash", full)
		for _, u := range []string{"http://169.254.170.23/creds", "http://169.254.170.2/relative-without-slash"} {
			if resp, err := c.Get(u); !errors.Is(err, errDenied) {
				if err == nil {
					resp.Body.Close()
				}
				t.Errorf("full URI %q: %s reachable: %v", full, u, err)
			}
		}
	}
	if d := routes.calls(); len(d) != 0 {
		t.Fatalf("dialed %v", d)
	}
}

func TestCredentialsClientHonoursDenylistAndRedirects(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Egress.Deny = []string{"[fd00:ec2::254]"} })
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			fmt.Fprint(w, "TOKEN")
			return
		}
		http.Redirect(w, r, "http://example.com/steal", http.StatusFound)
	}))
	defer redirector.Close()
	routes := &credsRoutes{routes: map[netip.AddrPort]string{
		netip.MustParseAddrPort("169.254.169.254:80"): redirector.Listener.Addr().String(),
	}}
	e.s.dialCreds = routes.dial
	c := e.s.CredentialsClient("", "")
	if resp, err := do(c, http.MethodPut, "http://[fd00:ec2::254]/latest/api/token", map[string]string{imdsTTLHeader: "60"}); !errors.Is(err, errDenied) {
		if err == nil {
			resp.Body.Close()
		}
		t.Fatalf("egress.deny ignored: %v", err)
	}
	resp, err := do(c, http.MethodGet, "http://169.254.169.254/latest/meta-data/placement/region", map[string]string{imdsTokenHeader: "TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("redirect followed: %s", resp.Status)
	}
	resp.Body.Close()
	for _, ap := range routes.calls() {
		if ap.Addr().String() != "169.254.169.254" {
			t.Fatalf("dialed %s", ap)
		}
	}
}

func TestCredentialsDialAcceptsOnlyItsTarget(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	ap := netip.MustParseAddrPort(ln.Addr().String())
	c, err := credsDial(context.Background(), ap)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

// The credentials dialer is the only way to the metadata and container
// credential endpoints: the box proxy, web_fetch's public client and the
// system clients of providers, search and HTTP MCP servers all refuse them,
// also with AllowPrivate and an explicit allowlist entry, and creating a
// credentials client changes none of that.
func TestMetadataUnreachableOutsideCredentialsClient(t *testing.T) {
	targets := []string{"169.254.169.254", "169.254.170.2", "169.254.170.23", "fd00:ec2::254", "fd00:ec2::23"}
	e := newEnv(t, func(c *config.Config) {
		for _, a := range targets {
			c.Egress.Allow = append(c.Egress.Allow, net.JoinHostPort(a, "443"), "meta-"+strings.ReplaceAll(a, ":", "-")+".example")
		}
		c.Egress.AllowPlainHTTP = true
	})
	_ = e.s.CredentialsClient("/v2/credentials/x", "http://169.254.170.23/v1/credentials")
	for _, a := range targets {
		e.res.set("meta-"+strings.ReplaceAll(a, ":", "-")+".example", []string{a})
	}

	key, sock := e.openBox(core.BoxEgress{Extra: []string{"169.254.170.2:80"}})
	e.bind(key, false)
	for _, a := range targets {
		for _, authority := range []string{net.JoinHostPort(a, "443"), "meta-" + strings.ReplaceAll(a, ":", "-") + ".example:443"} {
			if code, msg := connectStatus(t, sock, authority); code != http.StatusForbidden || !strings.Contains(msg, "metadata") && !strings.Contains(msg, "blocked") {
				t.Errorf("box CONNECT %s = %d %q", authority, code, msg)
			}
		}
	}
	pc := proxyClient(sock, nil)
	for _, u := range []string{"http://169.254.170.2/v2/credentials/x", "http://169.254.169.254/latest/api/token"} {
		resp, err := pc.Get(u)
		if err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if code != http.StatusForbidden {
				t.Errorf("box plain-HTTP %s = %d", u, code)
			}
		}
	}

	pub := e.s.PublicClient(e.sess, e.principal())
	for _, a := range targets {
		u := "http://" + net.JoinHostPort(a, "80") + "/latest/meta-data/"
		if resp, err := pub.Get(u); !errors.Is(err, errBlocked) {
			if err == nil {
				resp.Body.Close()
			}
			t.Errorf("web_fetch client reached %s: %v", u, err)
		}
	}

	for _, name := range []string{"mcp:docs", "provider:ollama", "search:searxng"} {
		for _, a := range targets {
			hp := net.JoinHostPort(a, "80")
			c := e.s.SystemClient(core.SystemDest{Name: name, Hosts: []string{hp}, AllowPrivate: true})
			if resp, err := c.Get("http://" + hp + "/"); !errors.Is(err, errBlocked) {
				if err == nil {
					resp.Body.Close()
				}
				t.Errorf("%s system client reached %s: %v", name, hp, err)
			}
		}
	}
	if d := e.dial.calls(); len(d) != 0 {
		t.Fatalf("metadata addresses were dialed: %v", d)
	}
	for _, a := range targets {
		if controlFor(true)("tcp", net.JoinHostPort(a, "80"), nil) == nil {
			t.Errorf("the production dialer's Control admits %s", a)
		}
	}
}
