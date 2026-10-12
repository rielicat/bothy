// Package sandboxtest provides fake core.Sandbox and core.Egress.
package sandboxtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/bothyhq/bothy/internal/core"
)

var (
	_ core.Sandbox = (*Sandbox)(nil)
	_ core.Egress  = (*Egress)(nil)
)

// Available is the status a Sandbox with an empty StatusV.Backend reports.
var Available = core.SandboxStatus{Backend: "fake", Available: true, Runtime: "fake"}

// Sandbox is safe for concurrent use. A StatusV with an empty Backend means
// Available; set e.g. StatusV{Backend: "none"} for a host without a sandbox,
// and then every method except Status, Usage and Watch fails with
// core.ErrNoSandbox, as with the real client.
//
// Kill cancels the context of every running Exec for that key; such an Exec
// returns a result with Killed set, exit 137 and a nil error. The result's
// stdout and stderr are passed to onOut after ExecFunc returns.
type Sandbox struct {
	core.Sandbox
	StatusV core.SandboxStatus
	// Exec handles every exec; default echoes argv to stdout with exit 0.
	ExecFunc func(ctx context.Context, key string, req core.ExecRequest) (*core.ExecResult, error)
	Killed   []string
	Ensured  []core.BoxSpec

	mu         sync.Mutex
	Execs      []ExecCall
	Destroyed  []string
	UsedBytes  map[core.WorkspaceID]int64
	QuotaBytes int64
	StdioFunc  func(ctx context.Context, key string, argv []string, env map[string]string) (io.ReadWriteCloser, error)
	watchers   []func(core.SandboxStatus)
	running    map[string]map[*execState]struct{}
}

// ExecCall is one recorded Exec.
type ExecCall struct {
	Key string
	Req core.ExecRequest
}

type execState struct {
	cancel context.CancelFunc
	killed bool
}

func (s *Sandbox) statusLocked() core.SandboxStatus {
	if s.StatusV.Backend == "" {
		return Available
	}
	return s.StatusV
}

func (s *Sandbox) Status(context.Context) core.SandboxStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

// SetStatus replaces StatusV and notifies Watch callbacks.
func (s *Sandbox) SetStatus(st core.SandboxStatus) {
	s.mu.Lock()
	s.StatusV = st
	cur := s.statusLocked()
	fns := slices.Clone(s.watchers)
	s.mu.Unlock()
	for _, fn := range fns {
		fn(cur)
	}
}

func (s *Sandbox) Watch(fn func(core.SandboxStatus)) {
	s.mu.Lock()
	s.watchers = append(s.watchers, fn)
	s.mu.Unlock()
}

func (s *Sandbox) availableLocked() error {
	if !s.statusLocked().Available {
		return core.ErrNoSandbox
	}
	return nil
}

func (s *Sandbox) Ensure(_ context.Context, spec core.BoxSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return err
	}
	s.Ensured = append(s.Ensured, spec)
	return nil
}

func (s *Sandbox) Usage(_ context.Context, ws core.WorkspaceID) (int64, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.UsedBytes[ws], s.QuotaBytes, nil
}

func (s *Sandbox) Exec(ctx context.Context, key string, req core.ExecRequest, onOut func(stream int, b []byte)) (*core.ExecResult, error) {
	s.mu.Lock()
	if err := s.availableLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.Execs = append(s.Execs, ExecCall{Key: key, Req: req})
	fn := s.ExecFunc
	ctx, cancel := context.WithCancel(ctx)
	st := &execState{cancel: cancel}
	if s.running == nil {
		s.running = map[string]map[*execState]struct{}{}
	}
	if s.running[key] == nil {
		s.running[key] = map[*execState]struct{}{}
	}
	s.running[key][st] = struct{}{}
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.running[key], st)
		s.mu.Unlock()
	}()

	var res *core.ExecResult
	var err error
	if fn != nil {
		res, err = fn(ctx, key, req)
	} else {
		res = &core.ExecResult{Stdout: []byte(strings.Join(req.Argv, " ") + "\n")}
	}
	s.mu.Lock()
	killed := st.killed
	s.mu.Unlock()
	if killed {
		if res == nil {
			res = &core.ExecResult{Exit: 137}
		}
		res.Killed = true
		if errors.Is(err, context.Canceled) {
			err = nil
		}
	}
	if err != nil {
		return res, err
	}
	if res == nil {
		res = &core.ExecResult{}
	}
	if onOut != nil {
		if len(res.Stdout) > 0 {
			onOut(1, res.Stdout)
		}
		if len(res.Stderr) > 0 {
			onOut(2, res.Stderr)
		}
	}
	return res, nil
}

// Stdio uses StdioFunc; without one it fails with core.ErrNotImplemented.
func (s *Sandbox) Stdio(ctx context.Context, key string, argv []string, env map[string]string) (io.ReadWriteCloser, error) {
	s.mu.Lock()
	err := s.availableLocked()
	fn := s.StdioFunc
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, fmt.Errorf("sandboxtest: Stdio without StdioFunc: %w", core.ErrNotImplemented)
	}
	return fn(ctx, key, argv, env)
}

// Kill is recorded even without a sandbox.
func (s *Sandbox) Kill(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Killed = append(s.Killed, key)
	if err := s.availableLocked(); err != nil {
		return err
	}
	for st := range s.running[key] {
		st.killed = true
		st.cancel()
	}
	return nil
}

func (s *Sandbox) Destroy(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.availableLocked(); err != nil {
		return err
	}
	s.Destroyed = append(s.Destroyed, key)
	return nil
}

// KilledKeys returns a copy of Killed.
func (s *Sandbox) KilledKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.Killed)
}

// ExecCalls returns a copy of Execs.
func (s *Sandbox) ExecCalls() []ExecCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.Execs)
}

// Egress is safe for concurrent use. Without a Client it hands out clients
// whose transport refuses every request, so no test reaches the network by
// accident. TakeActivity reports and clears Activity[key].
type Egress struct {
	core.Egress
	Client   *http.Client // returned by PublicClient and SystemClient (point it at an httptest server)
	Activity map[string]bool
	Bound    []core.EgressBinding

	mu     sync.Mutex
	Opened map[string]core.BoxEgress
	Closed []string
	Dests  []core.SystemDest
}

// OpenBox returns "egress/<sha256(key) hex[:12]>".
func (e *Egress) OpenBox(_ context.Context, key string, be core.BoxEgress) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Opened == nil {
		e.Opened = map[string]core.BoxEgress{}
	}
	e.Opened[key] = be
	sum := sha256.Sum256([]byte(key))
	return "egress/" + hex.EncodeToString(sum[:6]), nil
}

func (e *Egress) CloseBox(key string) {
	e.mu.Lock()
	e.Closed = append(e.Closed, key)
	e.mu.Unlock()
}

func (e *Egress) Bind(_ string, b core.EgressBinding) {
	e.mu.Lock()
	e.Bound = append(e.Bound, b)
	e.mu.Unlock()
}

// MarkActivity makes the next TakeActivity(key) report true.
func (e *Egress) MarkActivity(key string) {
	e.mu.Lock()
	if e.Activity == nil {
		e.Activity = map[string]bool{}
	}
	e.Activity[key] = true
	e.mu.Unlock()
}

func (e *Egress) TakeActivity(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	v := e.Activity[key]
	delete(e.Activity, key)
	return v
}

func (e *Egress) PublicClient(core.SessionID, core.PrincipalID) *http.Client { return e.client() }

func (e *Egress) SystemClient(d core.SystemDest) *http.Client {
	e.mu.Lock()
	e.Dests = append(e.Dests, d)
	e.mu.Unlock()
	return e.client()
}

func (e *Egress) client() *http.Client {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Client != nil {
		return e.Client
	}
	return &http.Client{Transport: refuse{}}
}

type refuse struct{}

func (refuse) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		r.Body.Close()
	}
	return nil, fmt.Errorf("sandboxtest: Egress.Client not set, refusing %s %s", r.Method, r.URL.Redacted())
}
