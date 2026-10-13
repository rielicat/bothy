package e2e

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// ComposeOptions configure a real compose deployment (deploy/compose.yaml
// plus e2e/docker/compose.test.yaml). Empty fields come from the
// environment that scripts/e2e-docker.sh sets.
type ComposeOptions struct {
	Prefix        string // project, volume and label prefix; $BOTHY_E2E_DOCKER_PREFIX or a random one
	Image         string // gateway image; $BOTHY_E2E_IMAGE
	SandboxImages string // "base=<image>[,node=<image>]"; $BOTHY_E2E_SANDBOX_IMAGES
	DockerGID     string // DOCKER_GID for compose; $DOCKER_GID or the socket's group
	MockLLM       []mockllm.Rule
	Models        []core.ModelInfo
}

// Compose is a running gateway + sandboxd pair driven from the host. HTTP
// drives the published port with Gateway's request helpers (its process
// methods do not apply).
type Compose struct {
	HTTP   *Gateway
	Prefix string
	LLM    *mockllm.Server
	// LLMURL is mockllm's base URL as the gateway container reaches it.
	LLMURL string
	// Healthy is how long `compose up` took until both healthchecks passed.
	Healthy time.Duration
	// SocketGID is the Docker socket's group as containers see it.
	SocketGID string

	t    testing.TB
	root string
	env  []string
}

var setupTokenRE = regexp.MustCompile(`/setup#t=([A-Z0-9]+)`)

// StartCompose brings the deployment up and waits for /healthz on the
// published loopback port. Everything it creates is removed when the test
// ends; the container logs are printed if the test failed.
func StartCompose(t testing.TB, opts ComposeOptions) *Compose {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found")
	}
	if opts.Image == "" {
		opts.Image = os.Getenv("BOTHY_E2E_IMAGE")
	}
	if opts.SandboxImages == "" {
		opts.SandboxImages = os.Getenv("BOTHY_E2E_SANDBOX_IMAGES")
	}
	if opts.Image == "" || opts.SandboxImages == "" {
		t.Skip("set BOTHY_E2E_IMAGE and BOTHY_E2E_SANDBOX_IMAGES (scripts/e2e-docker.sh builds both)")
	}
	if opts.Prefix == "" {
		opts.Prefix = os.Getenv("BOTHY_E2E_DOCKER_PREFIX")
	}
	if opts.Prefix == "" {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		opts.Prefix = "bothye2e" + hex.EncodeToString(b)
	}
	_, file, _, _ := runtime.Caller(0)
	c := &Compose{Prefix: opts.Prefix, t: t, root: filepath.Dir(filepath.Dir(file))}

	images, base, err := pinImages(opts.SandboxImages)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	sock := os.Getenv("DOCKER_SOCK")
	if sock == "" {
		sock = "/var/run/docker.sock"
	}
	// The group the engine's socket has inside containers: on Docker Desktop
	// that is the VM's root group, not the host file's group.
	out, err := docker("run", "--rm", "--network", "none", "--label", "bothy.test="+opts.Prefix,
		"-v", sock+":/var/run/docker.sock", "--entrypoint", "stat", base, "-c", "%g", "/var/run/docker.sock")
	if err != nil {
		t.Fatalf("compose: docker socket gid: %v", err)
	}
	c.SocketGID = strings.TrimSpace(out)
	gid := cmp.Or(opts.DockerGID, os.Getenv("DOCKER_GID"), c.SocketGID)
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	if opts.MockLLM != nil {
		c.LLM = mockllm.New(opts.MockLLM, opts.Models)
		t.Cleanup(c.LLM.Close)
		c.LLMURL = containerURL(t, c.LLM.URL())
	}
	c.env = append(os.Environ(),
		"BOTHY_E2E_PREFIX="+opts.Prefix, "BOTHY_E2E_IMAGE="+opts.Image, "BOTHY_E2E_PORT="+strconv.Itoa(port),
		"BOTHY_E2E_SBX_IMAGES="+images, "DOCKER_GID="+gid, "DOCKER_SOCK="+sock)

	t.Cleanup(c.down)
	// --wait holds out for both containers' healthchecks, sandboxd's included.
	upStart := time.Now()
	if out, err := c.compose("up", "-d", "--quiet-pull", "--wait", "--wait-timeout", "90"); err != nil {
		t.Fatalf("compose up: %v\n%s", err, out)
	}
	c.Healthy = time.Since(upStart)
	jar, _ := cookiejar.New(nil)
	c.HTTP = &Gateway{
		BaseURL: "http://127.0.0.1:" + strconv.Itoa(port),
		Client: &http.Client{Jar: jar, Timeout: 30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		t: t,
	}
	start := time.Now()
	probe := &http.Client{Timeout: time.Second}
	for {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, c.HTTP.BaseURL+"/healthz", nil)
		if resp, err := probe.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				c.HTTP.Boot = time.Since(start)
				break
			}
		}
		if time.Since(start) > 60*time.Second {
			t.Fatalf("compose: no healthy /healthz within 60s\n%s", c.Logs())
		}
		time.Sleep(50 * time.Millisecond)
	}
	return c
}

// pinImages turns "base=<tag>,node=<tag>" into the digest-pinned refs that
// BOTHY_SBX_IMAGES requires (local image ids for local builds) and returns
// the base profile's ref as well.
func pinImages(spec string) (pinned, base string, err error) {
	var out []string
	for item := range strings.SplitSeq(spec, ",") {
		profile, ref, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok {
			return "", "", fmt.Errorf("sandbox images %q: want profile=image", spec)
		}
		if !strings.Contains(ref, "sha256:") {
			id, err := docker("image", "inspect", "-f", "{{.Id}}", ref)
			if err != nil {
				return "", "", fmt.Errorf("sandbox image %s: %w", ref, err)
			}
			ref = strings.TrimSpace(id)
		}
		if profile == "base" || base == "" {
			base = ref
		}
		out = append(out, profile+"="+ref)
	}
	return strings.Join(out, ","), base, nil
}

// containerURL rewrites a loopback URL of the test process for the gateway
// container. Docker Desktop forwards host.docker.internal to the host's
// loopback; a Linux engine reaches the host on the bridge gateway, so a relay
// on all interfaces stands in for the loopback listener there.
func containerURL(t testing.TB, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port := u.Port()
	if runtime.GOOS == "linux" {
		port = strconv.Itoa(relay(t, u.Host))
	}
	u.Host = net.JoinHostPort("host.docker.internal", port)
	return u.String()
}

func relay(t testing.TB, target string) int {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() { l.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			in, err := l.Accept()
			if err != nil {
				return
			}
			out, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", target)
			if err != nil {
				in.Close()
				continue
			}
			wg.Add(2)
			go func() { defer wg.Done(); _, _ = io.Copy(out, in); out.Close() }()
			go func() { defer wg.Done(); _, _ = io.Copy(in, out); in.Close() }()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func docker(args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(context.Background(), "docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (c *Compose) compose(args ...string) (string, error) {
	full := append([]string{"compose", "-p", c.Prefix, "-f", filepath.Join(c.root, "deploy", "compose.yaml"),
		"-f", filepath.Join(c.root, "e2e", "docker", "compose.test.yaml")}, args...)
	cmd := exec.CommandContext(context.Background(), "docker", full...)
	cmd.Env = c.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Logs returns the output of both containers.
func (c *Compose) Logs() string {
	out, err := c.compose("logs", "--no-color", "--timestamps")
	if err != nil {
		return fmt.Sprintf("compose logs: %v\n%s", err, out)
	}
	return out
}

// Exec runs a command in a service container and returns its output.
func (c *Compose) Exec(service string, argv ...string) (string, error) {
	return c.compose(append([]string{"exec", "-T", service}, argv...)...)
}

// Boxes lists the tool box containers sandboxd started for this deployment.
func (c *Compose) Boxes() []string {
	out, err := docker("ps", "-aq", "--filter", "label=bothy.box=1", "--filter", "label=bothy.run_volume="+c.Prefix+"_run")
	if err != nil {
		c.t.Logf("compose: listing boxes: %v", err)
		return nil
	}
	return strings.Fields(out)
}

func (c *Compose) down() {
	if c.t.Failed() {
		c.t.Logf("compose logs (%s):\n%s", c.Prefix, c.Logs())
	}
	if out, err := c.compose("down", "--volumes", "--remove-orphans", "--timeout", "20"); err != nil {
		c.t.Logf("compose down: %v\n%s", err, out)
	}
	// sandboxd removes its boxes when it stops; a killed one leaves them,
	// and they pin the volumes.
	if left := c.Boxes(); len(left) > 0 {
		c.t.Errorf("compose: %d box container(s) outlived sandboxd", len(left))
		_, _ = docker(append([]string{"rm", "-f"}, left...)...)
	}
	for _, v := range []string{"data", "keys", "work", "run"} {
		name := c.Prefix + "_" + v
		if _, err := docker("volume", "inspect", name); err == nil {
			if _, err := docker("volume", "rm", "-f", name); err != nil {
				c.t.Logf("compose: %v", err)
			}
		}
	}
}

// SetupToken reads the one-time setup token from the gateway's boot banner.
func (c *Compose) SetupToken(t testing.TB) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		out, _ := c.compose("logs", "--no-color", "bothy")
		if m := setupTokenRE.FindStringSubmatch(out); m != nil {
			return m[1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no setup banner in the gateway log:\n%s", out)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// FirstRun claims the deployment with the setup token from the logs, creates
// the owner and makes mockllm (model id model) the only provider, as the
// setup wizard does.
func (c *Compose) FirstRun(t testing.TB, model string, window int) {
	t.Helper()
	c.SetupOwner(t)
	c.addMockProvider(t, model, window)
}

// SetupOwner claims the deployment with the setup token from the logs and
// creates the owner, leaving the session signed in.
func (c *Compose) SetupOwner(t testing.TB) {
	t.Helper()
	g := c.HTTP
	var claim dto.SetupClaimResp
	if code := g.JSON(t, http.MethodPost, "/api/setup/claim", dto.SetupClaimReq{Token: c.SetupToken(t)}, &claim); code != http.StatusOK || claim.CSRF == "" {
		t.Fatalf("claim = %d %+v", code, claim)
	}
	g.SetCSRF(claim.CSRF)
	var me dto.Me
	owner := dto.SetupOwnerReq{Username: "owner", Password: "Granite-River-42-Lantern", Timezone: "UTC"}
	if code := g.JSON(t, http.MethodPost, "/api/setup/owner", owner, &me); code != http.StatusOK || me.CSRF == "" {
		t.Fatalf("owner = %d %+v", code, me)
	}
	g.SetCSRF(me.CSRF)
}

func (c *Compose) addMockProvider(t testing.TB, model string, window int) {
	t.Helper()
	g := c.HTTP
	var probe core.ProbeResult
	test := dto.ProviderTestReq{Kind: core.KindOpenAICompat, BaseURL: c.LLMURL, Model: model, AllowPrivate: true}
	if code := g.JSON(t, http.MethodPost, "/api/providers/test", test, &probe); code != http.StatusOK || !probe.OK {
		t.Fatalf("provider test from the container against %s = %d %+v", c.LLMURL, code, probe)
	}
	provider, _ := json.Marshal(map[string]any{
		"id": "mock", "type": "openai_compat", "base_url": c.LLMURL, "allow_private": true,
		"models": []map[string]any{{"id": model, "context": window, "max_output": 2048, "tools": true}},
	})
	set := dto.ConfigPatchReq{Set: map[string]json.RawMessage{
		"providers.mock": provider,
		"models.main":    json.RawMessage(`["mock/` + model + `"]`),
		"models.fast":    json.RawMessage(`["mock/` + model + `"]`),
	}}
	if code := g.JSON(t, http.MethodPatch, "/api/config", set, nil); code != http.StatusOK {
		t.Fatalf("config patch = %d", code)
	}
	if code := g.JSON(t, http.MethodPost, "/api/setup/complete", nil, nil); code != http.StatusNoContent {
		t.Fatalf("setup complete = %d", code)
	}
}

// TokenOverhead is the fixed prompt overhead of one agent request (SPEC
// §11.3), in the gateway's own uncalibrated estimate (bytes/3.6).
type TokenOverhead struct {
	S1, Tools, System int // estimated tokens: constitution (+ standing rules), tool schemas, whole system prompt
	ToolNames         []string
}

// Fixed is S1 plus the core tool schemas, the budgeted quantity.
func (o TokenOverhead) Fixed() int { return o.S1 + o.Tools }

// MeasureTokenOverhead reads the first agent request mockllm recorded (the
// first one with a system prompt and tools) and splits its fixed part. constitution is
// the text GET /api/constitution serves; the system prompt must start with it.
func MeasureTokenOverhead(t testing.TB, requests [][]byte, constitution string) TokenOverhead {
	t.Helper()
	est := func(n int) int { return int(math.Ceil(float64(n) / 3.6)) }
	for _, body := range requests {
		var req struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("mockllm request: %v", err)
		}
		if len(req.Tools) == 0 {
			continue
		}
		var o TokenOverhead
		var system string
		for _, m := range req.Messages {
			if m.Role == "system" || m.Role == "developer" {
				var s string
				if json.Unmarshal(m.Content, &s) != nil {
					t.Fatalf("system content is not a string: %s", m.Content)
				}
				system += s
			}
		}
		if system == "" {
			continue // the provider probe's tool-calling check
		}
		if !strings.HasPrefix(system, strings.TrimRight(constitution, "\n")) {
			t.Fatalf("the system prompt does not start with the constitution:\n%.400s", system)
		}
		tools, _ := json.Marshal(req.Tools)
		o.S1, o.Tools, o.System = est(len(constitution)), est(len(tools)), est(len(system))
		for _, raw := range req.Tools {
			var tool struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			_ = json.Unmarshal(raw, &tool)
			o.ToolNames = append(o.ToolNames, tool.Function.Name)
		}
		return o
	}
	t.Fatal("mockllm recorded no agent request with tools")
	return TokenOverhead{}
}
