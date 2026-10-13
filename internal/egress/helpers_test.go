package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/config"
	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/events"
	"github.com/rielicat/bothy/internal/testkit/fakes"
	"github.com/rielicat/bothy/internal/testkit/memstore"
)

// publicIP stands in for a public server; fakeDialer routes it to httptest.
const publicIP = "93.184.216.34"

type fakeResolver struct {
	mu      sync.Mutex
	answers map[string][][]netip.Addr
	calls   map[string]int
}

// set registers successive answers for host; the last one repeats.
func (r *fakeResolver) set(host string, answers ...[]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answers == nil {
		r.answers, r.calls = map[string][][]netip.Addr{}, map[string]int{}
	}
	var list [][]netip.Addr
	for _, a := range answers {
		var addrs []netip.Addr
		for _, s := range a {
			addrs = append(addrs, netip.MustParseAddr(s))
		}
		list = append(list, addrs)
	}
	r.answers[host] = list
}

func (r *fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.answers[host]
	if len(list) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	i := min(r.calls[host], len(list)-1)
	r.calls[host]++
	return list[i], nil
}

func (r *fakeResolver) count(host string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[host]
}

// fakeDialer applies the production Control check, then routes address
// literals to local listeners.
type fakeDialer struct {
	mu     sync.Mutex
	routes map[string]string
	dialed []string
}

func (d *fakeDialer) route(from, to string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.routes == nil {
		d.routes = map[string]string{}
	}
	d.routes[from] = to
}

func (d *fakeDialer) dial(ctx context.Context, network, addr string, allowPrivate bool) (net.Conn, error) {
	if err := controlFor(allowPrivate)(network, addr, nil); err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.dialed = append(d.dialed, addr)
	to, ok := d.routes[addr]
	d.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fake dial: no route to %s", addr)
	}
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp", to)
}

func (d *fakeDialer) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dialed...)
}

// recPolicy records grants under its own lock so tests can read them while
// proxy goroutines are still running.
type recPolicy struct {
	*fakes.Policy
	mu     sync.Mutex
	grants []core.Grant
	inputs []core.PolicyInput
}

func (p *recPolicy) Grant(ctx context.Context, g core.Grant) (core.GrantID, error) {
	p.mu.Lock()
	p.grants = append(p.grants, g)
	p.mu.Unlock()
	return p.Policy.Grant(ctx, g)
}

func (p *recPolicy) Decide(ctx context.Context, in core.PolicyInput) core.Decision {
	p.mu.Lock()
	p.inputs = append(p.inputs, in)
	p.mu.Unlock()
	return p.Policy.Decide(ctx, in)
}

func (p *recPolicy) decided() []core.PolicyInput {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]core.PolicyInput(nil), p.inputs...)
}

func (p *recPolicy) recorded() []core.Grant {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]core.Grant(nil), p.grants...)
}

type testEnv struct {
	t      *testing.T
	s      *Service
	cfg    *config.Config
	store  core.Store
	pol    *recPolicy
	apr    *fakes.Approvals
	aud    *fakes.Auditor
	bus    *events.Bus
	vault  *fakes.Vault
	res    *fakeResolver
	dial   *fakeDialer
	runDir string
	user   core.UserID
	member core.UserID
	sess   core.SessionID
	run    *core.Run
}

func newEnv(t *testing.T, mut func(c *config.Config)) *testEnv {
	t.Helper()
	ctx := context.Background()
	c := config.Default()
	c.Egress.HoldForApproval = config.Duration(2 * time.Second)
	if mut != nil {
		mut(c)
	}
	st := memstore.New()
	owner := &core.User{Username: "alice", Role: core.RoleOwner}
	member := &core.User{Username: "bob", Role: core.RoleMember}
	for _, u := range []*core.User{owner, member} {
		if err := st.Users().Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	sess := &core.Session{Principal: core.UserPrincipal(owner.ID), Origin: "web", Title: "t"}
	if err := st.Sessions().Create(ctx, sess); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "egr") // unix socket paths must stay short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e := &testEnv{
		t: t, cfg: c, store: st,
		pol:   &recPolicy{Policy: &fakes.Policy{Default: core.VerdictAsk}},
		apr:   &fakes.Approvals{Manual: true},
		aud:   &fakes.Auditor{},
		bus:   events.New(events.Options{}),
		vault: fakes.NewVault(map[string]string{"brave_api_key": "BSA-secret-key", "tavily_api_key": "tvly-secret-key"}),
		res:   &fakeResolver{}, dial: &fakeDialer{}, runDir: dir,
		user: owner.ID, member: member.ID, sess: sess.ID,
	}
	e.s, err = New(Deps{Config: config.Static(c), Store: st, Vault: e.vault, Approvals: e.apr, Policy: e.pol,
		Bus: e.bus, Audit: e.aud, RunDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	e.s.resolver = e.res
	e.s.dialAddr = e.dial.dial
	t.Cleanup(e.s.closeAll)
	return e
}

func (e *testEnv) principal() core.PrincipalID { return core.UserPrincipal(e.user) }

func (e *testEnv) setTaint(t core.Taint) {
	e.t.Helper()
	if err := e.store.Sessions().SetTaint(context.Background(), e.sess, t); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) addGrant(g core.Grant) {
	e.t.Helper()
	if g.Principal == "" {
		g.Principal = e.principal()
	}
	if g.Scope == "" {
		g.Scope, g.ScopeID = core.GrantSession, string(e.sess)
	}
	if g.MaxSE == 0 {
		g.MaxSE = core.SEExternalSend
	}
	if g.CreatedBy == "" {
		g.CreatedBy = e.user
	}
	g.Kind = core.GrantDomain
	g.CreatedAt = time.Now()
	if err := e.store.Grants().Add(context.Background(), &g); err != nil {
		e.t.Fatal(err)
	}
}

// openBox opens the session box and returns its key and socket path.
func (e *testEnv) openBox(be core.BoxEgress) (string, string) {
	e.t.Helper()
	if be.Session == "" && be.Principal == "" {
		be.Session, be.Principal = e.sess, e.principal()
	}
	key := core.DerivedID(core.PrefixBox, "session:"+string(be.Session)+string(be.Principal))
	sub, err := e.s.OpenBox(context.Background(), key, be)
	if err != nil {
		e.t.Fatal(err)
	}
	return key, filepath.Join(e.runDir, sub, "proxy.sock")
}

// bind starts a running run (ending the previous one) and binds its next
// tool call to the box.
func (e *testEnv) bind(key string, interactive bool) core.EgressBinding {
	e.t.Helper()
	trigger := core.TrigMessage
	if !interactive {
		trigger = core.TrigSchedule
	}
	e.endRun()
	e.run = &core.Run{Session: e.sess, Principal: e.principal(), User: e.user, Trigger: trigger, Status: core.RunRunning}
	if err := e.store.Runs().Create(context.Background(), e.run); err != nil {
		e.t.Fatal(err)
	}
	b := core.EgressBinding{Run: e.run.ID, ToolCallID: core.NewID(core.PrefixToolCall),
		User: e.user, Role: core.RoleOwner, Trigger: trigger, Interactive: interactive, Context: "Web · test"}
	e.s.Bind(key, b)
	return b
}

func (e *testEnv) endRun() {
	e.t.Helper()
	if e.run == nil {
		return
	}
	e.run.Status = core.RunDone
	if err := e.store.Runs().Update(context.Background(), e.run); err != nil {
		e.t.Fatal(err)
	}
}

// records flushes until at least n egress_log rows exist and returns them
// oldest first.
func (e *testEnv) records(n int) []core.EgressRecord {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_ = e.s.flush(context.Background())
		rows, err := e.store.Egress().Query(context.Background(), core.SystemViewer(), "", core.Page{Limit: 500})
		if err != nil {
			e.t.Fatal(err)
		}
		if len(rows) >= n || time.Now().After(deadline) {
			out := make([]core.EgressRecord, len(rows))
			for i, r := range rows {
				out[len(rows)-1-i] = *r
			}
			if len(out) < n {
				e.t.Fatalf("got %d egress_log rows, want %d: %+v", len(out), n, out)
			}
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// bufConn reads through a bufio.Reader that may hold bytes already read.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// proxyConnect sends CONNECT over the box socket and returns the response.
func proxyConnect(t *testing.T, sock, authority string) (net.Conn, *http.Response) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return bufConn{Conn: c, r: br}, resp
}

// connectStatus sends CONNECT and closes the connection again; a refusal's
// body is returned.
func connectStatus(t *testing.T, sock, authority string) (int, string) {
	t.Helper()
	code, msg, err := rawConnect(sock, authority)
	if err != nil {
		t.Fatal(err)
	}
	return code, msg
}

func rawConnect(sock, authority string) (int, string, error) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return 0, "", err
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return 0, "", err
	}
	if resp.StatusCode == http.StatusOK {
		return resp.StatusCode, "", nil
	}
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), err
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return string(b)
}

// tlsUpstream starts an HTTPS server reachable as publicIP:443 and returns a
// client TLS config trusting it.
func (e *testEnv) tlsUpstream(h http.Handler) (*httptest.Server, *tls.Config) {
	ts := httptest.NewUnstartedServer(h)
	ts.Config.ErrorLog = log.New(io.Discard, "", 0) // aborted test tunnels are expected
	ts.StartTLS()
	e.t.Cleanup(ts.Close)
	e.dial.route(publicIP+":443", ts.Listener.Addr().String())
	pool := x509.NewCertPool()
	pool.AddCert(ts.Certificate())
	return ts, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// proxyClient sends every request through the box socket as a proxy.
func proxyClient(sock string, tc *tls.Config) *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy: http.ProxyURL(mustURL("http://proxy.invalid")),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		TLSClientConfig:   tc,
		DisableKeepAlives: true,
	}}
}

// clientHello returns the first TLS record a client sends for serverName.
func clientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	a, b := net.Pipe()
	defer b.Close()
	go func() {
		_ = tls.Client(a, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}).Handshake()
		a.Close()
	}()
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(b, hdr); err != nil {
		t.Fatal(err)
	}
	n := int(hdr[3])<<8 | int(hdr[4])
	rest := make([]byte, n)
	if _, err := io.ReadFull(b, rest); err != nil {
		t.Fatal(err)
	}
	return append(hdr, rest...)
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// closeIdle closes pooled system connections so their egress_log rows are
// written.
func (e *testEnv) closeIdle() {
	e.s.sysMu.Lock()
	defer e.s.sysMu.Unlock()
	for _, c := range e.s.sysClients {
		c.CloseIdleConnections()
	}
}
