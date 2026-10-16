// Package e2e is the black-box suite: it builds the real binary, boots it
// against a temp data dir, mockllm and fake platform servers, and drives it
// over HTTP and SSE. Tests carry the e2e build tag (make e2e).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/testkit/conformance"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// Platform is a fake chat platform (an adapter's <name>test package) that the
// gateway reaches through the locked channels.<name>.api_base / gateway_url
// keys, which Env sets.
type Platform interface {
	conformance.PlatformFake
	Env() map[string]string
	Close()
}

// Options configure one gateway under test. The zero value boots a fresh
// gateway with no providers and no sandbox.
type Options struct {
	DataDir   string            // reuse a data dir (restart scenarios); default t.TempDir()
	Config    string            // written to <data>/bothy.yaml when set
	Env       map[string]string // extra environment; overrides the defaults
	MockLLM   []mockllm.Rule    // starts mockllm when non-nil; see MockProviderYAML
	Models    []core.ModelInfo  // models mockllm lists on /v1/models
	Platforms []Platform        // closed when the test ends
	PublicURL bool              // server.public_url is the gateway's own http://127.0.0.1:<port>
}

type Gateway struct {
	BaseURL string
	DataDir string
	LLM     *mockllm.Server // nil unless Options.MockLLM was set
	Boot    time.Duration   // exec to the first 200 from /healthz
	Client  *http.Client

	t      testing.TB
	cmd    *exec.Cmd
	logs   *syncBuffer
	done   chan struct{}
	err    error // process exit error, valid after done is closed
	mu     sync.Mutex
	csrf   string
	closed bool
}

var (
	buildOnce sync.Once
	binPath   string
	binErr    error
	binDir    string
)

// Binary returns the gateway binary: $BOTHY_E2E_BINARY when set (CI builds
// once), otherwise a CGO_ENABLED=0 build of ./cmd/bothy cached for the process.
func Binary(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		if p := os.Getenv("BOTHY_E2E_BINARY"); p != "" {
			binPath, binErr = filepath.Abs(p)
			return
		}
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Dir(filepath.Dir(file))
		if binDir, binErr = os.MkdirTemp("", "bothy-e2e-bin"); binErr != nil {
			return
		}
		binPath = filepath.Join(binDir, "bothy")
		if runtime.GOOS == "windows" {
			binPath += ".exe"
		}
		cmd := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-o", binPath, "./cmd/bothy")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatalf("e2e: %v", binErr)
	}
	return binPath
}

// Cleanup removes the cached build; call it from TestMain after m.Run.
func Cleanup() {
	if binDir != "" {
		_ = os.RemoveAll(binDir)
	}
}

// Start boots a gateway and waits until /healthz answers. It is stopped when
// the test ends; its logs are printed if the test failed.
func Start(t testing.TB, opts Options) *Gateway {
	t.Helper()
	bin := Binary(t)
	data := opts.DataDir
	if data == "" {
		data = t.TempDir()
	}
	run := filepath.Join(t.TempDir(), "run")
	if err := os.MkdirAll(run, 0o700); err != nil {
		t.Fatal(err)
	}
	var llm *mockllm.Server
	if opts.MockLLM != nil {
		llm = mockllm.New(opts.MockLLM, opts.Models)
		t.Cleanup(llm.Close)
	}
	for _, p := range opts.Platforms {
		t.Cleanup(p.Close)
	}
	if opts.Config != "" {
		if err := os.WriteFile(filepath.Join(data, "bothy.yaml"), []byte(opts.Config), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// A port picked here can be taken before the gateway binds it; retry then.
	var g *Gateway
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		g, err = boot(t, bin, data, run, llm, opts)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("e2e: gateway did not become healthy: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("gateway logs (%s):\n%s", g.DataDir, g.Logs())
		}
		_ = g.Stop()
	})
	return g
}

func boot(t testing.TB, bin, data, run string, llm *mockllm.Server, opts Options) (*Gateway, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	// A minimal environment: the developer's BOTHY_* settings and provider
	// keys must not leak into the gateway under test.
	env := map[string]string{
		"PATH":                    os.Getenv("PATH"),
		"HOME":                    data,
		"TMPDIR":                  os.TempDir(),
		"BOTHY_STORAGE__DATA_DIR": data,
		"BOTHY_STORAGE__RUN_DIR":  run,
		"BOTHY_SERVER__LISTEN":    addr,
		"BOTHY_SANDBOX__BACKEND":  "none",
		"BOTHY_LOG__LEVEL":        "debug",
	}
	if opts.PublicURL {
		env["BOTHY_SERVER__PUBLIC_URL"] = "http://" + addr
	}
	for _, p := range opts.Platforms {
		for k, v := range p.Env() {
			env[k] = v
		}
	}
	for k, v := range opts.Env {
		env[k] = v
	}
	cmd := exec.CommandContext(context.Background(), bin, "serve")
	cmd.Dir = data
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	logs := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	jar, _ := cookiejar.New(nil)
	g := &Gateway{
		BaseURL: "http://" + addr,
		DataDir: data,
		LLM:     llm,
		Client: &http.Client{Jar: jar, Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		t:    t,
		cmd:  cmd,
		logs: logs,
		done: make(chan struct{}),
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { g.err = cmd.Wait(); close(g.done) }()

	probe := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-g.done:
			return nil, fmt.Errorf("exited early (%w):\n%s", g.err, logs.String())
		default:
		}
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, g.BaseURL+"/healthz", nil)
		resp, err := probe.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				g.Boot = time.Since(start)
				return g, nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	<-g.done
	return nil, fmt.Errorf("no healthy /healthz within 10s:\n%s", logs.String())
}

func freePort() (int, error) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Stop sends SIGTERM and waits up to 15s (the gateway drains within 10s),
// then kills. It returns the process exit error (nil on a clean exit).
func (g *Gateway) Stop() error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		<-g.done
		return g.err
	}
	g.closed = true
	g.mu.Unlock()
	if runtime.GOOS == "windows" {
		_ = g.cmd.Process.Kill()
	} else {
		_ = g.cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-g.done:
	case <-time.After(15 * time.Second):
		_ = g.cmd.Process.Kill()
		<-g.done
		return errors.New("gateway ignored SIGTERM for 15s")
	}
	return g.err
}

// Kill stops the gateway with SIGKILL (crash and restart scenarios).
func (g *Gateway) Kill() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	_ = g.cmd.Process.Kill()
	<-g.done
}

func (g *Gateway) PID() int     { return g.cmd.Process.Pid }
func (g *Gateway) Logs() string { return g.logs.String() }

// RSS returns the gateway's resident set size in bytes.
func (g *Gateway) RSS() (int64, error) {
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "rss=", "-p", strconv.Itoa(g.PID())).Output()
	if err != nil {
		return 0, err
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return kb * 1024, err
}

// SetupToken reads the one-time setup token the gateway writes at first boot.
func (g *Gateway) SetupToken(t testing.TB) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(g.DataDir, "setup-token"))
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// SetCSRF sets the X-CSRF-Token sent on unsafe requests (from dto.Me.CSRF or
// dto.SetupClaimResp.CSRF).
func (g *Gateway) SetCSRF(token string) {
	g.mu.Lock()
	g.csrf = token
	g.mu.Unlock()
}

// Response is a fully read HTTP response.
type Response struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
}

// Do sends a request with the gateway's cookie jar. A body that is not
// []byte or io.Reader is sent as JSON. hdr adds headers (e.g. Authorization).
func (g *Gateway) Do(t testing.TB, method, path string, body any, hdr http.Header) *Response {
	t.Helper()
	var r io.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
	case io.Reader:
		r = b
	default:
		j, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		r, ctype = bytes.NewReader(j), "application/json"
	}
	req, err := http.NewRequestWithContext(context.Background(), method, g.BaseURL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	g.mu.Lock()
	csrf := g.csrf
	g.mu.Unlock()
	if csrf != "" && method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return &Response{StatusCode: resp.StatusCode, Status: resp.Status, Header: resp.Header, Body: data}
}

// JSON sends in as JSON and decodes a 2xx response into out (when non-nil).
// It returns the status code; non-2xx bodies are logged, not decoded.
func (g *Gateway) JSON(t testing.TB, method, path string, in, out any) int {
	t.Helper()
	resp := g.Do(t, method, path, in, nil)
	if resp.StatusCode/100 != 2 {
		t.Logf("%s %s: %s %s", method, path, resp.Status, resp.Body)
		return resp.StatusCode
	}
	if out != nil && len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			t.Fatalf("%s %s: decode %T: %v\n%s", method, path, out, err, resp.Body)
		}
	}
	return resp.StatusCode
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// MockProviderYAML is a bothy.yaml fragment that makes mockllm the only
// provider (openai_compat) and every model slot point at model.
func MockProviderYAML(baseURL, model string, window int) string {
	return fmt.Sprintf(`providers:
  - id: mock
    type: openai_compat
    base_url: %q
    allow_private: true
    models:
      - {id: %q, context: %d, max_output: 2048, tools: true}
models:
  main: [mock/%s]
  fast: [mock/%s]
`, baseURL, model, window, model, model)
}
