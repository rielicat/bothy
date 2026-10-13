package sandboxd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rielicat/bothy/internal/sandboxd/engine"
	"github.com/rielicat/bothy/internal/sandboxd/enginetest"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

var testImage = "sha256:" + strings.Repeat("a", 64)

type harness struct {
	t    *testing.T
	d    *daemon
	eng  *enginetest.Fake
	srv  *httptest.Server
	now  time.Time
	mu   sync.Mutex
	conf Config
}

func newHarness(t *testing.T, mut func(*Config, *enginetest.Fake)) *harness {
	t.Helper()
	runDir := shortTempDir(t)
	cfg, err := ParseConfig([]string{"BOTHY_SBX_IMAGES=base=" + testImage, "BOTHY_SBX_RUNTIME=runc",
		"BOTHY_SBX_SOCKET=" + runDir + "/sandboxd.sock", "BOTHY_SBX_MAX_BOXES=3"})
	if err != nil {
		t.Fatal(err)
	}
	eng := enginetest.New()
	eng.RunDir = runDir
	eng.AddImage(testImage)
	if mut != nil {
		mut(&cfg, eng)
	}
	h := &harness{t: t, eng: eng, now: time.Unix(1_800_000_000, 0), conf: cfg}
	h.d = newDaemon(cfg, eng, slog.New(slog.DiscardHandler), h.clock)
	h.srv = httptest.NewServer(h.d.routes())
	t.Cleanup(h.srv.Close)
	h.d.selfTest(context.Background())
	return h
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func (h *harness) do(method, path, body string) (*http.Response, []byte) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.srv.URL+"/v1"+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (h *harness) wantError(method, path, body string, status int, code string) {
	h.t.Helper()
	resp, b := h.do(method, path, body)
	var e proto.Error
	if resp.StatusCode != status || json.Unmarshal(b, &e) != nil || e.Code != code {
		h.t.Errorf("%s %s %s: got %d %s, want %d %s", method, path, body, resp.StatusCode, b, status, code)
	}
}

func createBody(key, ws string) string {
	return fmt.Sprintf(`{"key":%q,"profile":"base","workspace":%q,"mcp_tree":"","mcp_tree_rw":false,"limits":{"memory_mb":256,"cpus":1,"pids":64}}`, key, ws)
}

func (h *harness) create(key string) {
	h.t.Helper()
	resp, b := h.do("POST", "/boxes", createBody(key, testWS))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		h.t.Fatalf("create %s: %d %s", key, resp.StatusCode, b)
	}
}

type execOut struct {
	stdout, stderr string
	final          proto.Frame
}

func execBody(argv []string, timeoutMS int, extra string) string {
	a, _ := json.Marshal(argv)
	return fmt.Sprintf(`{"argv":%s,"cwd":"","env":{},"stdin_b64":"","timeout_ms":%d,"max_output":0%s}`, a, timeoutMS, extra)
}

func (h *harness) exec(key, body string) execOut {
	h.t.Helper()
	resp, b := h.do("POST", "/boxes/"+key+"/exec", body)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("exec: %d %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		h.t.Fatalf("content type %q", ct)
	}
	return parseFrames(h.t, b)
}

func parseFrames(t *testing.T, b []byte) execOut {
	t.Helper()
	var o execOut
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var f proto.Frame
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			t.Fatalf("frame %q: %v", sc.Text(), err)
		}
		if f.Exit != nil || f.Error != "" {
			o.final = f
			continue
		}
		d, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			t.Fatal(err)
		}
		if f.Stream == 1 {
			o.stdout += string(d)
		} else {
			o.stderr += string(d)
		}
	}
	return o
}

func TestConfig(t *testing.T) {
	c, err := ParseConfig([]string{"BOTHY_SBX_IMAGES=base=ghcr.io/rielicat/bothy-sandbox@" + testImage + ",node=" + testImage,
		"PATH=/bin", "BOTHY_SBX_IDLE=1m"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Engine != "unix:///var/run/docker.sock" || c.WorkVolume != "bothy_work" || c.RunVolume != "bothy_run" ||
		c.Runtime != "auto" || c.MaxBoxes != 8 || c.GatewayUID != 65532 || c.Idle != time.Minute ||
		c.MaxLifetime != 4*time.Hour || c.Socket != "/run/bothy/sandboxd.sock" || c.RunDir != "/run/bothy" || len(c.Images) != 2 {
		t.Fatalf("defaults: %+v", c)
	}
	if c.canaryProfile() != "base" {
		t.Fatal("canary profile")
	}
	for name, env := range map[string][]string{
		"no images":       {},
		"unpinned image":  {"BOTHY_SBX_IMAGES=base=ghcr.io/x/y:latest"},
		"tag and digest":  {"BOTHY_SBX_IMAGES=base=ghcr.io/x/y:1@sha256:abc"},
		"bad profile":     {"BOTHY_SBX_IMAGES=Base!=" + testImage},
		"duplicate":       {"BOTHY_SBX_IMAGES=base=" + testImage + ",base=" + testImage},
		"bad runtime":     {"BOTHY_SBX_IMAGES=base=" + testImage, "BOTHY_SBX_RUNTIME=kata"},
		"bad max boxes":   {"BOTHY_SBX_IMAGES=base=" + testImage, "BOTHY_SBX_MAX_BOXES=0"},
		"bad volume":      {"BOTHY_SBX_IMAGES=base=" + testImage, "BOTHY_SBX_WORK_VOLUME=../x"},
		"bad duration":    {"BOTHY_SBX_IMAGES=base=" + testImage, "BOTHY_SBX_IDLE=soon"},
		"relative socket": {"BOTHY_SBX_IMAGES=base=" + testImage, "BOTHY_SBX_SOCKET=sandboxd.sock"},
	} {
		if _, err := ParseConfig(env); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMainRefusesArguments(t *testing.T) {
	if err := Main(context.Background(), []string{"--debug"}, os.Environ); err == nil {
		t.Fatal("Main accepted arguments")
	}
}

func TestSelfTestHealthy(t *testing.T) {
	h := newHarness(t, nil)
	resp, b := h.do("GET", "/health", "")
	var hl proto.Health
	if err := json.Unmarshal(b, &hl); err != nil || resp.StatusCode != 200 {
		t.Fatalf("health: %d %s", resp.StatusCode, b)
	}
	if !hl.OK || hl.Runtime != "runc" || hl.API != "1.47" || !hl.Subpath || hl.Engine != "docker 27.4.0" || hl.MaxBoxes != 3 {
		t.Fatalf("health = %+v", hl)
	}
	var names []string
	for _, c := range hl.SelfTest {
		names = append(names, c.Name)
		if !c.OK {
			t.Errorf("check %s failed: %s", c.Name, c.Detail)
		}
	}
	for _, want := range append([]string{proto.CheckEngine, proto.CheckEngineAPI, proto.CheckImages, proto.CheckCanary, proto.CheckEgressRelay}, proto.IsolationChecks...) {
		if !slices.Contains(names, want) {
			t.Errorf("check %s missing from %v", want, names)
		}
	}
	// The canary box is gone again and never counted as a box.
	if len(h.eng.Running()) != 0 || hl.Boxes != 0 {
		t.Fatalf("running after self-test: %v (boxes %d)", h.eng.Running(), hl.Boxes)
	}
	if got := h.eng.Created()[0]; got.HostConfig.Runtime != "runc" || got.Labels["bothy.key"] != canaryKey {
		t.Fatalf("canary spec: runtime %q key %q", got.HostConfig.Runtime, got.Labels["bothy.key"])
	}
}

func TestSelfTestFailuresRefuseBoxes(t *testing.T) {
	for name, tc := range map[string]struct {
		mut        func(*Config, *enginetest.Fake)
		wantReason string
		wantCheck  string
	}{
		"isolation": {func(_ *Config, e *enginetest.Fake) { e.FailChecks = map[string][]string{"runc": {proto.CheckCaps}} },
			"box isolation self-test failed: caps", proto.CheckCaps},
		"old engine": {func(_ *Config, e *enginetest.Fake) { e.ServerAPI = "1.44" }, "Docker 26+ required", proto.CheckEngineAPI},
		"socket": {func(_ *Config, e *enginetest.Fake) {
			e.NegotiateErr = &net.OpError{Op: "dial", Net: "unix", Err: os.ErrPermission}
			e.SocketPath = os.TempDir()
		}, "cannot access", proto.CheckEngine},
		"bare image id missing": {func(c *Config, _ *enginetest.Fake) {
			c.Images = map[string]string{"base": "sha256:" + strings.Repeat("b", 64)}
		},
			"cannot be pulled", proto.CheckImages},
		"runsc missing": {func(c *Config, _ *enginetest.Fake) { c.Runtime = "runsc" }, "runsc is not registered", proto.CheckRunsc},
		"runsc relay fails": {func(c *Config, e *enginetest.Fake) {
			c.Runtime = "runsc"
			e.EngineInfo.Runtimes["runsc"] = engine.Runtime{Path: "runsc"}
			e.FailChecks = map[string][]string{"runsc": {proto.CheckEgressRelay}}
		}, "the runsc canary failed", proto.CheckRunsc},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, tc.mut)
			hl := h.d.healthSnapshot()
			if hl.OK || !strings.Contains(hl.Reason, tc.wantReason) {
				t.Fatalf("health = OK %v reason %q fix %q, want reason containing %q", hl.OK, hl.Reason, hl.Fix, tc.wantReason)
			}
			i := slices.IndexFunc(hl.SelfTest, func(c proto.Check) bool { return c.Name == tc.wantCheck })
			if i < 0 || hl.SelfTest[i].OK {
				t.Fatalf("check %s not reported failed: %+v", tc.wantCheck, hl.SelfTest)
			}
			h.wantError("POST", "/boxes", createBody(testKey, testWS), http.StatusServiceUnavailable, "unavailable")
		})
	}
}

func TestSocketOnboardingFixLines(t *testing.T) {
	f, err := os.CreateTemp("", "docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	defer os.Remove(f.Name())
	if err := os.Chmod(f.Name(), 0o660); err != nil {
		t.Fatal(err)
	}
	reason, fix := socketOnboarding(f.Name(), os.ErrPermission)
	fi, _ := os.Stat(f.Name())
	uid, gid, _ := fileOwner(fi)
	if !strings.HasPrefix(reason, "cannot access "+f.Name()+fmt.Sprintf(" (owner uid %d gid %d, mode 0660)", uid, gid)) {
		t.Fatalf("reason = %q", reason)
	}
	wantRootful := fmt.Sprintf("Set DOCKER_GID=%d in deploy/.env and run: docker compose up -d", gid)
	wantRootless := fmt.Sprintf("set SANDBOXD_USER=%d:%d and DOCKER_SOCK=$XDG_RUNTIME_DIR/docker.sock", uid, gid)
	if uid == 0 && fix != wantRootful || uid != 0 && !strings.HasPrefix(fix, wantRootless) {
		t.Fatalf("fix = %q", fix)
	}
	if !rootlessSocket("/run/user/1000/docker.sock", 1000, 0o600) || rootlessSocket("/var/run/docker.sock", 0, 0o660) {
		t.Fatal("rootless detection")
	}
	if reason, _ := socketOnboarding("/nonexistent/docker.sock", os.ErrNotExist); !strings.Contains(reason, "cannot find") {
		t.Fatalf("missing socket reason %q", reason)
	}
}

func TestSelfTestAutoFallsBackToRunc(t *testing.T) {
	h := newHarness(t, func(c *Config, e *enginetest.Fake) {
		c.Runtime = "auto"
		e.EngineInfo.Runtimes["runsc"] = engine.Runtime{Path: "runsc"}
		e.FailChecks = map[string][]string{"runsc": {proto.CheckEgressRelay}}
	})
	hl := h.d.healthSnapshot()
	if !hl.OK || hl.Runtime != "runc" {
		t.Fatalf("health = %+v", hl)
	}
	i := slices.IndexFunc(hl.SelfTest, func(c proto.Check) bool { return c.Name == proto.CheckRunsc })
	if i < 0 || hl.SelfTest[i].OK || !strings.Contains(hl.SelfTest[i].Detail, "--host-uds=open") {
		t.Fatalf("runsc fallback not reported: %+v", hl.SelfTest)
	}
	h.create(testKey)
	if rt := h.eng.Created()[len(h.eng.Created())-1].HostConfig.Runtime; rt != "runc" {
		t.Fatalf("box runtime %q after fallback", rt)
	}
}

func TestSelfTestPicksRunscAndBuiltinSeccomp(t *testing.T) {
	h := newHarness(t, func(c *Config, e *enginetest.Fake) {
		c.Runtime = "auto"
		e.EngineInfo.Runtimes["runsc"] = engine.Runtime{Path: "runsc"}
		e.EngineInfo.SecurityOptions = []string{"name=seccomp,profile=unconfined", "name=rootless"}
	})
	hl := h.d.healthSnapshot()
	if !hl.OK || hl.Runtime != "runsc" || !hl.Rootless {
		t.Fatalf("health = %+v", hl)
	}
	h.create(testKey)
	box := h.eng.Created()[len(h.eng.Created())-1]
	if box.HostConfig.Runtime != "runsc" || !slices.Contains(box.HostConfig.SecurityOpt, "seccomp=builtin") {
		t.Fatalf("box runtime %q secopt %q", box.HostConfig.Runtime, box.HostConfig.SecurityOpt)
	}
}

func TestSelfTestPullsPinnedImages(t *testing.T) {
	ref := "ghcr.io/rielicat/bothy-sandbox@" + testImage
	h := newHarness(t, func(c *Config, e *enginetest.Fake) { c.Images = map[string]string{"base": ref} })
	if !h.d.healthSnapshot().OK || !slices.Equal(h.eng.Pulled(), []string{ref}) {
		t.Fatalf("pulled %v, health %+v", h.eng.Pulled(), h.d.healthSnapshot())
	}
}

func TestRequestValidation(t *testing.T) {
	h := newHarness(t, nil)
	h.create(testKey)
	bad := func(method, path, body string) { h.wantError(method, path, body, http.StatusBadRequest, "bad_request") }
	// Unknown JSON fields and trailing data.
	bad("POST", "/boxes", `{"key":"`+testKey+`","profile":"base","image":"evil:latest"}`)
	bad("POST", "/boxes", createBody(testKey, testWS)+`{}`)
	bad("POST", "/boxes/"+testKey+"/exec", `{"argv":["echo"],"timeout_ms":1000,"privileged":true}`)
	bad("POST", "/boxes", `not json`)
	// Ids.
	bad("POST", "/boxes", createBody("box_../../etc", testWS))
	bad("POST", "/boxes", createBody(testKey, "ws_../../data"))
	bad("POST", "/boxes", `{"key":"`+testKey+`","profile":"base","mcp_tree":"mcp/../../keys"}`)
	bad("POST", "/boxes", `{"key":"`+testKey+`","profile":"base","mcp_tree_rw":true}`)
	bad("POST", "/boxes", `{"key":"`+testKey+`","profile":"gpu"}`)
	bad("POST", "/boxes/box_bad/exec", execBody([]string{"echo"}, 1000, ""))
	bad("DELETE", "/boxes/box_bad", "")
	// Exec fields.
	for _, cwd := range []string{"..", "../ws_other", "a/../../b", "/etc", "a//b"} {
		bad("POST", "/boxes/"+testKey+"/exec", fmt.Sprintf(`{"argv":["pwd"],"cwd":%q,"timeout_ms":1000}`, cwd))
	}
	for _, k := range []string{"PATH", "LD_PRELOAD", "HTTPS_PROXY", "BASH_ENV", "NODE_OPTIONS", "PYTHONPATH", "HOME", "lower"} {
		bad("POST", "/boxes/"+testKey+"/exec", fmt.Sprintf(`{"argv":["env"],"env":{%q:"x"},"timeout_ms":1000}`, k))
	}
	bad("POST", "/boxes/"+testKey+"/exec", execBody(nil, 1000, ""))
	bad("POST", "/boxes/"+testKey+"/exec", execBody([]string{"echo"}, 0, ""))
	bad("POST", "/boxes/"+testKey+"/exec", execBody([]string{"echo"}, int(time.Hour.Milliseconds()), ""))
	bad("POST", "/boxes/"+testKey+"/exec", `{"argv":["cat"],"stdin_b64":"!!!","timeout_ms":1000}`)
	bad("POST", "/boxes/"+testKey+"/exec", `{"argv":["cat"],"max_output":-1,"timeout_ms":1000}`)
}

func TestCreateIsIdempotentAndClamps(t *testing.T) {
	h := newHarness(t, nil)
	resp, b := h.do("POST", "/boxes", createBody(testKey, testWS))
	var cr proto.CreateResp
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(b, &cr) != nil || !cr.Created || cr.Runtime != "runc" {
		t.Fatalf("first create: %d %s", resp.StatusCode, b)
	}
	resp, b = h.do("POST", "/boxes", createBody(testKey, testWS))
	var again proto.CreateResp
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &again) != nil || again.Created || again.ID != cr.ID {
		t.Fatalf("second create: %d %s", resp.StatusCode, b)
	}
	if n := len(h.eng.Running()); n != 1 {
		t.Fatalf("%d containers running", n)
	}
	_, _ = h.do("POST", "/boxes", `{"key":"box_1123456789abcdefghjkmnpqrs","profile":"base","workspace":"","limits":{"memory_mb":99999,"cpus":64,"pids":1}}`)
	hc := h.eng.Created()[len(h.eng.Created())-1].HostConfig
	if hc.Memory != 2048<<20 || hc.NanoCPUs != 2e9 || *hc.PidsLimit != 16 {
		t.Fatalf("limits not clamped: mem %d cpu %d pids %d", hc.Memory, hc.NanoCPUs, *hc.PidsLimit)
	}
	// A box whose container died is recreated.
	h.eng.Vanish(containerName(testKey))
	resp, b = h.do("POST", "/boxes", createBody(testKey, testWS))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("recreate: %d %s", resp.StatusCode, b)
	}
}

func TestMaxBoxesEvictsLRUIdle(t *testing.T) {
	h := newHarness(t, nil) // max 3
	keys := []string{"box_0000000000000000000000000a", "box_0000000000000000000000000b", "box_0000000000000000000000000c"}
	for _, k := range keys {
		h.create(k)
		h.advance(time.Second)
	}
	h.create("box_0000000000000000000000000d")
	if slices.Contains(h.eng.Running(), containerName(keys[0])) || !slices.Contains(h.eng.Removed(), containerName(keys[0])) {
		t.Fatalf("LRU box not evicted: running %v", h.eng.Running())
	}
	// With every box busy, creation is refused.
	for _, k := range []string{keys[1], keys[2], "box_0000000000000000000000000d"} {
		b, err := h.d.acquire(k, false)
		if err != nil {
			t.Fatal(err)
		}
		defer h.d.release(b, false)
	}
	h.wantError("POST", "/boxes", createBody("box_0000000000000000000000000e", testWS), http.StatusServiceUnavailable, "busy")
}

func TestExecStreamsAndReports(t *testing.T) {
	h := newHarness(t, nil)
	h.create(testKey)
	o := h.exec(testKey, execBody([]string{"echo", "hello", "world"}, 5000, ""))
	if o.stdout != "hello world\n" || o.final.Exit == nil || *o.final.Exit != 0 || o.final.TimedOut || o.final.Killed {
		t.Fatalf("echo: %+v exit %v", o, o.final.Exit)
	}
	o = h.exec(testKey, execBody([]string{"warn", "careful"}, 5000, ""))
	if o.stderr != "careful\n" {
		t.Fatalf("stderr %q", o.stderr)
	}
	o = h.exec(testKey, execBody([]string{"exit", "7"}, 5000, ""))
	if *o.final.Exit != 7 {
		t.Fatalf("exit %d", *o.final.Exit)
	}
	stdin := base64.StdEncoding.EncodeToString([]byte("from stdin"))
	o = h.exec(testKey, fmt.Sprintf(`{"argv":["cat"],"stdin_b64":%q,"timeout_ms":5000}`, stdin))
	if o.stdout != "from stdin" {
		t.Fatalf("cat %q", o.stdout)
	}
	o = h.exec(testKey, `{"argv":["env"],"cwd":"src/app","env":{"FOO":"bar","A_B":"1"},"timeout_ms":5000}`)
	if o.stdout != "A_B=1\nFOO=bar\n" {
		t.Fatalf("env %q", o.stdout)
	}
	o = h.exec(testKey, `{"argv":["pwd"],"cwd":"src/app","timeout_ms":5000}`)
	if o.stdout != "/workspace/src/app\n" {
		t.Fatalf("pwd %q", o.stdout)
	}
	o = h.exec(testKey, execBody([]string{"flood", "100000"}, 5000, `,"max_output":1000`))
	if len(o.stdout) != 1000 || !o.final.Truncated {
		t.Fatalf("flood: kept %d truncated %v", len(o.stdout), o.final.Truncated)
	}
	o = h.exec(testKey, execBody([]string{"sleep"}, 200, ""))
	if !o.final.TimedOut || *o.final.Exit != 124 {
		t.Fatalf("timeout: %+v exit %d", o.final, *o.final.Exit)
	}
}

func TestExecWrapsInSbxInitRun(t *testing.T) {
	var got []string
	h := newHarness(t, func(_ *Config, e *enginetest.Fake) {
		e.Program = func(ctx context.Context, p *enginetest.Proc) int {
			got = p.Cfg.Cmd
			if p.Cfg.User != "10001:10001" {
				return 99
			}
			return 0
		}
	})
	h.create(testKey)
	o := h.exec(testKey, execBody([]string{"bash", "-lc", "ls"}, 1500, ""))
	want := []string{"/opt/bothy/sbx-init", "run", "--timeout", "1.5s", "--kill-after", "5s", "--", "bash", "-lc", "ls"}
	if *o.final.Exit != 0 || !slices.Equal(got, want) {
		t.Fatalf("cmd %q exit %d", got, *o.final.Exit)
	}
}

func TestExecOnVanishedBoxIs404(t *testing.T) {
	h := newHarness(t, nil)
	h.create(testKey)
	h.eng.Vanish(containerName(testKey))
	h.wantError("POST", "/boxes/"+testKey+"/exec", execBody([]string{"echo"}, 1000, ""), http.StatusNotFound, "not_found")
	if _, err := h.d.lookup(testKey); err == nil {
		t.Fatal("vanished box still registered")
	}
	h.wantError("POST", "/boxes/box_1123456789abcdefghjkmnpqrs/exec", execBody([]string{"echo"}, 1000, ""), http.StatusNotFound, "not_found")
}

func TestKill(t *testing.T) {
	h := newHarness(t, nil)
	h.create(testKey)
	done := make(chan execOut)
	go func() { done <- h.exec(testKey, execBody([]string{"sleep"}, 60_000, "")) }()
	waitFor(t, func() bool {
		b, _ := h.d.lookup(testKey)
		h.d.mu.Lock()
		defer h.d.mu.Unlock()
		return b != nil && b.active == 1
	})
	start := time.Now()
	resp, b := h.do("POST", "/boxes/"+testKey+"/kill", "")
	var kr proto.KillResp
	if resp.StatusCode != 200 || json.Unmarshal(b, &kr) != nil || kr.Recreated || kr.Killed != 1 {
		t.Fatalf("kill: %d %s", resp.StatusCode, b)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("kill took %v", took)
	}
	o := <-done
	if !o.final.Killed || *o.final.Exit != 137 {
		t.Fatalf("killed exec reported %+v", o.final)
	}
	if !slices.Contains(h.eng.Running(), containerName(testKey)) {
		t.Fatal("killall path removed the box")
	}

	// Processes that survive killall: the box is killed and removed.
	h.eng.Survivors = 1
	resp, b = h.do("POST", "/boxes/"+testKey+"/kill", "")
	if resp.StatusCode != 200 || json.Unmarshal(b, &kr) != nil || !kr.Recreated {
		t.Fatalf("fallback kill: %d %s", resp.StatusCode, b)
	}
	if slices.Contains(h.eng.Running(), containerName(testKey)) {
		t.Fatal("box survived the fallback")
	}
	h.wantError("POST", "/boxes/"+testKey+"/kill", "", http.StatusNotFound, "not_found")
}

func TestDeleteListAndReap(t *testing.T) {
	h := newHarness(t, nil)
	h.create(testKey)
	h.create("box_1123456789abcdefghjkmnpqrs")
	resp, b := h.do("GET", "/boxes", "")
	var infos []proto.BoxInfo
	if resp.StatusCode != 200 || json.Unmarshal(b, &infos) != nil || len(infos) != 2 || infos[0].Key != testKey ||
		infos[0].Runtime != "runc" || infos[0].Profile != "base" || infos[0].Created != h.clock().UnixMilli() {
		t.Fatalf("list: %s", b)
	}
	if resp, _ := h.do("DELETE", "/boxes/"+testKey, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp, _ := h.do("DELETE", "/boxes/"+testKey, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatal("delete of a missing box must be 204")
	}
	h.advance(h.conf.Idle + time.Second)
	h.d.reap(context.Background())
	if len(h.d.list()) != 0 || len(h.eng.Running()) != 0 {
		t.Fatalf("idle box not reaped: %v", h.eng.Running())
	}
	// Lifetime cap applies even to a busy box.
	h.create(testKey)
	b2, _ := h.d.acquire(testKey, false)
	h.advance(h.conf.MaxLifetime + time.Second)
	h.d.reap(context.Background())
	h.d.release(b2, false)
	if len(h.eng.Running()) != 0 {
		t.Fatal("box older than the lifetime cap survived")
	}
}

func TestOrphansRemovedAtStart(t *testing.T) {
	h := newHarness(t, func(_ *Config, e *enginetest.Fake) {
		e.AddContainer("bothy-box_old", map[string]string{"bothy.box": "1", "bothy.run_volume": "bothy_run"})
		e.AddContainer("other-install", map[string]string{"bothy.box": "1", "bothy.run_volume": "elsewhere"})
		e.AddContainer("unrelated", map[string]string{})
	})
	h.d.removeOrphans(context.Background())
	if got := h.eng.Running(); !slices.Equal(got, []string{"other-install", "unrelated"}) {
		t.Fatalf("running after orphan cleanup: %v", got)
	}
}

func TestStdio(t *testing.T) {
	h := newHarness(t, nil)
	h.create(testKey)
	conn, br := h.stdio(testKey, `{"argv":["cat"],"cwd":"","env":{}}`)
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"jsonrpc":"2.0","id":1}` + "\n")); err != nil {
		t.Fatal(err)
	}
	line, err := br.ReadString('\n')
	if err != nil || line != `{"jsonrpc":"2.0","id":1}`+"\n" {
		t.Fatalf("echoed %q, %v", line, err)
	}
	// One stdio stream per box.
	req, _ := http.NewRequest("POST", h.srv.URL+"/v1/boxes/"+testKey+"/stdio", strings.NewReader(`{"argv":["cat"]}`))
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "bothy-stdio/1")
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second stdio: %d", resp.StatusCode)
	}
	h.wantError("POST", "/boxes/"+testKey+"/stdio", `{"argv":["cat"]}`, http.StatusBadRequest, "bad_request") // no Upgrade
}

func TestStdioStderrRing(t *testing.T) {
	h := newHarness(t, func(_ *Config, e *enginetest.Fake) {
		e.Program = func(ctx context.Context, p *enginetest.Proc) int {
			fmt.Fprint(p.Stderr, "server starting\n")
			_, _ = io.Copy(p.Stdout, p.Stdin)
			return 0
		}
	})
	h.create(testKey)
	conn, _ := h.stdio(testKey, `{"argv":["mcp-server"]}`)
	waitFor(t, func() bool {
		_, b := h.do("GET", "/boxes/"+testKey+"/stderr", "")
		return string(b) == "server starting\n"
	})
	conn.Close()
}

func (h *harness) stdio(key, body string) (net.Conn, *bufio.Reader) {
	h.t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.srv.URL, "http://"))
	if err != nil {
		h.t.Fatal(err)
	}
	fmt.Fprintf(conn, "POST /v1/boxes/%s/stdio HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: bothy-stdio/1\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", key, len(body), body)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		h.t.Fatalf("stdio upgrade: %v %v", resp, err)
	}
	return conn, br
}

func TestRing(t *testing.T) {
	r := newRing()
	_, _ = r.Write([]byte("abc"))
	if string(r.Bytes()) != "abc" {
		t.Fatal(string(r.Bytes()))
	}
	big := bytes.Repeat([]byte("0123456789"), stderrRing/10+5)
	_, _ = r.Write(big)
	_, _ = r.Write([]byte("tail"))
	got := r.Bytes()
	if len(got) != stderrRing || !bytes.HasSuffix(got, []byte("tail")) || !bytes.Equal(got[:stderrRing-4], big[len(big)-(stderrRing-4):]) {
		t.Fatalf("ring kept %d bytes, suffix %q", len(got), got[len(got)-8:])
	}
}

func TestOutputCaps(t *testing.T) {
	c := outputCaps{max: 4}
	if k, err := c.take(1, []byte("ab")); string(k) != "ab" || err != nil {
		t.Fatal(string(k), err)
	}
	if k, err := c.take(1, []byte("cdef")); string(k) != "cd" || err != nil || !c.truncated {
		t.Fatal(string(k), err)
	}
	if k, _ := c.take(2, []byte("wxyz!")); string(k) != "wxyz" {
		t.Fatal("caps are per stream", string(k))
	}
	if _, err := c.take(1, make([]byte, drainLimit+1)); !errors.Is(err, errCut) {
		t.Fatalf("drain limit: %v", err)
	}
}

// shortTempDir keeps egress socket paths under the 104-byte sun_path limit
// of macOS, whose default temp dir is long.
func shortTempDir(t *testing.T) string {
	t.Helper()
	base := os.TempDir()
	if len(base) > 20 {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "sb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
