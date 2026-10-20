package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

// sandboxdBackend talks to the sidecar over its unix socket. The socket is
// local IPC, not egress, so it gets its own transport rather than an
// Egress client.
type sandboxdBackend struct {
	socket string
	hc     *http.Client
	group  int // boxes run as 10001:10001 and the gateway shares group 10001
}

const sandboxdBase = "http://sandboxd/v" + proto.Version

func newSandboxdBackend(socket string) *sandboxdBackend {
	b := &sandboxdBackend{socket: socket, group: proto.BoxUID}
	b.hc = &http.Client{Transport: &http.Transport{
		DialContext:        b.dial,
		DisableCompression: true,
		MaxIdleConns:       4,
		IdleConnTimeout:    30 * time.Second,
		Proxy:              nil,
	}}
	return b
}

func (b *sandboxdBackend) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", b.socket)
}

func (b *sandboxdBackend) name() string { return "sandboxd" }

func (b *sandboxdBackend) gid() int { return b.group }

func (b *sandboxdBackend) health(ctx context.Context) (proto.Health, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var h proto.Health
	err := b.call(ctx, http.MethodGet, "/health", nil, &h)
	return h, err
}

func (b *sandboxdBackend) probe(ctx context.Context) core.SandboxStatus {
	h, err := b.health(ctx)
	return statusFromHealth(h, err)
}

func statusFromHealth(h proto.Health, err error) core.SandboxStatus {
	st := core.SandboxStatus{Backend: "sandboxd", SelfTest: []core.SelfTestCheck{}}
	if err != nil {
		st.Reason = "sandboxd is not reachable: " + err.Error()
		st.Fix = sandboxdFix
		return st
	}
	st.Available, st.Reason, st.Fix = h.OK, h.Reason, h.Fix
	st.Runtime, st.Rootless = h.Runtime, h.Rootless
	if h.Engine != "" {
		st.Engine = h.Engine
		switch {
		case h.API != "" && h.Mounts == proto.MountsBind:
			st.Engine += " (api " + h.API + ", bind mounts)"
		case h.API != "":
			st.Engine += " (api " + h.API + ")"
		}
	}
	for _, c := range h.SelfTest {
		st.SelfTest = append(st.SelfTest, core.SelfTestCheck{Name: c.Name, OK: c.OK, Detail: c.Detail})
	}
	for _, p := range h.Profiles {
		st.Profiles = append(st.Profiles, core.BoxProfile(p))
	}
	return st
}

func (b *sandboxdBackend) create(ctx context.Context, spec core.BoxSpec) error {
	req := proto.CreateReq{Key: spec.Key, Profile: string(spec.Profile), Workspace: string(spec.Workspace),
		MCPTree: spec.MCPTree, MCPTreeRW: spec.MCPTreeRW,
		Limits: proto.Limits{MemoryMB: spec.Limits.MemoryMB, CPUs: spec.Limits.CPUs, Pids: spec.Limits.Pids}}
	var resp proto.CreateResp
	return b.call(ctx, http.MethodPost, "/boxes", req, &resp)
}

func (b *sandboxdBackend) exec(ctx context.Context, key string, req core.ExecRequest, onOut func(int, []byte)) (*core.ExecResult, error) {
	body, err := json.Marshal(proto.ExecReq{
		Argv: req.Argv, Cwd: req.Cwd, Env: req.Env, StdinB64: base64.StdEncoding.EncodeToString(req.Stdin),
		TimeoutMS: int(req.Timeout.Milliseconds()), MaxOutput: req.MaxOutput,
	})
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, sandboxdBase+"/boxes/"+key+"/exec", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := b.hc.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("sandboxd: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readError(resp)
	}
	return readFrames(resp.Body, req.MaxOutput, onOut)
}

// maxFrameLine bounds one NDJSON line: frames carry at most 32 KiB of data.
const maxFrameLine = 1 << 20

// readFrames decodes the exec NDJSON stream into a result. Stdout and
// Stderr keep at most keep bytes each, whatever the server sends.
func readFrames(r io.Reader, keep int, onOut func(int, []byte)) (*core.ExecResult, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxFrameLine)
	res := &core.ExecResult{}
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var f proto.Frame
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return res, fmt.Errorf("sandboxd: bad exec frame: %w", err)
		}
		if f.Exit == nil && f.Error != "" {
			return res, fmt.Errorf("sandboxd: %s", f.Error)
		}
		if f.Exit != nil {
			res.Exit, res.Duration = *f.Exit, time.Duration(f.Ms)*time.Millisecond
			res.Truncated = res.Truncated || f.Truncated
			res.TimedOut, res.Killed = f.TimedOut, f.Killed
			if f.Error != "" {
				return res, fmt.Errorf("sandboxd: %s", f.Error)
			}
			return res, nil
		}
		if f.Stream != 1 && f.Stream != 2 {
			return res, fmt.Errorf("sandboxd: bad exec frame: stream %d", f.Stream)
		}
		data, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			return res, fmt.Errorf("sandboxd: bad exec frame: %w", err)
		}
		dst := &res.Stdout
		if f.Stream == 2 {
			dst = &res.Stderr
		}
		if room := keep - len(*dst); room < len(data) {
			res.Truncated = true
			*dst = append(*dst, data[:max(room, 0)]...)
		} else {
			*dst = append(*dst, data...)
		}
		if onOut != nil && len(data) > 0 {
			onOut(f.Stream, data)
		}
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("sandboxd: exec stream: %w", err)
	}
	return res, errors.New("sandboxd: exec stream ended without a final frame")
}

func (b *sandboxdBackend) stdio(ctx context.Context, key string, argv []string, env map[string]string) (io.ReadWriteCloser, error) {
	body, err := json.Marshal(proto.StdioReq{Argv: argv, Env: env})
	if err != nil {
		return nil, err
	}
	conn, err := b.dial(ctx, "", "")
	if err != nil {
		return nil, fmt.Errorf("sandboxd: %w", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	_ = conn.SetDeadline(deadline)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sandboxdBase+"/boxes/"+key+"/stdio", bytes.NewReader(body))
	if err != nil {
		conn.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "bothy-stdio/1")
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("sandboxd: %w", err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("sandboxd: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		err := readError(resp)
		resp.Body.Close()
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &stdioConn{Conn: conn, r: br}, nil
}

// stdioConn reads through the handshake's buffered reader so no early
// process output is lost.
type stdioConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *stdioConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (b *sandboxdBackend) kill(ctx context.Context, key string) error {
	var resp proto.KillResp
	err := b.call(ctx, http.MethodPost, "/boxes/"+key+"/kill", nil, &resp)
	if errors.Is(err, errGone) {
		return nil
	}
	return err
}

func (b *sandboxdBackend) destroy(ctx context.Context, key string) error {
	err := b.call(ctx, http.MethodDelete, "/boxes/"+key, nil, nil)
	if errors.Is(err, errGone) {
		return nil
	}
	return err
}

func (b *sandboxdBackend) list(ctx context.Context) ([]proto.BoxInfo, error) {
	var out []proto.BoxInfo
	err := b.call(ctx, http.MethodGet, "/boxes", nil, &out)
	return out, err
}

func (b *sandboxdBackend) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, sandboxdBase+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		return fmt.Errorf("sandboxd: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return readError(resp)
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("sandboxd: bad response to %s %s: %w", method, path, err)
	}
	return nil
}

// readError maps a proto.Error to the core sentinels.
func readError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e proto.Error
	if json.Unmarshal(raw, &e) != nil || e.Code == "" {
		e = proto.Error{Code: "engine", Message: strings.TrimSpace(string(raw))}
	}
	switch e.Code {
	case "bad_request":
		return fmt.Errorf("sandboxd: %w: %s", core.ErrInvalid, e.Message)
	case "not_found":
		return fmt.Errorf("sandboxd: %s: %w", e.Message, errGone)
	case "busy":
		return fmt.Errorf("sandboxd: %w: %s", core.ErrBusy, e.Message)
	case "unavailable":
		return fmt.Errorf("sandboxd: %w: %s", core.ErrNoSandbox, e.Message)
	default:
		return fmt.Errorf("sandboxd: %s (HTTP %d)", e.Message, resp.StatusCode)
	}
}
