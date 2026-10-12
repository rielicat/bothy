// Package enginetest is an in-memory engine.API for tests of sandboxd and
// its gateway client. Containers are records; execs run small scripted
// programs that speak the engine's multiplexed stream, so the real exec,
// stdio, kill and self-test code paths run end to end without Docker.
package enginetest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bothyhq/bothy/internal/sandboxd/engine"
	"github.com/bothyhq/bothy/internal/sandboxd/proto"
)

// Fake implements engine.API. Exported fields may be set before use; read
// them back through the accessor methods while it runs.
type Fake struct {
	SocketPath   string
	ServerAPI    string      // default "1.47"
	EngineInfo   engine.Info // returned by Info
	NegotiateErr error
	PullErr      error
	// RunDir is sandboxd's run directory; the canary dials its egress socket
	// there to prove the egress_relay probe.
	RunDir string
	// FailChecks makes `sbx-init selftest --runtime <rt>` fail these probes.
	FailChecks map[string][]string
	// Survivors is what `sbx-init count` keeps reporting after killall
	// (processes that escaped it).
	Survivors int
	// Program overrides the scripted programs run by `sbx-init run`.
	Program func(ctx context.Context, p *Proc) int

	mu         sync.Mutex
	images     map[string]bool
	containers map[string]*container
	execs      map[string]*execState
	created    []*engine.ContainerConfig
	removed    []string
	pulled     []string
	nextID     int
	version    string
}

type container struct {
	id, name string
	cfg      *engine.ContainerConfig
	running  bool
	procs    map[*Proc]struct{}
}

type execState struct {
	c       *container
	cfg     engine.ExecConfig
	running bool
	exit    int
	started bool
}

// Proc is one running exec as seen by a program.
type Proc struct {
	Argv    []string // the command after `sbx-init run … --`
	Timeout time.Duration
	Cfg     engine.ExecConfig
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Labels  map[string]string
	cancel  context.CancelFunc
}

func New() *Fake {
	return &Fake{
		SocketPath: "/var/run/docker.sock",
		ServerAPI:  "1.47",
		EngineInfo: engine.Info{Runtimes: map[string]engine.Runtime{"runc": {Path: "runc"}}, SecurityOptions: []string{"name=seccomp,profile=builtin"}},
		images:     map[string]bool{},
		containers: map[string]*container{},
		execs:      map[string]*execState{},
	}
}

func apiErr(status int, msg string) error { return &engine.APIError{Status: status, Message: msg} }

// AddImage makes ref present locally.
func (f *Fake) AddImage(ref string) {
	f.mu.Lock()
	f.images[ref] = true
	f.mu.Unlock()
}

func (f *Fake) Socket() string { return f.SocketPath }

func (f *Fake) Negotiate(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if f.NegotiateErr != nil {
		return "", f.NegotiateErr
	}
	v := f.ServerAPI
	if engine.CompareVersions(v, engine.MaxAPI) > 0 {
		v = engine.MaxAPI
	}
	f.mu.Lock()
	f.version = v
	f.mu.Unlock()
	return f.ServerAPI, nil
}

func (f *Fake) APIVersion() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.version
}

func (f *Fake) Version(context.Context) (engine.Version, error) {
	return engine.Version{Version: "27.4.0", APIVersion: f.ServerAPI}, nil
}

func (f *Fake) Info(context.Context) (engine.Info, error) { return f.EngineInfo, nil }

func (f *Fake) ImageInspect(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.images[ref] {
		return apiErr(http.StatusNotFound, "no such image: "+ref)
	}
	return nil
}

func (f *Fake) ImagePull(_ context.Context, ref string) error {
	if f.PullErr != nil {
		return f.PullErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images[ref] = true
	f.pulled = append(f.pulled, ref)
	return nil
}

func (f *Fake) ContainerCreate(_ context.Context, name string, cfg *engine.ContainerConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.images[cfg.Image] {
		return "", apiErr(http.StatusNotFound, "no such image: "+cfg.Image)
	}
	for _, c := range f.containers {
		if c.name == name {
			return "", apiErr(http.StatusConflict, "name "+name+" is already in use")
		}
	}
	f.nextID++
	id := fmt.Sprintf("c%063d", f.nextID)
	f.containers[id] = &container{id: id, name: name, cfg: cfg, procs: map[*Proc]struct{}{}}
	f.created = append(f.created, cfg)
	return id, nil
}

func (f *Fake) lookupLocked(idOrName string) *container {
	if c := f.containers[idOrName]; c != nil {
		return c
	}
	for _, c := range f.containers {
		if c.name == idOrName {
			return c
		}
	}
	return nil
}

func (f *Fake) ContainerStart(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.lookupLocked(id)
	if c == nil {
		return apiErr(http.StatusNotFound, "no such container")
	}
	c.running = true
	return nil
}

func (f *Fake) ContainerKill(_ context.Context, id, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.lookupLocked(id)
	if c == nil {
		return apiErr(http.StatusNotFound, "no such container")
	}
	f.stopLocked(c)
	return nil
}

func (f *Fake) stopLocked(c *container) {
	c.running = false
	for p := range c.procs {
		p.cancel()
	}
}

func (f *Fake) ContainerRemove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.lookupLocked(id)
	if c == nil {
		return nil // as engine.Client: a missing container is not an error
	}
	f.stopLocked(c)
	delete(f.containers, c.id)
	f.removed = append(f.removed, c.name)
	return nil
}

// Vanish removes a container behind sandboxd's back (AutoRemove after its
// PID 1 died).
func (f *Fake) Vanish(name string) {
	_ = f.ContainerRemove(context.Background(), name)
}

func (f *Fake) ContainerList(_ context.Context, labels []string) ([]engine.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []engine.Container
	for _, c := range f.containers {
		ok := true
		for _, l := range labels {
			k, v, _ := strings.Cut(l, "=")
			if c.cfg.Labels[k] != v {
				ok = false
			}
		}
		if ok {
			state := "exited"
			if c.running {
				state = "running"
			}
			out = append(out, engine.Container{ID: c.id, Names: []string{"/" + c.name}, Labels: c.cfg.Labels, State: state})
		}
	}
	return out, nil
}

func (f *Fake) ContainerInspect(_ context.Context, id string) (*engine.ContainerJSON, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.lookupLocked(id)
	if c == nil {
		return nil, apiErr(http.StatusNotFound, "no such container")
	}
	var j engine.ContainerJSON
	j.ID = c.id
	j.State.Running = c.running
	j.Config.Labels = c.cfg.Labels
	return &j, nil
}

func (f *Fake) ExecCreate(_ context.Context, id string, cfg engine.ExecConfig) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.lookupLocked(id)
	if c == nil {
		return "", apiErr(http.StatusNotFound, "no such container")
	}
	if !c.running {
		return "", apiErr(http.StatusConflict, "container is not running")
	}
	f.nextID++
	eid := fmt.Sprintf("e%063d", f.nextID)
	f.execs[eid] = &execState{c: c, cfg: cfg}
	return eid, nil
}

func (f *Fake) ExecInspect(_ context.Context, id string) (engine.ExecInspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.execs[id]
	if e == nil {
		return engine.ExecInspect{}, apiErr(http.StatusNotFound, "no such exec")
	}
	return engine.ExecInspect{Running: e.running, ExitCode: e.exit}, nil
}

// ExecStart runs the exec's program in a goroutine behind pipes.
func (f *Fake) ExecStart(_ context.Context, id string) (engine.ExecStream, error) {
	f.mu.Lock()
	e := f.execs[id]
	if e == nil || e.started {
		f.mu.Unlock()
		return nil, apiErr(http.StatusNotFound, "no such exec")
	}
	if !e.c.running {
		f.mu.Unlock()
		return nil, apiErr(http.StatusConflict, "container is not running")
	}
	e.started, e.running = true, true
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	mux := &muxWriter{w: outW}
	p := &Proc{Cfg: e.cfg, Stdin: inR, Stdout: mux.stream(engine.Stdout), Stderr: mux.stream(engine.Stderr),
		Labels: maps.Clone(e.c.cfg.Labels), cancel: cancel}
	if !e.cfg.AttachStdin {
		p.Stdin = strings.NewReader("")
	}
	e.c.procs[p] = struct{}{}
	c := e.c
	f.mu.Unlock()
	go func() {
		code := f.run(ctx, c, p)
		cancel()
		f.mu.Lock()
		e.running, e.exit = false, code
		delete(c.procs, p)
		f.mu.Unlock()
		outW.Close()
		inR.Close()
	}()
	return &stream{r: outR, w: inW}, nil
}

type stream struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (s *stream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *stream) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *stream) CloseWrite() error           { return s.w.Close() }
func (s *stream) Close() error {
	s.w.Close()
	return s.r.Close()
}

// muxWriter frames writes onto the multiplexed stream.
type muxWriter struct {
	mu sync.Mutex
	w  io.Writer
}

type streamWriter struct {
	m  *muxWriter
	id int
}

func (m *muxWriter) stream(id int) io.Writer { return streamWriter{m, id} }

func (s streamWriter) Write(p []byte) (int, error) {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if _, err := s.m.w.Write(engine.AppendFrame(nil, s.id, p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (f *Fake) run(ctx context.Context, c *container, p *Proc) int {
	cmd := p.Cfg.Cmd
	if len(cmd) < 2 || cmd[0] != "/opt/bothy/sbx-init" {
		fmt.Fprintln(p.Stderr, "enginetest: only sbx-init commands run here")
		return 127
	}
	switch cmd[1] {
	case "killall":
		f.mu.Lock()
		n := 0
		for q := range c.procs {
			if q != p {
				q.cancel()
				n++
			}
		}
		f.mu.Unlock()
		fmt.Fprintln(p.Stdout, n+f.Survivors)
		return 0
	case "count":
		fmt.Fprintln(p.Stdout, f.Survivors)
		return 0
	case "selftest":
		return f.selftest(p, cmd[2:])
	case "run":
		i := slices.Index(cmd, "--")
		if i < 0 {
			return 125
		}
		for j := 2; j+1 < i; j += 2 {
			if cmd[j] == "--timeout" {
				p.Timeout, _ = time.ParseDuration(cmd[j+1])
			}
		}
		p.Argv = cmd[i+1:]
		if p.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeoutCause(ctx, p.Timeout, errTimeout)
			defer cancel()
		}
		if f.Program != nil {
			return f.Program(ctx, p)
		}
		return Script(ctx, p)
	}
	return 125
}

var errTimeout = fmt.Errorf("enginetest: sbx-init run timeout")

// Script is the default program set of `sbx-init run`:
//
//	echo ARGS…     stdout ARGS joined by spaces
//	warn ARGS…     the same on stderr
//	cat            stdin to stdout
//	exit N         exit status N
//	sleep          block until killed (137) or timed out (124)
//	flood N        N bytes of 'x' on stdout
//	pwd            the exec's working directory
//	env            the exec's env entries, one per line
func Script(ctx context.Context, p *Proc) int {
	args := p.Argv[1:]
	switch p.Argv[0] {
	case "echo":
		fmt.Fprintln(p.Stdout, strings.Join(args, " "))
	case "warn":
		fmt.Fprintln(p.Stderr, strings.Join(args, " "))
	case "cat":
		_, _ = io.Copy(p.Stdout, p.Stdin)
	case "exit":
		n, _ := strconv.Atoi(args[0])
		return n
	case "sleep":
		<-ctx.Done()
		if errors.Is(context.Cause(ctx), errTimeout) {
			return 124
		}
		return 137
	case "flood":
		n, _ := strconv.Atoi(args[0])
		chunk := []byte(strings.Repeat("x", 32<<10))
		for n > 0 {
			k := min(n, len(chunk))
			if _, err := p.Stdout.Write(chunk[:k]); err != nil {
				return 141
			}
			n -= k
		}
	case "pwd":
		fmt.Fprintln(p.Stdout, p.Cfg.WorkingDir)
	case "env":
		for _, kv := range p.Cfg.Env {
			fmt.Fprintln(p.Stdout, kv)
		}
	default:
		fmt.Fprintf(p.Stderr, "%s: command not found\n", p.Argv[0])
		return 127
	}
	return 0
}

func (f *Fake) selftest(p *Proc, args []string) int {
	runtime, nonce := "runc", ""
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--runtime":
			runtime = args[i+1]
		case "--canary":
			nonce = args[i+1]
		}
	}
	fail := map[string]bool{}
	for _, n := range f.FailChecks[runtime] {
		fail[n] = true
	}
	var checks []proto.Check
	for _, name := range append(slices.Clone(proto.IsolationChecks), proto.CheckEgressRelay) {
		c := proto.Check{Name: name, OK: !fail[name], Detail: "fake"}
		if name == proto.CheckEgressRelay && c.OK {
			c.OK, c.Detail = f.dialCanary(p.Labels["bothy.key"], nonce)
		}
		checks = append(checks, c)
	}
	_ = json.NewEncoder(p.Stdout).Encode(checks)
	return 0
}

func (f *Fake) dialCanary(key, nonce string) (bool, string) {
	if f.RunDir == "" {
		return true, "fake (no run dir)"
	}
	sock := filepath.Join(f.RunDir, proto.EgressSubpath(key), "proxy.sock")
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(context.Background(), "unix", sock)
	if err != nil {
		return false, err.Error()
	}
	defer c.Close()
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return false, err.Error()
	}
	if strings.TrimSpace(line) != "bothy-canary "+nonce {
		return false, "wrong canary reply"
	}
	return true, "reached the canary listener"
}

// Created returns the configs of every container created so far.
func (f *Fake) Created() []*engine.ContainerConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.created)
}

// Removed returns the names of removed containers in order.
func (f *Fake) Removed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.removed)
}

// Pulled returns the refs pulled so far.
func (f *Fake) Pulled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.pulled)
}

// Running returns the names of running containers, sorted.
func (f *Fake) Running() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.containers {
		if c.running {
			out = append(out, c.name)
		}
	}
	slices.Sort(out)
	return out
}

// AddContainer registers a running container as if an earlier sandboxd had
// created it.
func (f *Fake) AddContainer(name string, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("c%063d", f.nextID)
	f.containers[id] = &container{id: id, name: name, cfg: &engine.ContainerConfig{Labels: labels}, running: true,
		procs: map[*Proc]struct{}{}}
}

var _ engine.API = (*Fake)(nil)
