//go:build e2e && docker

package e2e

// The Docker half of the sandbox suite: the real gateway and the real
// sandboxd, each in a Linux container as compose runs them, sharing the
// work and run volumes (and so sandboxd's socket and the per-box egress
// sockets), with tool boxes created by sandboxd on the host's engine. This
// works on Docker Desktop too, where the host cannot share unix sockets or
// named volumes with containers; the test drives the gateway over a
// published loopback port and reaches mockllm from the gateway container.
//
// Every container, volume, network and image is named
// <prefix>-<run>-…, labelled bothy.test=<prefix>, and removed when the test
// ends; boxes are found by sandboxd's bothy.run_volume label. A run killed
// before its cleanups (go test -timeout) leaves them behind: remove the
// bothy.test=<prefix> containers first (sandboxd last), then the boxes whose
// bothy.run_volume starts with <prefix>-, then volumes, networks and images.
// Env:
//
//	BOTHY_E2E_DOCKER_PREFIX  resource prefix (default bothy-e2e)
//	BOTHY_E2E_NET=0          skip the scenarios that need example.com:443

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

type dockerSuite struct {
	prefix  string
	run     string // random per suite, part of every name
	root    string // repository root
	box     string // box image id (BOTHY_SBX_IMAGES accepts a local id)
	image   string // gateway/sandboxd image tag
	sockGID string // group of /var/run/docker.sock inside containers
}

func dockerOut(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, errb.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func dockerT(t testing.TB, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	out, err := dockerOut(ctx, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// dockerQuiet runs a cleanup command; failures are logged, not fatal.
func dockerQuiet(t testing.TB, args ...string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := dockerOut(ctx, args...); err != nil {
		t.Logf("cleanup: %v", err)
	}
}

// newDockerSuite builds the box image from deploy/sandbox and a gateway
// image holding a linux build of ./cmd/bothy, both removed at cleanup.
func newDockerSuite(t *testing.T) *dockerSuite {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	arch, err := dockerOut(ctx, "version", "--format", "{{.Server.Arch}}")
	if err != nil {
		t.Skipf("no usable Docker engine: %v", err)
	}
	prefix := os.Getenv("BOTHY_E2E_DOCKER_PREFIX")
	if prefix == "" {
		prefix = "bothy-e2e"
	}
	rnd := make([]byte, 3)
	_, _ = rand.Read(rnd)
	_, file, _, _ := runtime.Caller(0)
	s := &dockerSuite{prefix: prefix, run: hex.EncodeToString(rnd), root: filepath.Dir(filepath.Dir(file))}
	label := "bothy.test=" + s.prefix
	// The docker-driver builder loads images straight into the engine; its
	// name is the current context's.
	builder := dockerT(t, "context", "show")

	dir := t.TempDir()
	bin := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-o", filepath.Join(dir, "bothy"), "./cmd/bothy")
	bin.Dir = s.root
	bin.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if out, err := bin.CombinedOutput(); err != nil {
		t.Fatalf("go build (linux/%s): %v\n%s", arch, err, out)
	}
	boxTag := s.name("box") + ":e2e"
	s.image = s.name("bothy") + ":e2e"
	t.Cleanup(func() { dockerQuiet(t, "image", "rm", "-f", boxTag, s.image) })

	dockerT(t, "buildx", "build", "--builder", builder, "--load", "--label", label,
		"-f", filepath.Join(s.root, "deploy/sandbox/Dockerfile"), "--target", "base", "-t", boxTag, s.root)
	s.box = dockerT(t, "image", "inspect", "--format", "{{.Id}}", boxTag)

	// The gateway image's directory layout is SPEC §2.3's: named volumes copy
	// owner and mode from the first container that mounts them.
	df := fmt.Sprintf(`FROM %s
RUN install -d -m 0700 -o 65532 -g 65532 /data /keys \
 && install -d -m 2770 -o 65532 -g 10001 /work \
 && install -d -m 0770 -o 65532 -g 65532 /run/bothy
COPY bothy /bothy
USER 65532:65532
ENTRYPOINT ["/bothy"]
`, s.debianBase(t))
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(df), 0o644); err != nil {
		t.Fatal(err)
	}
	dockerT(t, "buildx", "build", "--builder", builder, "--load", "--label", label, "-t", s.image, dir)
	s.sockGID = dockerT(t, "run", "--rm", "--label", label, "--user", "0:0", "-v", "/var/run/docker.sock:/var/run/docker.sock",
		"--entrypoint", "stat", s.image, "-c", "%g", "/var/run/docker.sock")
	return s
}

// debianBase is the digest-pinned debian the box image starts from.
func (s *dockerSuite) debianBase(t *testing.T) string {
	b, err := os.ReadFile(filepath.Join(s.root, "deploy/sandbox/Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^FROM (docker\.io/library/debian:\S+@sha256:[0-9a-f]{64}) AS base$`).FindSubmatch(b)
	if m == nil {
		t.Fatal("deploy/sandbox/Dockerfile: no digest-pinned debian base stage")
	}
	return string(m[1])
}

func (s *dockerSuite) name(part string) string { return s.prefix + "-" + s.run + "-" + part }

type dockerStack struct {
	*Gateway
	LLM *mockllm.Server
	s   *dockerSuite

	gw, sbx, net, runVol string
	volumes              []string
}

// start brings up mockllm, sandboxd and the gateway for one scenario,
// completes setup as the owner and waits until the sandbox is available.
func (s *dockerSuite) start(t *testing.T, scenario string, rules []mockllm.Rule, extraYAML string) *dockerStack {
	t.Helper()
	label := "bothy.test=" + s.prefix
	base := s.name(scenario)
	st := &dockerStack{s: s, gw: base + "-gateway", sbx: base + "-sandboxd", net: base + "-net", runVol: base + "-run"}
	work, data := base+"-work", base+"-data"
	st.volumes = []string{work, st.runVol, data}
	t.Cleanup(func() { st.teardown(t) })

	dockerT(t, "network", "create", "--label", label, st.net)
	for _, v := range st.volumes {
		dockerT(t, "volume", "create", "--label", label, v)
	}

	st.LLM = mockllm.New(rules, sbxModels())
	t.Cleanup(st.LLM.Close)
	cfgDir := t.TempDir()
	cfg := filepath.Join(cfgDir, "bothy.yaml")
	if err := os.WriteFile(cfg, []byte(MockProviderYAML(st.llmURL(t), sbxModel, 32768)+extraYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hardening := []string{"--label", label, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true"}
	args := append([]string{"run", "-d", "--name", st.sbx}, hardening...)
	args = append(args, "--network", "none", "--user", "65532:65532", "--group-add", s.sockGID, "--group-add", "65532",
		"--memory", "64m", "--pids-limit", "64",
		"-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", st.runVol+":/run/bothy",
		"-e", "BOTHY_SBX_WORK_VOLUME="+work, "-e", "BOTHY_SBX_RUN_VOLUME="+st.runVol,
		"-e", "BOTHY_SBX_IMAGES=base="+s.box, "-e", "BOTHY_SBX_RUNTIME=auto", "-e", "BOTHY_SBX_GATEWAY_UID=65532",
		"-e", "BOTHY_SBX_MAX_BOXES=8", s.image, "sandboxd")
	dockerT(t, args...)

	args = append([]string{"run", "-d", "--name", st.gw}, hardening...)
	args = append(args, "--network", st.net, "--user", "65532:65532", "--group-add", "10001",
		"--memory", "256m", "--pids-limit", "256", "--tmpfs", "/tmp:size=32m,mode=1777,nosuid,nodev,noexec",
		"-v", data+":/data", "-v", work+":/work", "-v", st.runVol+":/run/bothy", "-v", cfg+":/etc/bothy/bothy.yaml:ro",
		"-p", "127.0.0.1::8080", "--add-host", "host.docker.internal:host-gateway",
		"-e", "BOTHY_DEPLOYMENT=container", "-e", "BOTHY_SANDBOX__BACKEND=sandboxd", "-e", "BOTHY_CONFIG=/etc/bothy/bothy.yaml",
		"-e", "BOTHY_LOG__LEVEL=debug", "-e", "GOMEMLIMIT=48MiB", s.image, "serve")
	dockerT(t, args...)

	hostPort := dockerT(t, "port", st.gw, "8080/tcp")
	hostPort, _, _ = strings.Cut(hostPort, "\n")
	jar, _ := cookiejar.New(nil)
	st.Gateway = &Gateway{BaseURL: "http://" + hostPort, t: t, logs: &syncBuffer{}, done: make(chan struct{}),
		Client: &http.Client{Jar: jar, Timeout: 60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	st.waitHealthy(t)
	setupOwner(t, st.Gateway, dockerT(t, "exec", st.gw, "cat", "/data/setup-token"))
	st.waitSandbox(t)
	return st
}

// llmURL is mockllm's base URL as the gateway container sees it. Docker
// Desktop forwards host.docker.internal to the host's loopback; on Linux the
// engine runs here, so a forwarder listens on the stack network's gateway
// address.
func (st *dockerStack) llmURL(t *testing.T) string {
	u, err := url.Parse(st.LLM.URL())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" {
		return "http://host.docker.internal:" + u.Port() + "/v1"
	}
	gw := dockerT(t, "network", "inspect", "--format", "{{range .IPAM.Config}}{{.Gateway}}{{end}}", st.net)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort(gw, "0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				up, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", u.Host)
				if err != nil {
					return
				}
				defer up.Close()
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return "http://" + ln.Addr().String() + "/v1"
}

func (st *dockerStack) waitHealthy(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, st.BaseURL+"/healthz", nil)
		if resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("gateway container never became healthy:\n%s", st.containerLogs(st.gw))
}

// waitSandbox waits for sandboxd's first self-test and the gateway's next
// health probe.
func (st *dockerStack) waitSandbox(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	var info dto.SandboxInfo
	for time.Now().Before(deadline) {
		if st.JSON(t, http.MethodGet, "/api/sandbox", nil, &info) == http.StatusOK && info.Status.Available {
			t.Logf("sandbox: backend %s runtime %s rootless %v", info.Status.Backend, info.Status.Runtime, info.Status.Rootless)
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("sandbox never became available: %+v\nsandboxd logs:\n%s", info.Status, st.containerLogs(st.sbx))
}

func (st *dockerStack) containerLogs(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "logs", "--tail", "400", name)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func (st *dockerStack) Logs() string { return st.containerLogs(st.gw) }

func (st *dockerStack) boxes(t testing.TB) []string {
	out := dockerT(t, "ps", "-aq", "--filter", "label=bothy.box=1", "--filter", "label=bothy.run_volume="+st.runVol)
	return strings.Fields(out)
}

func (st *dockerStack) teardown(t *testing.T) {
	if t.Failed() {
		t.Logf("gateway logs:\n%s", st.containerLogs(st.gw))
		t.Logf("sandboxd logs:\n%s", st.containerLogs(st.sbx))
	}
	dockerQuiet(t, "rm", "-f", st.gw)
	dockerQuiet(t, "stop", "-t", "10", st.sbx)
	dockerQuiet(t, "rm", "-f", st.sbx)
	if ids := st.boxes(t); len(ids) > 0 {
		dockerQuiet(t, append([]string{"rm", "-f"}, ids...)...)
	}
	for _, v := range st.volumes {
		dockerQuiet(t, "volume", "rm", "-f", v)
	}
	dockerQuiet(t, "network", "rm", st.net)
}

// sessionBox returns the id of session s's box container, or "" if there is
// none.
func (st *dockerStack) sessionBox(t testing.TB, s core.SessionID) string {
	key := core.DerivedID(core.PrefixBox, "session:"+string(s))
	out := dockerT(t, "ps", "-q", "--filter", "label=bothy.run_volume="+st.runVol, "--filter", "label=bothy.key="+key)
	id, _, _ := strings.Cut(out, "\n")
	return id
}

// boxProcs lists the processes in box id other than its PID 1 (the bare
// sbx-init), as "pid args" lines; a box that is gone has none.
func (st *dockerStack) boxProcs(t testing.TB, id string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := dockerOut(ctx, "top", id, "-eo", "pid,args")
	if err != nil {
		if strings.Contains(err.Error(), "No such container") || strings.Contains(err.Error(), "is not running") {
			return nil
		}
		t.Fatal(err)
	}
	var procs []string
	for i, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 2 || (len(f) == 2 && f[1] == "/opt/bothy/sbx-init") {
			continue
		}
		procs = append(procs, strings.Join(f, " "))
	}
	return procs
}
