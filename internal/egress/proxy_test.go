package egress

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bothyhq/bothy/internal/config"
	"github.com/bothyhq/bothy/internal/core"
)

func hello(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "hello %s", r.URL.Path) }

// rawUpstream accepts one TCP connection at publicIP:443 and collects what
// it receives; send is written back after the first read.
type rawUpstream struct {
	got  chan []byte
	recv atomic.Int64
}

func (e *testEnv) rawUpstream(send []byte) *rawUpstream {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { ln.Close() })
	e.dial.route(publicIP+":443", ln.Addr().String())
	u := &rawUpstream{got: make(chan []byte, 1)}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			u.got <- nil
			return
		}
		defer c.Close()
		var all bytes.Buffer
		buf := make([]byte, 32<<10)
		sent := false
		for {
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			n, err := c.Read(buf)
			all.Write(buf[:n])
			u.recv.Add(int64(n))
			if n > 0 && !sent && send != nil {
				sent = true
				go func() { _, _ = c.Write(send) }()
			}
			if err != nil {
				u.got <- all.Bytes()
				return
			}
		}
	}()
	return u
}

func (u *rawUpstream) wait(t *testing.T) []byte {
	t.Helper()
	select {
	case b := <-u.got:
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("upstream never finished")
		return nil
	}
}

func TestOpenBoxCreatesSocket(t *testing.T) {
	e := newEnv(t, nil)
	key := core.DerivedID(core.PrefixBox, "session:x")
	sub, err := e.s.OpenBox(context.Background(), key, core.BoxEgress{Session: e.sess, Principal: e.principal()})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(key))
	if want := "egress/" + hex.EncodeToString(sum[:])[:32]; sub != want {
		t.Fatalf("subpath = %q, want %q", sub, want)
	}
	sock := filepath.Join(e.runDir, sub, "proxy.sock")
	fi, err := os.Lstat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode = %v, want socket 0660", fi.Mode())
	}
	again, err := e.s.OpenBox(context.Background(), key, core.BoxEgress{Session: e.sess, Principal: e.principal(), Extra: []string{"x.org:443"}})
	if err != nil || again != sub {
		t.Fatalf("reopen = %q, %v", again, err)
	}
	e.s.CloseBox(key)
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present after CloseBox: %v", err)
	}
	// A stale socket left by a previous process is replaced.
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := e.s.OpenBox(context.Background(), key, core.BoxEgress{}); err != nil {
		t.Fatalf("OpenBox over a stale socket: %v", err)
	}
	if _, err := e.s.OpenBox(context.Background(), "../../etc", core.BoxEgress{}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("bad key: %v", err)
	}
}

func TestConnectAllowedTunnelIsLoggedAndTaints(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Egress.Allow = []string{"example.com"} })
	e.res.set("example.com", []string{publicIP})
	_, tc := e.tlsUpstream(http.HandlerFunc(hello))
	key, sock := e.openBox(core.BoxEgress{})
	b := e.bind(key, true)

	resp, err := proxyClient(sock, tc).Get("https://example.com/hi")
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); got != "hello /hi" {
		t.Fatalf("body = %q", got)
	}
	recs := e.records(1)
	r := recs[0]
	if r.Dialer != "box" || r.Decision != "allow" || r.Host != "example.com" || r.Port != 443 || r.IP != publicIP ||
		r.Session != e.sess || r.Principal != e.principal() || r.Run != b.Run || r.Up == 0 || r.Down == 0 {
		t.Fatalf("record = %+v", r)
	}
	if !e.s.TakeActivity(key) {
		t.Fatal("TakeActivity = false after a tunnel moved bytes")
	}
	if e.s.TakeActivity(key) {
		t.Fatal("TakeActivity did not reset")
	}
}

func TestConnectRefusals(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Egress.Allow = []string{"example.com", "example.com:22", "*.wild.example"}
		c.Egress.Deny = []string{"bad.wild.example", "1.2.3.4"}
	})
	e.res.set("good.wild.example", []string{"1.2.3.4"})
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, false)
	cases := []struct{ authority, reason string }{
		{"example.com:22", "port 22 is not allowed"},
		{"example.com:80", "port 80 is not allowed"},
		{publicIP + ":443", "IP literals need an exact ip:port grant"},
		{"[2606:4700::1111]:443", "IP literals need an exact ip:port grant"},
		{"unknown.example:443", "not in the allowlist"},
		{"bad.wild.example:443", "denylisted"},
		{"good.wild.example:443", "denylisted"},
		{"0x7f000001:443", "IP literals"},
	}
	for _, c := range cases {
		_, resp := proxyConnect(t, sock, c.authority)
		msg := body(t, resp)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(msg, c.reason) || !strings.Contains(msg, "call net_allow") {
			t.Errorf("CONNECT %s = %d %q, want 403 mentioning %q", c.authority, resp.StatusCode, msg, c.reason)
		}
	}
	recs := e.records(len(cases))
	for i, r := range recs {
		if r.Decision != "deny" || !strings.Contains(r.Reason, cases[i].reason) {
			t.Errorf("record %d = %+v, want deny %q", i, r, cases[i].reason)
		}
	}
	if n := len(e.aud.All()); n != len(cases) {
		t.Errorf("audit entries = %d, want %d egress.deny", n, len(cases))
	}
	for _, a := range e.aud.Actions() {
		if a != "egress.deny" {
			t.Errorf("audit action %q", a)
		}
	}
	if len(e.dial.calls()) != 0 {
		t.Fatalf("refused CONNECTs were dialed: %v", e.dial.calls())
	}
}

func TestBlockedEventPublished(t *testing.T) {
	e := newEnv(t, nil)
	sub := e.bus.Subscribe(core.Filter{System: true, Types: []core.EventType{core.EvEgressBlocked}}, 8)
	defer sub.Close()
	key, sock := e.openBox(core.BoxEgress{})
	b := e.bind(key, false)
	connectStatus(t, sock, "pypi.org:443")
	select {
	case ev := <-sub.C():
		d := ev.Data.(core.EgressBlockedData)
		if ev.Principal != e.principal() || ev.Session != e.sess || ev.Run != b.Run || d.Host != "pypi.org" || d.Port != 443 {
			t.Fatalf("event = %+v %+v", ev, d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no egress.blocked event")
	}
}

func TestIPLiteralNeedsExactGrant(t *testing.T) {
	e := newEnv(t, nil)
	e.tlsUpstream(http.HandlerFunc(hello))
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, false)
	e.addGrant(core.Grant{Value: publicIP}) // no port: never matches an IP literal
	if code, _ := connectStatus(t, sock, publicIP+":443"); code != http.StatusForbidden {
		t.Fatalf("CONNECT without exact grant = %d", code)
	}
	e.addGrant(core.Grant{Value: publicIP + ":443"})
	c := proxyClient(sock, &tls.Config{InsecureSkipVerify: true})
	resp2, err := c.Get("https://" + publicIP + "/ip")
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp2); got != "hello /ip" {
		t.Fatalf("body = %q", got)
	}
}

func TestSNIMustMatchConnectHost(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Egress.Allow = []string{"example.com"} })
	e.res.set("example.com", []string{publicIP})
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, false)

	up := e.rawUpstream(nil)
	conn, resp := proxyConnect(t, sock, "example.com:443")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d", resp.StatusCode)
	}
	if _, err := conn.Write(clientHello(t, "evil.com")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := conn.Read(make([]byte, 16)); err == nil {
		t.Fatalf("tunnel stayed open after an SNI mismatch (read %d bytes)", n)
	}
	conn.Close()
	if got := up.wait(t); len(got) != 0 {
		t.Fatalf("upstream received %d bytes despite the SNI mismatch", len(got))
	}
	r := e.records(1)[0]
	if r.Decision != "deny" || !strings.Contains(r.Reason, "SNI") {
		t.Fatalf("record = %+v", r)
	}

	// A matching SNI is replayed byte for byte.
	up = e.rawUpstream(nil)
	conn, resp = proxyConnect(t, sock, "example.com:443")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d", resp.StatusCode)
	}
	h := clientHello(t, "Example.COM")
	if _, err := conn.Write(h); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "hello forwarded", func() bool { return up.recv.Load() >= int64(len(h)) })
	conn.Close()
	if got := up.wait(t); !bytes.Equal(got, h) {
		t.Fatalf("upstream got %d bytes, want the %d-byte ClientHello verbatim", len(got), len(h))
	}

	// Plain bytes instead of TLS close the tunnel.
	up = e.rawUpstream(nil)
	conn, _ = proxyConnect(t, sock, "example.com:443")
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 16)); err == nil {
		t.Fatal("non-TLS tunnel stayed open")
	}
	conn.Close()
	if got := up.wait(t); len(got) != 0 {
		t.Fatalf("upstream received non-TLS bytes: %q", got)
	}
}

func TestSharedTenantHostNeedsAdminGrant(t *testing.T) {
	e := newEnv(t, nil)
	host := "bucket.s3.amazonaws.com"
	e.res.set(host, []string{publicIP})
	e.tlsUpstream(http.HandlerFunc(hello))
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, false)
	try := func() int {
		code, _ := connectStatus(t, sock, host+":443")
		return code
	}
	e.addGrant(core.Grant{Value: host + ":443"})
	if code := try(); code != http.StatusForbidden {
		t.Fatalf("session grant admitted a shared-tenant host: %d", code)
	}
	e.addGrant(core.Grant{Value: host + ":443", Scope: core.GrantPrincipal, ScopeID: "", CreatedBy: e.member})
	if code := try(); code != http.StatusForbidden {
		t.Fatalf("member's principal grant admitted a shared-tenant host: %d", code)
	}
	recs := e.records(2)
	if !strings.Contains(recs[0].Reason, "shared-tenant") {
		t.Fatalf("reason = %q", recs[0].Reason)
	}
	e.addGrant(core.Grant{Value: host + ":443", Scope: core.GrantPrincipal, ScopeID: "", CreatedBy: e.user})
	if code := try(); code != http.StatusOK {
		t.Fatalf("admin principal grant refused: %d", code)
	}
}

func TestWildcardGrantNeedsAdminPrincipalScope(t *testing.T) {
	e := newEnv(t, nil)
	e.res.set("a.example.org", []string{publicIP})
	e.tlsUpstream(http.HandlerFunc(hello))
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, false)
	e.addGrant(core.Grant{Value: "*.example.org:443"})
	if code, _ := connectStatus(t, sock, "a.example.org:443"); code != http.StatusForbidden {
		t.Fatalf("session wildcard grant admitted: %d", code)
	}
	e.addGrant(core.Grant{Value: "*.example.org:443", Scope: core.GrantPrincipal, ScopeID: ""})
	if code, _ := connectStatus(t, sock, "a.example.org:443"); code != http.StatusOK {
		t.Fatalf("admin wildcard grant refused: %d", code)
	}
}

func TestExposedSessionIgnoresGrants(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Egress.Allow = []string{"allowed.example"} })
	e.res.set("example.com", []string{publicIP})
	e.res.set("allowed.example", []string{publicIP})
	e.tlsUpstream(http.HandlerFunc(hello))
	key, sock := e.openBox(core.BoxEgress{})
	e.addGrant(core.Grant{Value: "example.com:443"})
	e.addGrant(core.Grant{Value: "example.com:443", Scope: core.GrantPrincipal, ScopeID: ""})
	e.bind(key, true)
	if code, _ := connectStatus(t, sock, "example.com:443"); code != http.StatusOK {
		t.Fatalf("clean session with a grant: %d", code)
	}

	e.setTaint(core.TaintPrivate | core.TaintUntrusted)
	start := time.Now()
	code, msg := connectStatus(t, sock, "example.com:443")
	if code != http.StatusForbidden || !strings.Contains(msg, "grants are suspended") {
		t.Fatalf("exposed session = %d %q", code, msg)
	}
	if time.Since(start) > time.Second {
		t.Fatal("exposed session was held instead of refused at once")
	}
	if n := len(e.apr.All()); n != 0 {
		t.Fatalf("exposed session requested %d approvals", n)
	}
	if code, _ := connectStatus(t, sock, "allowed.example:443"); code != http.StatusOK {
		t.Fatalf("egress.allow must still apply when exposed: %d", code)
	}
}

func TestNoGrantsBoxUsesOnlyAllowlistAndExtra(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Egress.Presets.NPMPackages = true })
	e.res.set("example.com", []string{publicIP})
	e.res.set("api.mcp.example", []string{publicIP})
	e.res.set("registry.npmjs.org", []string{publicIP})
	e.tlsUpstream(http.HandlerFunc(hello))
	key, sock := e.openBox(core.BoxEgress{Session: e.sess, Principal: e.principal(), Extra: []string{"api.mcp.example:443"}, NoGrants: true})
	e.bind(key, true)
	e.addGrant(core.Grant{Value: "example.com:443"})
	if code, _ := connectStatus(t, sock, "example.com:443"); code != http.StatusForbidden {
		t.Fatalf("NoGrants box used a session grant: %d", code)
	}
	if n := len(e.apr.All()); n != 0 {
		t.Fatalf("NoGrants box requested %d approvals", n)
	}
	if code, _ := connectStatus(t, sock, "api.mcp.example:443"); code != http.StatusOK {
		t.Fatalf("Extra host refused: %d", code)
	}
	if code, _ := connectStatus(t, sock, "registry.npmjs.org:443"); code != http.StatusOK {
		t.Fatalf("npm preset refused: %d", code)
	}
}

func TestUploadCapAbortsConnection(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Egress.Allow = []string{"example.com"}
		c.Egress.Caps.SessionUpMB = 1
	})
	e.res.set("example.com", []string{publicIP})
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, false)
	up := e.rawUpstream(nil)
	conn, resp := proxyConnect(t, sock, "example.com:443")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d", resp.StatusCode)
	}
	h := clientHello(t, "example.com")
	go func() {
		_, _ = conn.Write(h)
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		for i := 0; i < 32; i++ {
			if _, err := conn.Write(chunk); err != nil {
				return
			}
		}
	}()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.Copy(io.Discard, conn)
	got := up.wait(t)
	if len(got) > 1<<20 {
		t.Fatalf("upstream received %d bytes, cap is %d", len(got), 1<<20)
	}
	r := e.records(1)[0]
	if r.Reason != "upload cap exceeded" || r.Up > 1<<20 {
		t.Fatalf("record = %+v", r)
	}
	if code, msg := connectStatus(t, sock, "example.com:443"); code != http.StatusForbidden || !strings.Contains(msg, "cap exhausted") {
		t.Fatalf("after the cap: %d %q", code, msg)
	}
}

func TestDownloadCapAbortsConnection(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Egress.Allow = []string{"example.com"}
		c.Egress.Caps.SessionDownMB = 1
	})
	e.res.set("example.com", []string{publicIP})
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, false)
	e.rawUpstream(bytes.Repeat([]byte("y"), 3<<20))
	conn, _ := proxyConnect(t, sock, "example.com:443")
	if _, err := conn.Write(clientHello(t, "example.com")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := io.Copy(io.Discard, conn)
	if n > 1<<20 {
		t.Fatalf("client received %d bytes, cap is %d", n, 1<<20)
	}
	if r := e.records(1)[0]; r.Reason != "download cap exceeded" {
		t.Fatalf("record = %+v", r)
	}
}

func TestPlainHTTPAbsoluteForm(t *testing.T) {
	var mu sync.Mutex
	var gotHost, gotPath, gotProxyAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHost, gotPath, gotProxyAuth = r.Host, r.URL.Path, r.Header.Get("Proxy-Authorization")
		mu.Unlock()
		fmt.Fprint(w, "plain")
	}))
	defer ts.Close()
	seen := func() (string, string, string) {
		mu.Lock()
		defer mu.Unlock()
		return gotHost, gotPath, gotProxyAuth
	}
	setup := func(plain bool) string {
		e := newEnv(t, func(c *config.Config) {
			c.Egress.Allow = []string{"example.com"}
			c.Egress.AllowPlainHTTP = plain
		})
		e.res.set("example.com", []string{publicIP})
		e.dial.route(publicIP+":80", ts.Listener.Addr().String())
		key, sock := e.openBox(core.BoxEgress{})
		e.bind(key, false)
		return sock
	}

	resp, err := proxyClient(setup(false), nil).Get("http://example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	if msg := body(t, resp); resp.StatusCode != http.StatusForbidden || !strings.Contains(msg, "plain HTTP") {
		t.Fatalf("plain HTTP without allow_plain_http = %d %q", resp.StatusCode, msg)
	}

	sock := setup(true)
	resp, err = proxyClient(sock, nil).Get("http://example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	got := body(t, resp)
	host, path, _ := seen()
	if got != "plain" || path != "/x" || host != "example.com" {
		t.Fatalf("body %q host %q path %q", got, host, path)
	}

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "GET http://example.com/y HTTP/1.1\r\nHost: evil.example\r\nProxy-Authorization: Basic eDp5\r\n\r\n")
	out, _ := io.ReadAll(c)
	host, _, auth := seen()
	if !strings.Contains(string(out), "plain") || host != "example.com" || auth != "" {
		t.Fatalf("forwarded Host %q Proxy-Authorization %q, response %q", host, auth, out)
	}
}

func TestMalformedProxyRequests(t *testing.T) {
	e := newEnv(t, nil)
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, false)
	for _, req := range []string{
		"garbage\r\n\r\n",
		"GET / HTTP/1.1\r\nHost: example.com\r\n\r\n",
		"GET https://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n",
		"CONNECT example.com HTTP/1.1\r\n\r\n",
	} {
		c, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(c, req)
		out, _ := io.ReadAll(c)
		c.Close()
		if !strings.HasPrefix(string(out), "HTTP/1.1 400 ") {
			t.Errorf("%q -> %q, want 400", req, out)
		}
	}
}

func TestCloseBoxEndsTunnels(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Egress.Allow = []string{"example.com"} })
	e.res.set("example.com", []string{publicIP})
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, false)
	e.rawUpstream(nil)
	conn, _ := proxyConnect(t, sock, "example.com:443")
	if _, err := conn.Write(clientHello(t, "example.com")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	e.s.CloseBox(key)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("tunnel survived CloseBox: %v", err)
	}
	if e.s.TakeActivity(key) {
		t.Fatal("closed box reports activity")
	}
}

func TestRunFlushesOnShutdown(t *testing.T) {
	e := newEnv(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.s.Run(ctx) }()
	key, sock := e.openBox(core.BoxEgress{})
	e.bind(key, false)
	connectStatus(t, sock, "nowhere.example:443")
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	rows, err := e.store.Egress().Query(context.Background(), core.SystemViewer(), "", core.Page{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows after shutdown = %d, %v", len(rows), err)
	}
	if _, err := e.s.OpenBox(context.Background(), key, core.BoxEgress{}); err == nil {
		t.Fatal("OpenBox after shutdown succeeded")
	}
}
