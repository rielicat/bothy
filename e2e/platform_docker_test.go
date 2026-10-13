//go:build e2e && docker

package e2e

// The docker variant runs the gateway the way compose deploys it: gateway
// and sandboxd in containers built from this checkout, sharing the work and
// run volumes, against the host's Docker engine. Every image, volume and
// container it creates is named and labelled (bothy.test=<prefix>) with
// BOTHY_E2E_DOCKER_PREFIX (default bothy-e2e) and removed afterwards, boxes
// included (they carry this run's run-volume label); BOTHY_E2E_KEEP_IMAGES=1
// keeps the images.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/core"
	"github.com/rielicat/bothy/internal/httpapi/dto"
	"github.com/rielicat/bothy/internal/testkit/mockllm"
)

// pltDebian is the base deploy/sandbox/test/acceptance.sh also uses.
const pltDebian = "docker.io/library/debian:trixie-20260918-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a"

func pltDockerOut(stdin io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "docker", args...)
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, errb.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func pltDockerCmd(t testing.TB, args ...string) string {
	t.Helper()
	out, err := pltDockerOut(nil, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// pltDocker is one deployment: three volumes, sandboxd and the gateway.
type pltDocker struct {
	prefix, label string
	box, harness  string // image id, image tag
	data, work    string
	run           string
	sbxd, gw      string
}

func pltNewDocker(t *testing.T) *pltDocker {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not installed")
	}
	if _, err := pltDockerOut(nil, "version"); err != nil {
		t.Skipf("docker engine unreachable: %v", err)
	}
	prefix := os.Getenv("BOTHY_E2E_DOCKER_PREFIX")
	if prefix == "" {
		prefix = "bothy-e2e"
	}
	id := prefix + "-" + pltRand(4)
	d := &pltDocker{prefix: prefix, label: "bothy.test=" + prefix, harness: prefix + "/harness:e2e",
		data: id + "-data", work: id + "-work", run: id + "-run", sbxd: id + "-sandboxd", gw: id + "-gateway"}
	t.Cleanup(func() { d.cleanup(t) })
	d.buildImages(t)
	for _, v := range []string{d.data, d.work, d.run} {
		pltDockerCmd(t, "volume", "create", "--label", d.label, v)
	}
	gid := pltDockerCmd(t, "run", "--rm", "--label", d.label, "-v", "/var/run/docker.sock:/var/run/docker.sock",
		"--entrypoint", "stat", d.harness, "-c", "%g", "/var/run/docker.sock")
	pltDockerCmd(t, "run", "-d", "--name", d.sbxd, "--label", d.label,
		"--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--user", "65532:65532", "--group-add", gid, "--group-add", "65532",
		"-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", d.run+":/run/bothy",
		"-e", "BOTHY_SBX_WORK_VOLUME="+d.work, "-e", "BOTHY_SBX_RUN_VOLUME="+d.run, "-e", "BOTHY_SBX_IMAGES=base="+d.box,
		"-e", "BOTHY_SBX_RUNTIME=auto", "-e", "BOTHY_SBX_GATEWAY_UID=65532",
		"--entrypoint", "/bothy", d.harness, "sandboxd")
	return d
}

func (d *pltDocker) buildImages(t *testing.T) {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	arch := pltDockerCmd(t, "version", "--format", "{{.Server.Arch}}")
	build := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-o", filepath.Join(out, "bothy"), "./cmd/bothy")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+arch)
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build linux/%s: %v\n%s", arch, err, b)
	}
	// The docker-driver builder of the current context loads straight into
	// the engine and never borrows another builder instance.
	builder := pltDockerCmd(t, "context", "show")
	boxTag := d.prefix + "/box:e2e"
	pltDockerCmd(t, "buildx", "build", "--builder", builder, "--load", "--label", d.label,
		"-f", filepath.Join(root, "deploy/sandbox/Dockerfile"), "--target", "base", "-t", boxTag, root)
	d.box = pltDockerCmd(t, "image", "inspect", "--format", "{{.Id}}", boxTag)
	dockerfile := "FROM " + pltDebian + "\n" +
		"RUN install -d -m 0700 -o 65532 -g 65532 /data && install -d -m 2770 -o 65532 -g 10001 /work " +
		"&& install -d -m 0770 -o 65532 -g 65532 /run/bothy\nCOPY bothy /bothy\n"
	if err := os.WriteFile(filepath.Join(out, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	pltDockerCmd(t, "buildx", "build", "--builder", builder, "--load", "--label", d.label, "-t", d.harness, out)
}

// pltHostListener listens where containers reach the host: on Linux the
// default bridge's gateway address, elsewhere (Docker Desktop, which
// forwards host.docker.internal to the host's loopback) 127.0.0.1.
func pltHostListener(t *testing.T) net.Listener {
	t.Helper()
	lc := net.ListenConfig{}
	if gw, err := pltDockerOut(nil, "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}"); err == nil && gw != "" {
		if ln, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort(gw, "0")); err == nil {
			return ln
		}
	}
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// pltForward relays a host listener to mockllm and returns the provider base
// URL the gateway container uses.
func pltForward(t *testing.T, llm *mockllm.Server) string {
	t.Helper()
	target, err := url.Parse(llm.URL())
	if err != nil {
		t.Fatal(err)
	}
	ln := pltHostListener(t)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				up, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", target.Host)
				if err != nil {
					return
				}
				defer up.Close()
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return fmt.Sprintf("http://host.docker.internal:%d/v1", ln.Addr().(*net.TCPAddr).Port)
}

// StartGateway writes bothy.yaml into the data volume and starts the
// gateway with the sandboxd backend; it returns once /healthz answers.
func (d *pltDocker) StartGateway(t *testing.T, cfg string) *Gateway {
	t.Helper()
	if _, err := pltDockerOut(strings.NewReader(cfg), "run", "--rm", "-i", "--label", d.label, "--user", "65532:65532",
		"-v", d.data+":/data", "--entrypoint", "sh", d.harness, "-c", "cat > /data/bothy.yaml"); err != nil {
		t.Fatal(err)
	}
	// Published-port traffic reaches the container from Docker's own
	// networks; only the test host can connect (127.0.0.1 publish).
	pltDockerCmd(t, "run", "-d", "--name", d.gw, "--label", d.label,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", "65532:65532", "--group-add", "10001",
		"-p", "127.0.0.1::8080", "--add-host", "host.docker.internal:host-gateway",
		"-v", d.data+":/data", "-v", d.work+":/work", "-v", d.run+":/run/bothy",
		"-e", "BOTHY_DEPLOYMENT=container", "-e", "BOTHY_STORAGE__DATA_DIR=/data", "-e", "BOTHY_SANDBOX__BACKEND=sandboxd",
		"-e", "BOTHY_LOG__LEVEL=debug", "-e", `BOTHY_SERVER__LOCAL_CIDRS=["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16"]`,
		"--entrypoint", "/bothy", d.harness, "serve")
	addr := pltDockerCmd(t, "port", d.gw, "8080/tcp")
	if i := strings.IndexByte(addr, '\n'); i >= 0 {
		addr = addr[:i]
	}
	g := &Gateway{BaseURL: "http://" + addr, t: t, logs: &syncBuffer{}, done: make(chan struct{}), closed: true}
	close(g.done) // there is no local process: Stop and Kill return at once
	deadline := time.Now().Add(60 * time.Second)
	for {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, g.BaseURL+"/healthz", nil)
		if resp, err := (&http.Client{Timeout: time.Second}).Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return g
			}
		}
		if time.Now().After(deadline) {
			logs, _ := pltDockerOut(nil, "logs", d.gw)
			t.Fatalf("gateway container not healthy within 60s:\n%s", logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (d *pltDocker) SetupToken(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		tok, err := pltDockerOut(nil, "exec", d.gw, "cat", "/data/setup-token")
		if err == nil && tok != "" {
			return tok
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup token: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// BoxKeys lists the box keys of this deployment's live boxes.
func (d *pltDocker) BoxKeys(t *testing.T) []string {
	t.Helper()
	out := pltDockerCmd(t, "ps", "-a", "--filter", "label=bothy.run_volume="+d.run, "--format", `{{.Label "bothy.key"}}`)
	return strings.Fields(out)
}

func (d *pltDocker) cleanup(t *testing.T) {
	if t.Failed() {
		for _, c := range []string{d.gw, d.sbxd} {
			if logs, err := pltDockerOut(nil, "logs", "--tail", "300", c); err == nil {
				t.Logf("--- %s logs:\n%s", c, logs)
			}
		}
	}
	_, _ = pltDockerOut(nil, "rm", "-f", d.gw)
	_, _ = pltDockerOut(nil, "stop", "-t", "10", d.sbxd)
	_, _ = pltDockerOut(nil, "rm", "-f", d.sbxd)
	if boxes, err := pltDockerOut(nil, "ps", "-aq", "--filter", "label=bothy.run_volume="+d.run); err == nil && boxes != "" {
		_, _ = pltDockerOut(nil, append([]string{"rm", "-f"}, strings.Fields(boxes)...)...)
	}
	_, _ = pltDockerOut(nil, "volume", "rm", "-f", d.data, d.work, d.run)
	if os.Getenv("BOTHY_E2E_KEEP_IMAGES") != "1" {
		_, _ = pltDockerOut(nil, "image", "rm", "-f", d.harness, d.prefix+"/box:e2e")
	}
}

// TestPrincipalIsolationSandbox is the docker half of E19: B's shell runs
// in B's own box, which mounts only B's workspace, so A's files are
// invisible to it while A's later boxes still see them; purging A destroys
// A's boxes and workspace and leaves B's alone.
func TestPrincipalIsolationSandbox(t *testing.T) {
	aSecret := "A-BOX-SECRET-" + pltRand(6)
	llm := pltLLM(t,
		mockllm.Rule{MatchUser: `E19D-`, MatchToolResult: `.`, Text: []string{"Done."}},
		mockllm.Rule{MatchUser: `E19D-A-WRITE`, ToolCalls: []core.ToolCall{pltCall("call_aw", "shell",
			`{"command":"mkdir -p notes && echo `+aSecret+` > notes/a.txt && cat notes/a.txt"}`)}},
		mockllm.Rule{MatchUser: `E19D-A-READ`, ToolCalls: []core.ToolCall{pltCall("call_ar", "shell", `{"command":"cat notes/a.txt"}`)}},
		mockllm.Rule{MatchUser: `E19D-B-PROBE`, ToolCalls: []core.ToolCall{pltCall("call_bp", "shell",
			`{"command":"cat notes/a.txt; ls -a /workspace; ls /work; find / -xdev -name a.txt 2>/dev/null; grep -rl `+aSecret+` /workspace /tmp /home 2>/dev/null; echo probe-done"}`)}},
	)
	d := pltNewDocker(t)
	g := d.StartGateway(t, pltYAML(pltForward(t, llm), nil, "mode: multi\n"))
	owner := pltClaim(t, g, d.SetupToken(t))
	deadline := time.Now().Add(90 * time.Second)
	for {
		var info dto.SandboxInfo
		if code := owner.JSON(t, http.MethodGet, "/api/sandbox", nil, &info); code == http.StatusOK && info.Status.Available {
			t.Logf("sandbox: %s runtime %s, %s", info.Status.Backend, info.Status.Runtime, info.Status.Engine)
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("sandbox never became available: %+v", info.Status)
		}
		time.Sleep(250 * time.Millisecond)
	}
	a := owner.CreateUser(t, "alice")
	b := owner.CreateUser(t, "bob")

	shell := func(u *pltUser, sid core.SessionID, text string) *core.ToolCallRecord {
		t.Helper()
		dt := u.WaitRun(t, u.Send(t, sid, text), 3*time.Minute, func(r *core.Run) bool { return r.Status.Terminal() })
		if dt.Run.Status != core.RunDone || len(dt.ToolCalls) != 1 || dt.ToolCalls[0].Tool != "shell" {
			t.Fatalf("%s: run %+v tool calls %+v", text, dt.Run, dt.ToolCalls)
		}
		return dt.ToolCalls[0]
	}
	sA := a.NewSession(t)
	if tc := shell(a, sA.ID, "E19D-A-WRITE save a note"); tc.Status != core.TCOK || !strings.Contains(tc.ResultPreview, aSecret) {
		t.Fatalf("A's write = %+v", tc)
	}
	sB := b.NewSession(t)
	tc := shell(b, sB.ID, "E19D-B-PROBE look around")
	if !strings.Contains(tc.ResultPreview, "probe-done") || strings.Contains(tc.ResultPreview, aSecret) ||
		!strings.Contains(tc.ResultPreview, "cannot access '/work'") {
		t.Fatalf("B's probe = %q, want no trace of A's workspace", tc.ResultPreview)
	}
	sA2 := a.NewSession(t)
	if tc := shell(a, sA2.ID, "E19D-A-READ the note"); tc.Status != core.TCOK || !strings.Contains(tc.ResultPreview, aSecret) {
		t.Fatalf("A's read from a new box = %+v, want the note (the probe above was meaningful)", tc)
	}

	boxA := []string{core.DerivedID(core.PrefixBox, "session:"+string(sA.ID)), core.DerivedID(core.PrefixBox, "session:"+string(sA2.ID))}
	boxB := core.DerivedID(core.PrefixBox, "session:"+string(sB.ID))
	keys := d.BoxKeys(t)
	if !slices.Contains(keys, boxA[0]) || !slices.Contains(keys, boxA[1]) || !slices.Contains(keys, boxB) {
		t.Fatalf("boxes before purge = %v, want A's %v and B's %s", keys, boxA, boxB)
	}
	wsA := string(a.Principal(t).Workspace)
	wsB := string(b.Principal(t).Workspace)
	if ls := pltDockerCmd(t, "exec", d.gw, "ls", "/work/ws"); !strings.Contains(ls, wsA) || !strings.Contains(ls, wsB) {
		t.Fatalf("/work/ws before purge = %q", ls)
	}

	if code := owner.JSON(t, http.MethodDelete, "/api/users/"+string(a.ID())+"?purge=true", nil, nil); code != http.StatusNoContent {
		t.Fatalf("purge = %d", code)
	}
	keys = d.BoxKeys(t)
	for _, k := range boxA {
		if slices.Contains(keys, k) {
			t.Errorf("A's box %s survived the purge (boxes %v)", k, keys)
		}
	}
	if !slices.Contains(keys, boxB) {
		t.Errorf("B's box %s was removed by A's purge (boxes %v)", boxB, keys)
	}
	ls := pltDockerCmd(t, "exec", d.gw, "ls", "/work/ws")
	if strings.Contains(ls, wsA) || !strings.Contains(ls, wsB) {
		t.Errorf("/work/ws after purge = %q, want A's %s gone and B's %s kept", ls, wsA, wsB)
	}
	if _, err := pltDockerOut(nil, "exec", d.gw, "ls", "/data/memory/"+string(a.ID())); err == nil {
		t.Errorf("A's memory directory survived the purge")
	}
}
