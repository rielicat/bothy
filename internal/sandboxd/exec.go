package sandboxd

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/sandboxd/engine"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

const (
	killAfter = 5 * time.Second
	// drainLimit is how much output beyond max_output is read and dropped
	// before the stream is cut.
	drainLimit = 64 << 20
	// deadlineSlack lets sbx-init's own timeout and kill-after report first.
	deadlineSlack    = 10 * time.Second
	defaultMaxOutput = 64 << 10
)

// execPlan is a validated ExecReq.
type execPlan struct {
	argv      []string
	cwd       string
	env       []string
	stdin     []byte
	timeout   time.Duration
	maxOutput int
}

func (d *daemon) planExec(r *proto.ExecReq) (execPlan, error) {
	var p execPlan
	if err := proto.ValidateArgv(r.Argv); err != nil {
		return p, err
	}
	cwd, err := proto.CleanCwd(r.Cwd)
	if err != nil {
		return p, err
	}
	if err := proto.ValidateEnv(r.Env); err != nil {
		return p, err
	}
	if r.TimeoutMS < 1 || time.Duration(r.TimeoutMS)*time.Millisecond > d.cfg.MaxExec {
		return p, fmt.Errorf("%w: timeout_ms must be in [1, %d]", proto.ErrInvalid, d.cfg.MaxExec.Milliseconds())
	}
	if r.MaxOutput < 0 || r.MaxOutput > proto.MaxOutputCap {
		return p, fmt.Errorf("%w: max_output must be in [0, %d]", proto.ErrInvalid, proto.MaxOutputCap)
	}
	if r.StdinB64 != "" {
		if base64.StdEncoding.DecodedLen(len(r.StdinB64)) > proto.MaxStdin+3 {
			return p, fmt.Errorf("%w: stdin exceeds %d bytes", proto.ErrInvalid, proto.MaxStdin)
		}
		if p.stdin, err = base64.StdEncoding.DecodeString(r.StdinB64); err != nil {
			return p, fmt.Errorf("%w: stdin_b64: %w", proto.ErrInvalid, err)
		}
	}
	p.argv, p.cwd, p.env = r.Argv, cwd, envList(r.Env)
	p.timeout = time.Duration(r.TimeoutMS) * time.Millisecond
	p.maxOutput = r.MaxOutput
	if p.maxOutput == 0 {
		p.maxOutput = defaultMaxOutput
	}
	return p, nil
}

func envList(env map[string]string) []string {
	keys := slices.Sorted(maps.Keys(env))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

func workDir(cwd string) string {
	if cwd == "" {
		return "/workspace"
	}
	return "/workspace/" + cwd
}

// runCmd wraps argv in `sbx-init run` so every exec gets the rlimits, its own
// session and the in-box timeout (SPEC §2.7).
func runCmd(argv []string, timeout time.Duration) []string {
	return append([]string{sbxInit, "run", "--timeout", timeout.String(), "--kill-after", killAfter.String(), "--"}, argv...)
}

// frameWriter serializes NDJSON frames to the gateway.
type frameWriter struct {
	w     io.Writer
	flush func() error
	err   error
}

func (f *frameWriter) write(fr proto.Frame) {
	if f.err != nil {
		return
	}
	b, err := jsonLine(fr)
	if err != nil {
		f.err = err
		return
	}
	if _, f.err = f.w.Write(b); f.err == nil && f.flush != nil {
		f.err = f.flush()
	}
}

// outputCaps keeps max bytes per stream and drops the rest, up to drainLimit
// in total; past it errCut stops the stream.
type outputCaps struct {
	max       int
	kept      [3]int
	dropped   int
	truncated bool
}

var errCut = errors.New("output limit exceeded")

func (c *outputCaps) take(stream int, p []byte) ([]byte, error) {
	room := c.max - c.kept[stream]
	if room >= len(p) {
		c.kept[stream] += len(p)
		return p, nil
	}
	c.truncated = true
	keep := max(room, 0)
	c.kept[stream] += keep
	c.dropped += len(p) - keep
	if c.dropped > drainLimit {
		return p[:keep], errCut
	}
	return p[:keep], nil
}

// execRun is an exec that has started; its frames are not streamed yet, so
// start failures can still become proper HTTP errors.
type execRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	id     string
	stream ExecStream
	gen    uint64
	start  time.Time
}

func (d *daemon) beginExec(ctx context.Context, b *box, p execPlan) (*execRun, error) {
	d.mu.Lock()
	gen := b.killGen
	d.mu.Unlock()
	start := time.Now() // durations use the monotonic clock, not the injected one
	ctx, cancel := context.WithTimeout(ctx, p.timeout+killAfter+deadlineSlack)
	execID, err := d.eng.ExecCreate(ctx, b.id, engine.ExecConfig{
		User: boxUser, WorkingDir: workDir(p.cwd), Env: p.env, Cmd: runCmd(p.argv, p.timeout),
		AttachStdin: len(p.stdin) > 0, AttachStdout: true, AttachStderr: true,
	})
	if err == nil {
		var s ExecStream
		if s, err = d.eng.ExecStart(ctx, execID); err == nil {
			return &execRun{ctx: ctx, cancel: cancel, id: execID, stream: s, gen: gen, start: start}, nil
		}
	}
	cancel()
	if errors.Is(err, engine.ErrNotFound) || errors.Is(err, engine.ErrConflict) {
		// The container is gone or stopped: the gateway recreates the box.
		d.forget(b.key, b.id)
		return nil, errNotFound(b.key)
	}
	return nil, errEngine(err)
}

// streamExec feeds stdin, streams the output frames and writes the final one.
func (d *daemon) streamExec(r *execRun, b *box, p execPlan, fw *frameWriter) {
	defer r.cancel()
	defer r.stream.Close()
	if len(p.stdin) > 0 {
		go func() {
			_, _ = r.stream.Write(p.stdin)
			_ = r.stream.CloseWrite()
		}()
	}
	caps := outputCaps{max: p.maxOutput}
	derr := engine.Demux(r.stream, func(s int, chunk []byte) error {
		keep, cerr := caps.take(s, chunk)
		if len(keep) > 0 {
			fw.write(proto.Frame{Stream: s, Data: base64.StdEncoding.EncodeToString(keep)})
			if fw.err != nil {
				return fw.err
			}
		}
		return cerr
	})
	r.stream.Close()
	deadlineHit := errors.Is(r.ctx.Err(), context.DeadlineExceeded)
	final := proto.Frame{Truncated: caps.truncated}
	exit, ierr := d.awaitExit(context.WithoutCancel(r.ctx), r.id)
	elapsed := time.Since(r.start)
	final.Ms = elapsed.Milliseconds()
	switch {
	case deadlineHit:
		exit, final.TimedOut = exitTimeout, true
	case ierr != nil:
		final.Error = "exec inspect: " + ierr.Error()
	case exit == exitTimeout && elapsed >= p.timeout:
		final.TimedOut = true
	}
	if derr != nil && !errors.Is(derr, errCut) && !deadlineHit && fw.err == nil && final.Error == "" {
		final.Error = derr.Error()
	}
	d.mu.Lock()
	final.Killed = b.killGen != r.gen
	d.mu.Unlock()
	final.Exit = &exit
	fw.write(final)
}

const exitTimeout = 124

// awaitExit polls exec inspect until the process is gone; a stream cut short
// (output limit, deadline) can end before the process does.
func (d *daemon) awaitExit(ctx context.Context, execID string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		ins, err := d.eng.ExecInspect(ctx, execID)
		if err != nil {
			return -1, err
		}
		if !ins.Running {
			return ins.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 137, nil // still running: reported as killed-by-signal, the box reaper or Kill ends it
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// execCollect runs cmd directly (no sbx-init run wrapper) and collects its
// output; used for the canary, killall and count.
func (d *daemon) execCollect(ctx context.Context, id string, cmd []string, limit int) (stdout, stderr string, exit int, err error) {
	execID, err := d.eng.ExecCreate(ctx, id, engine.ExecConfig{User: boxUser, Cmd: cmd, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", "", -1, err
	}
	s, err := d.eng.ExecStart(ctx, execID)
	if err != nil {
		return "", "", -1, err
	}
	defer s.Close()
	var out, errb bytes.Buffer
	err = engine.Demux(s, func(st int, p []byte) error {
		dst := &out
		if st == engine.Stderr {
			dst = &errb
		}
		if dst.Len()+len(p) > limit {
			return errors.New("output too large")
		}
		dst.Write(p)
		return nil
	})
	if err != nil {
		return out.String(), errb.String(), -1, err
	}
	exit, err = d.awaitExit(ctx, execID)
	return out.String(), errb.String(), exit, err
}

// kill implements SPEC §2.7: sbx-init killall, then count until zero for up
// to a second; otherwise (or on any failure) kill and remove the container.
func (d *daemon) kill(ctx context.Context, key string) (proto.KillResp, error) {
	b, err := d.lookup(key)
	if err != nil {
		return proto.KillResp{}, err
	}
	d.mu.Lock()
	b.killGen++
	id := b.id
	d.mu.Unlock()
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	killed, ok := 0, false
	if out, _, exit, err := d.execCollect(ctx, id, []string{sbxInit, "killall"}, 1<<10); err == nil && exit == 0 {
		killed, _ = strconv.Atoi(strings.TrimSpace(out))
		for {
			out, _, exit, err := d.execCollect(ctx, id, []string{sbxInit, "count"}, 1<<10)
			if err != nil || exit != 0 {
				break
			}
			if n, perr := strconv.Atoi(strings.TrimSpace(out)); perr == nil && n == 0 {
				ok = true
				break
			}
			if time.Since(start) > time.Second {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	if ok {
		return proto.KillResp{Killed: killed}, nil
	}
	rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer rcancel()
	_ = d.eng.ContainerKill(rctx, id, "SIGKILL")
	if _, err := d.removeIf(rctx, b, nil); err != nil {
		return proto.KillResp{}, errEngine(err)
	}
	d.log.Info("box killed and removed", "key", key)
	return proto.KillResp{Killed: killed, Recreated: true}, nil
}

// stdioPump copies between the gateway connection and the exec stream until
// either side ends; process stderr goes to the box's ring. Once the gateway
// stops sending (it closed the stream), the process gets grace to exit on
// stdin EOF; then the stream is cut so a server that ignores EOF cannot hold
// the box's only stdio slot.
func stdioPump(gw io.ReadWriteCloser, gwIn io.Reader, s ExecStream, errRing io.Writer, grace time.Duration) {
	inDone, outDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(inDone)
		_, _ = io.Copy(s, gwIn)
		_ = s.CloseWrite()
	}()
	go func() {
		defer close(outDone)
		_ = engine.Demux(s, func(st int, p []byte) error {
			if st == engine.Stderr {
				_, _ = errRing.Write(p)
				return nil
			}
			_, err := gw.Write(p)
			return err
		})
	}()
	select {
	case <-outDone:
	case <-inDone:
		t := time.NewTimer(grace)
		select {
		case <-outDone:
		case <-t.C:
		}
		t.Stop()
	}
	gw.Close()
	s.Close()
	<-inDone
	<-outDone
}
