package bwrap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/rielicat/bothy/internal/core"
)

// command prepares one jail. The seccomp program goes to bwrap on fd 3
// through a pipe; Env is set explicitly because a nil Env would hand the
// gateway's environment (BOTHY_SECRET_*) to bwrap.
// The returned start func starts cmd and feeds the program.
func (b *Backend) command(ctx context.Context, args []string) (*exec.Cmd, func() error, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, b.o.Bwrap, args...)
	cmd.Env = []string{}
	cmd.ExtraFiles = []*os.File{r}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	prog := b.prog
	start := func() error {
		err := cmd.Start()
		r.Close()
		if err != nil {
			w.Close()
			return err
		}
		go func() {
			_, _ = w.Write(prog)
			w.Close()
		}()
		return nil
	}
	return cmd, start, nil
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	// The outer bwrap leads its own process group; killing it takes the
	// jail's PID namespace down with it (--die-with-parent).
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// Exec runs req in a fresh jail sharing j's workspace.
func (b *Backend) Exec(ctx context.Context, j Jail, req core.ExecRequest, onOut func(int, []byte)) (*core.ExecResult, error) {
	cmd, startCmd, err := b.command(ctx, Args(b.o, j, req.Cwd, req.Env, req.Timeout, req.Argv))
	if err != nil {
		return nil, err
	}
	cmd.Stdin = bytes.NewReader(req.Stdin)
	p := &jailProc{kill: func() { _ = killGroup(cmd) }}
	var mu sync.Mutex
	res := &core.ExecResult{}
	out := &capture{mu: &mu, stream: 1, max: req.MaxOutput, truncated: &res.Truncated, onOut: onOut, kill: p.kill}
	errw := &capture{mu: &mu, stream: 2, max: req.MaxOutput, truncated: &res.Truncated, onOut: onOut, kill: p.kill}
	cmd.Stdout, cmd.Stderr = out, errw
	start := time.Now()
	if err := startCmd(); err != nil {
		return nil, err
	}
	untrack := b.track(j.Key, p)
	err = cmd.Wait()
	res.Killed = untrack()
	res.Duration = time.Since(start)
	res.Stdout, res.Stderr = out.buf, errw.buf
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Exit = exitCode(ee.ProcessState)
	case errors.Is(err, exec.ErrWaitDelay):
	default:
		return res, err
	}
	res.TimedOut = res.Exit == 124 && res.Duration >= req.Timeout
	return res, nil
}

func exitCode(st *os.ProcessState) int {
	if ws, ok := st.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return st.ExitCode()
}

// Stdio starts a long-lived jail wired to the returned stream (MCP stdio).
// Its stderr is discarded: bwrap mode has no stderr ring.
func (b *Backend) Stdio(ctx context.Context, j Jail, argv []string, env map[string]string) (io.ReadWriteCloser, error) {
	// The jail outlives the call's ctx, so it is not bound to it.
	jctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd, startCmd, err := b.command(jctx, Args(b.o, j, "", env, stdioTimeout, argv))
	if err != nil {
		cancel()
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := startCmd(); err != nil {
		cancel()
		return nil, err
	}
	p := &jailProc{kill: func() { _ = killGroup(cmd) }}
	untrack := b.track(j.Key, p)
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		untrack()
		close(done)
	}()
	return &stdioJail{in: stdin, out: stdout, stop: func() {
		cancel()
		<-done
	}}, nil
}

type stdioJail struct {
	in   io.WriteCloser
	out  io.Reader
	once sync.Once
	stop func()
}

func (s *stdioJail) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *stdioJail) Write(p []byte) (int, error) { return s.in.Write(p) }

func (s *stdioJail) Close() error {
	s.once.Do(func() {
		s.in.Close()
		s.stop()
	})
	return nil
}

// track registers a running jail of key; untrack removes it and reports
// whether Kill ran meanwhile.
func (b *Backend) track(key string, p *jailProc) (untrack func() (killed bool)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active[key] == nil {
		b.active[key] = map[*jailProc]struct{}{}
	}
	b.active[key][p] = struct{}{}
	gen := b.gen[key]
	return func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		killed := b.gen[key] != gen
		delete(b.active[key], p)
		if len(b.active[key]) == 0 {
			delete(b.active, key)
			delete(b.gen, key)
		}
		return killed
	}
}
