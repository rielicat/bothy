package egress

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bothyhq/bothy/internal/core"
)

const (
	boxGID          = 10001 // tool box processes; must be able to connect to the socket
	maxConnsPerBox  = 64
	headTimeout     = 30 * time.Second
	maxHeadBytes    = 64 << 10
	idleTimeout     = 5 * time.Minute
	maxUsage        = 4096
	maxReported     = 256
	reportWindow    = 10 * time.Minute
	maxHoldsPerCall = 8
)

type box struct {
	key    string
	ln     net.Listener
	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}
	use    *usage

	mu        sync.Mutex
	egress    core.BoxEgress
	binding   core.EgressBinding
	allowOnce map[string]map[string]bool   // tool call id -> host:port
	refused   map[string]map[string]string // tool call id -> host:port -> why its hold failed
	asked     map[string]map[string]bool   // tool call id -> host:port held for approval
	reported  map[string]bool              // tool call id + denial already audited
	reportAt  time.Time                    // when reported was last cleared
	conns     map[net.Conn]struct{}
	closed    bool

	// activity is set before any byte is passed on in either direction, so it
	// is already visible when an exec could have seen it (TakeActivity taints
	// exec output). Long-lived tunnels keep setting it.
	activity atomic.Bool
}

// usage is one session's byte totals against egress.caps. It outlives the
// session's box, which is destroyed when idle, so reopening the box does not
// reset the caps.
type usage struct {
	up, down atomic.Int64
	refs     int       // open boxes; guarded by Service.mu
	idleAt   time.Time // when refs last dropped to 0; guarded by Service.mu
}

func (b *box) snapshot() (core.BoxEgress, core.EgressBinding) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.egress, b.binding
}

func (b *box) track(c net.Conn) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.conns[c] = struct{}{}
	return true
}

func (b *box) untrack(c net.Conn) {
	b.mu.Lock()
	delete(b.conns, c)
	b.mu.Unlock()
}

func (b *box) allowedOnce(call, hp string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return call != "" && b.allowOnce[call][hp]
}

func (b *box) addAllowOnce(call, hp string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.allowOnce[call] == nil {
		b.allowOnce[call] = map[string]bool{}
	}
	b.allowOnce[call][hp] = true
}

// refusedOnce returns why an earlier hold for hp in this tool call failed, so
// a retrying client is not held, and the user not asked, again.
func (b *box) refusedOnce(call, hp string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.refused[call][hp]
	return r, ok && call != ""
}

// reserveHold admits a hold for hp unless the tool call already asked about
// maxHoldsPerCall other destinations, so code in the box cannot flood the
// user with approval cards.
func (b *box) reserveHold(call, hp string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	asked := b.asked[call]
	if asked[hp] {
		return true
	}
	if len(asked) >= maxHoldsPerCall {
		return false
	}
	if asked == nil {
		asked = map[string]bool{}
		b.asked[call] = asked
	}
	asked[hp] = true
	return true
}

func (b *box) addRefused(call, hp, reason string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.refused[call] == nil {
		b.refused[call] = map[string]string{}
	}
	b.refused[call][hp] = reason
}

// firstReport reports whether a denial is new for the tool call (or, for boxes
// that are never bound, such as MCP servers, within reportWindow). A retry
// loop thus yields one egress.deny audit entry and one egress.blocked event
// per destination and reason, since the append-only audit log cannot be
// pruned; egress_log still records every connection.
func (b *box) firstReport(call, what string) bool {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Sub(b.reportAt) >= reportWindow {
		clear(b.reported)
		b.reportAt = now
	}
	k := call + "\x00" + what
	if b.reported[k] || len(b.reported) >= maxReported {
		return false
	}
	b.reported[k] = true
	return true
}

func (b *box) close() {
	b.cancel()
	_ = b.ln.Close()
	b.mu.Lock()
	b.closed = true
	conns := make([]net.Conn, 0, len(b.conns))
	for c := range b.conns {
		conns = append(conns, c)
	}
	b.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// boxSubpath is "egress/" + hex(sha256(key))[:32], the run-volume subpath
// sandboxd mounts read-only at /run/egress in exactly that box.
func boxSubpath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "egress/" + hex.EncodeToString(sum[:])[:32]
}

// OpenBox creates /run/bothy/egress/<h>/proxy.sock for the box (the socket is
// the box identity) and returns the volume subpath "egress/<h>". Opening an
// open box only replaces its BoxEgress.
func (s *Service) OpenBox(ctx context.Context, key string, e core.BoxEgress) (string, error) {
	if !core.ValidID(core.PrefixBox, key) {
		return "", fmt.Errorf("egress: %w: box key %q", core.ErrInvalid, key)
	}
	e.Extra = slices.Clone(e.Extra)
	sub := boxSubpath(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", errors.New("egress: service stopped")
	}
	if s.runDir == "" {
		return "", fmt.Errorf("egress: %w: no run directory configured", core.ErrInvalid)
	}
	if b, ok := s.boxes[key]; ok {
		b.mu.Lock()
		b.egress = e
		b.mu.Unlock()
		return sub, nil
	}
	dir := filepath.Join(s.runDir, filepath.FromSlash(sub))
	if err := ensureSocketDir(dir); err != nil {
		return "", err
	}
	sock := filepath.Join(dir, "proxy.sock")
	if err := removeStaleSocket(sock); err != nil {
		return "", err
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", sock)
	if err != nil {
		return "", fmt.Errorf("egress: listen on %s: %w", sock, err)
	}
	if err := os.Chmod(sock, 0o660); err != nil { //nolint:gosec // the box connects through group 10001
		_ = ln.Close()
		return "", fmt.Errorf("egress: chmod %s: %w", sock, err)
	}
	if err := os.Chown(sock, -1, boxGID); err != nil {
		s.log.Warn("egress: cannot hand the proxy socket to the box group; boxes will fail closed",
			"socket", sock, "gid", boxGID, "err", err)
	}
	bctx, cancel := context.WithCancel(context.Background())
	b := &box{key: key, ln: ln, ctx: bctx, cancel: cancel, sem: make(chan struct{}, maxConnsPerBox),
		use: s.acquireUsage(usageKey(key, e)), egress: e, allowOnce: map[string]map[string]bool{},
		refused: map[string]map[string]string{}, asked: map[string]map[string]bool{}, reported: map[string]bool{},
		conns: map[net.Conn]struct{}{}}
	s.boxes[key] = b
	go s.serve(b)
	return sub, nil
}

func usageKey(key string, e core.BoxEgress) string {
	if e.Session != "" {
		return "session:" + string(e.Session)
	}
	return "box:" + key
}

// acquireUsage must be called with s.mu held.
func (s *Service) acquireUsage(k string) *usage {
	if u, ok := s.usage[k]; ok {
		u.refs++
		return u
	}
	if len(s.usage) >= maxUsage {
		s.pruneUsage()
	}
	u := &usage{refs: 1}
	s.usage[k] = u
	return u
}

// releaseUsage must be called with s.mu held.
func (s *Service) releaseUsage(u *usage) {
	if u.refs--; u.refs == 0 {
		u.idleAt = time.Now()
	}
}

// pruneUsage forgets the longest-idle meters until half the table is free;
// meters of open boxes are kept. Must be called with s.mu held.
func (s *Service) pruneUsage() {
	idle := make([]string, 0, len(s.usage))
	for k, u := range s.usage {
		if u.refs == 0 {
			idle = append(idle, k)
		}
	}
	slices.SortFunc(idle, func(a, b string) int { return s.usage[a].idleAt.Compare(s.usage[b].idleAt) })
	n := min(len(idle), len(s.usage)-maxUsage/2)
	for _, k := range idle[:n] {
		delete(s.usage, k)
	}
}

// ensureSocketDir creates the box's socket directory, normally made by the
// sandbox before OpenBox, and hands it to the box group, without which the
// box cannot reach the socket inside.
func ensureSocketDir(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("egress: create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("egress: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("egress: %s is not a directory", dir)
	}
	_ = os.Chown(dir, -1, boxGID) // the socket chown below warns when this fails
	return nil
}

func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("egress: %w", err)
	case fi.Mode()&fs.ModeSocket == 0:
		return fmt.Errorf("egress: %s exists and is not a socket", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("egress: remove stale socket: %w", err)
	}
	return nil
}

func (s *Service) CloseBox(key string) {
	s.mu.Lock()
	b := s.boxes[key]
	delete(s.boxes, key)
	if b != nil {
		s.releaseUsage(b.use)
	}
	s.mu.Unlock()
	if b != nil {
		b.close()
	}
}

func (s *Service) closeAll() {
	s.mu.Lock()
	s.closed = true
	boxes := s.boxes
	s.boxes = map[string]*box{}
	for _, b := range boxes {
		s.releaseUsage(b.use)
	}
	s.mu.Unlock()
	for _, b := range boxes {
		b.close()
	}
}

// startHandler registers a connection handler unless the service is
// stopping, so handlers.Add never races the shutdown Wait.
func (s *Service) startHandler() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.handlers.Add(1)
	return true
}

func (s *Service) box(key string) *box {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.boxes[key]
}

// Bind attaches the current run/tool call to a box before each exec, for
// logging, taint and hold-for-approval. allow_once admissions and refused
// holds belong to one tool call, so binding the next call drops them.
func (s *Service) Bind(key string, eb core.EgressBinding) {
	b := s.box(key)
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.binding.ToolCallID != eb.ToolCallID {
		clear(b.reported)
		b.reportAt = time.Now()
	}
	b.binding = eb
	if eb.Chat != nil {
		c := *eb.Chat
		b.binding.Chat = &c
	}
	for call := range b.allowOnce {
		if call != eb.ToolCallID {
			delete(b.allowOnce, call)
		}
	}
	for call := range b.refused {
		if call != eb.ToolCallID {
			delete(b.refused, call)
		}
	}
	for call := range b.asked {
		if call != eb.ToolCallID {
			delete(b.asked, call)
		}
	}
}

// TakeActivity reports whether the box moved bytes since the last call.
func (s *Service) TakeActivity(key string) bool {
	b := s.box(key)
	return b != nil && b.activity.Swap(false)
}

func (s *Service) serve(b *box) {
	for {
		c, err := b.ln.Accept()
		if err != nil {
			if b.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			s.log.Warn("egress: accept failed", "err", err)
			select {
			case <-time.After(100 * time.Millisecond):
			case <-b.ctx.Done():
				return
			}
			continue
		}
		select {
		case b.sem <- struct{}{}:
		default:
			writeResponse(c, http.StatusServiceUnavailable, "bothy: too many concurrent connections from this box\n")
			_ = c.Close()
			continue
		}
		if !b.track(c) || !s.startHandler() {
			b.untrack(c)
			<-b.sem
			_ = c.Close()
			continue
		}
		go func() {
			defer s.handlers.Done()
			defer func() { <-b.sem }()
			defer b.untrack(c)
			defer c.Close()
			defer func() {
				if p := recover(); p != nil {
					s.log.Error("egress: proxy handler panicked", "box", b.key, "panic", p)
				}
			}()
			s.handle(b, c)
		}()
	}
}

// headLimiter caps the request head; n < 0 lifts the cap for the body and
// the tunnel that follow.
type headLimiter struct {
	r io.Reader
	n int64
}

func (l *headLimiter) Read(p []byte) (int, error) {
	if l.n < 0 {
		return l.r.Read(p)
	}
	if l.n == 0 {
		return 0, errors.New("proxy request head too large")
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

type target struct {
	host    hostname
	port    int
	connect bool
}

func parseTarget(req *http.Request) (target, error) {
	if req.Method == http.MethodConnect {
		if req.URL == nil || req.URL.Host == "" || req.URL.Path != "" {
			return target{}, errors.New("CONNECT needs host:port")
		}
		h, port, err := splitHostPort(req.URL.Host)
		return target{host: h, port: port, connect: true}, err
	}
	if req.URL == nil || !req.URL.IsAbs() {
		return target{}, errors.New("not a proxy request")
	}
	if req.URL.Scheme != "http" {
		return target{}, fmt.Errorf("absolute-form %s is not supported; use CONNECT", req.URL.Scheme)
	}
	port := 80
	if p := req.URL.Port(); p != "" {
		n, err := parsePort(p)
		if err != nil {
			return target{}, err
		}
		port = n
	}
	h, err := parseHost(req.URL.Hostname())
	return target{host: h, port: port}, err
}

// handle serves one proxy connection. Refusals are logged and audited before
// the client hears about them.
func (s *Service) handle(b *box, c net.Conn) {
	start := time.Now()
	be, bind := b.snapshot()
	rec := core.EgressRecord{At: s.clock.Now(), Dialer: "box", Session: be.Session, Principal: be.Principal, Run: bind.Run}
	finish := func(decision, reason string) {
		rec.Decision, rec.Reason, rec.Duration = decision, reason, time.Since(start)
		s.record(rec)
	}

	lim := &headLimiter{r: c, n: maxHeadBytes}
	br := bufio.NewReader(lim)
	_ = c.SetReadDeadline(time.Now().Add(headTimeout))
	req, err := http.ReadRequest(br)
	if err != nil {
		finish("deny", "malformed proxy request")
		writeResponse(c, http.StatusBadRequest, "bothy: malformed proxy request\n")
		return
	}
	lim.n = -1
	_ = c.SetReadDeadline(time.Time{})

	t, err := parseTarget(req)
	if err != nil {
		rec.Host = truncate(req.Host, 255)
		finish("deny", "bad request: "+err.Error())
		writeResponse(c, http.StatusBadRequest, "bothy: "+err.Error()+"\n")
		return
	}
	rec.Host, rec.Port = t.host.name, t.port

	v := s.decide(b.ctx, b, be, bind, t)
	allowDecision, denyDecision := "allow", "deny"
	if v.held {
		allowDecision, denyDecision = "held_allow", "held_deny"
	}
	if !v.allow {
		finish(denyDecision, v.reason)
		s.refuse(b, c, be, bind, t, v.reason)
		return
	}

	pd := s.dialPinned(b.ctx, t.host, t.port, false)
	if pd.ip.IsValid() {
		rec.IP = pd.ip.String()
	}
	if pd.reason != "" {
		finish("deny", pd.reason)
		s.refuse(b, c, be, bind, t, pd.reason)
		return
	}
	if pd.err != nil {
		finish(allowDecision, "connect failed")
		writeResponse(c, http.StatusBadGateway, fmt.Sprintf("bothy: cannot reach %s\n", t.host.hostport(t.port)))
		return
	}
	up := pd.conn
	defer up.Close()

	var res spliceResult
	if t.connect {
		res = s.tunnel(b, c, br, up, t)
	} else {
		res = s.forward(b, c, br, up, req, t)
	}
	rec.Up, rec.Down = res.up, res.down
	if res.denied {
		finish(denyDecision, res.reason)
		s.denied(b, be, bind, t, res.reason)
		return
	}
	finish(allowDecision, res.reason)
}

// refuse answers 403 after publishing and auditing the denial.
func (s *Service) refuse(b *box, c net.Conn, be core.BoxEgress, bind core.EgressBinding, t target, reason string) {
	s.denied(b, be, bind, t, reason)
	writeResponse(c, http.StatusForbidden,
		fmt.Sprintf("bothy: egress to %s denied (%s). Ask the user or call net_allow.\n", t.host.hostport(t.port), reason))
}

func (s *Service) denied(b *box, be core.BoxEgress, bind core.EgressBinding, t target, reason string) {
	hp := t.host.hostport(t.port)
	if !b.firstReport(bind.ToolCallID, hp+"\x00"+reason) {
		return
	}
	s.publishBlocked(be.Principal, be.Session, bind.Run, t.host.name, t.port, reason)
	s.audit(b.ctx, "egress.deny", hp, "denied", be.Session, bind.Run, map[string]any{"dialer": "box", "reason": reason})
}

func writeResponse(c net.Conn, code int, body string) {
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		code, http.StatusText(code), len(body), body)
	_ = c.SetWriteDeadline(time.Time{})
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

type verdict struct {
	allow  bool
	held   bool
	reason string
}

// decide applies SPEC §2.8 steps 1–5 to one connection.
func (s *Service) decide(ctx context.Context, b *box, be core.BoxEgress, bind core.EgressBinding, t target) verdict {
	c := s.cfg().Egress
	h, port := t.host, t.port
	if !t.connect && !c.AllowPlainHTTP {
		return verdict{reason: "plain HTTP is not allowed"}
	}
	if !portAllowed(&c, port) {
		return verdict{reason: "port " + strconv.Itoa(port) + " is not allowed"}
	}
	if e, ok := matchAny(c.Deny, h, port, true, true); ok {
		return verdict{reason: "denylisted (" + e + ")"}
	}
	if s.capExhausted(b) {
		return verdict{reason: "session byte cap exhausted"}
	}
	exposed := s.exposed(ctx, be.Session)
	grants := !exposed && !be.NoGrants
	shared := sharedTenant(h, c.SharedTenantSuffixes)
	if _, ok := matchAny(c.Allow, h, port, true, false); ok {
		return verdict{allow: true}
	}
	if _, ok := matchAny(be.Extra, h, port, false, false); ok {
		return verdict{allow: true}
	}
	if !shared {
		if _, ok := matchAny(presetHosts(c.Presets), h, port, false, false); ok {
			return verdict{allow: true}
		}
	}
	if grants {
		if b.allowedOnce(bind.ToolCallID, h.hostport(port)) {
			return verdict{allow: true}
		}
		if s.grantAllows(ctx, be, bind, h, port, shared) {
			return verdict{allow: true}
		}
	}
	switch {
	case h.isIP():
		return verdict{reason: "IP literals need an exact ip:port grant"}
	case exposed:
		return verdict{reason: "session holds private and untrusted data, so grants are suspended"}
	}
	if grants && bind.Interactive && bind.ToolCallID != "" && c.HoldForApproval.D() > 0 &&
		s.policy != nil && s.approvals != nil && be.Principal != "" {
		if r, ok := b.refusedOnce(bind.ToolCallID, h.hostport(port)); ok {
			return verdict{reason: r + " earlier in this tool call"}
		}
		if !b.reserveHold(bind.ToolCallID, h.hostport(port)) {
			return verdict{reason: "too many approval requests in this tool call"}
		}
		r := s.holdForApproval(b, be, bind, h, port, shared)
		return verdict{allow: r.ok, held: true, reason: r.reason}
	}
	if shared {
		return verdict{reason: "shared-tenant host needs an admin grant or egress.allow"}
	}
	return verdict{reason: "not in the allowlist"}
}

func (s *Service) capExhausted(b *box) bool {
	up, down := s.caps()
	return up > 0 && b.use.up.Load() >= up || down > 0 && b.use.down.Load() >= down
}

// caps returns the byte caps; 0 disables a cap.
func (s *Service) caps() (up, down int64) {
	c := s.cfg().Egress.Caps
	return int64(c.SessionUpMB) << 20, int64(c.SessionDownMB) << 20
}

// exposed fails closed: a session that cannot be read counts as exposed.
func (s *Service) exposed(ctx context.Context, id core.SessionID) bool {
	if id == "" {
		return false
	}
	sess, err := s.store.Sessions().Get(ctx, core.SystemViewer(), id)
	if err != nil {
		s.log.Warn("egress: cannot read session taint; suspending grants", "session", id, "err", err)
		return true
	}
	return sess.Taint.Exposed()
}

// grantAllows checks active domain grants. Wildcards and shared-tenant hosts
// need a principal-scoped grant created by an admin. A grant limited to
// MaxUses is charged one use per admitted connection.
func (s *Service) grantAllows(ctx context.Context, be core.BoxEgress, bind core.EgressBinding, h hostname, port int, shared bool) bool {
	if be.Principal == "" {
		return false
	}
	gs, err := s.store.Grants().Active(ctx, be.Principal, be.Session, bind.Schedule, s.clock.Now())
	if err != nil {
		s.log.Warn("egress: cannot read grants", "principal", be.Principal, "err", err)
		return false
	}
	for _, g := range gs {
		if g.Kind != core.GrantDomain || g.MaxSE < core.SEExternalSend {
			continue
		}
		p, ok := parsePattern(g.Value)
		if !ok || p.port == 0 || !p.matches(h, port, false) {
			continue
		}
		if (p.wildcard || shared) && (g.Scope != core.GrantPrincipal || !s.isAdmin(ctx, g.CreatedBy)) {
			continue
		}
		if g.MaxUses > 0 {
			if err := s.store.Grants().Use(ctx, g.ID); err != nil {
				continue
			}
		}
		return true
	}
	return false
}

func (s *Service) isAdmin(ctx context.Context, u core.UserID) bool {
	if u == "" {
		return false
	}
	usr, err := s.store.Users().Get(ctx, u)
	return err == nil && usr.Role.IsAdmin() && !usr.Disabled
}
