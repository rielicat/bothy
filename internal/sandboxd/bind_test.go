package sandboxd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rielicat/bothy/internal/sandboxd/engine"
	"github.com/rielicat/bothy/internal/sandboxd/enginetest"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

// newBindHarness runs sandboxd on an engine without volume subpaths (API
// 1.44). The volumes' "host" paths are directories of this machine, which
// also serve as sandboxd's own mounts of them; the fake engine resolves bind
// sources when a container starts, following symlinks as the kernel does.
func newBindHarness(t *testing.T, mut func(*Config, *enginetest.Fake)) (h *harness, work string) {
	t.Helper()
	work = shortTempDir(t)
	for _, d := range []string{"ws/" + testWS, "skills", testMCP} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h = newHarness(t, func(c *Config, e *enginetest.Fake) {
		e.ServerAPI = "1.44"
		c.WorkDir = work
		e.Volumes = map[string]engine.Volume{
			c.WorkVolume: {Name: c.WorkVolume, Driver: "local", Mountpoint: work},
			c.RunVolume:  {Name: c.RunVolume, Driver: "local", Mountpoint: c.RunDir},
		}
		if err := os.MkdirAll(filepath.Join(c.RunDir, proto.EgressSubpath(testKey)), 0o750); err != nil {
			t.Fatal(err)
		}
		if mut != nil {
			mut(c, e)
		}
	})
	return h, work
}

func bindMount(source, target string, ro bool) engine.Mount {
	return engine.Mount{Type: "bind", Source: source, Target: target, ReadOnly: ro,
		BindOptions: &engine.BindOptions{NonRecursive: true}}
}

func TestBindModeRunsBoxes(t *testing.T) {
	h, work := newBindHarness(t, nil)
	hl := h.d.healthSnapshot()
	if !hl.OK || hl.Mounts != proto.MountsBind || hl.Subpath || hl.API != "1.44" || hl.Runtime != "runc" {
		t.Fatalf("health = %+v", hl)
	}
	i := slices.IndexFunc(hl.SelfTest, func(c proto.Check) bool { return c.Name == proto.CheckBindMounts })
	if i < 0 || !hl.SelfTest[i].OK || !strings.Contains(hl.SelfTest[i].Detail, work) {
		t.Fatalf("bind_mounts check missing or failed: %+v", hl.SelfTest)
	}
	// The canary went through the same bind mounts and their check.
	canary := h.eng.Created()[0]
	assertMounts(t, canary.HostConfig.Mounts, []engine.Mount{
		bindMount(filepath.Join(h.conf.RunDir, proto.EgressSubpath(canaryKey)), "/run/egress", true)})

	h.create(testKey)
	got := h.eng.Created()[len(h.eng.Created())-1]
	assertMounts(t, got.HostConfig.Mounts, []engine.Mount{
		bindMount(filepath.Join(work, "ws", testWS), "/workspace", false),
		bindMount(filepath.Join(h.conf.RunDir, proto.EgressSubpath(testKey)), "/run/egress", true),
		bindMount(filepath.Join(work, "skills"), "/skills", true),
	})
	if out := h.exec(testKey, execBody([]string{"echo", "hi"}, 5000, "")); out.stdout != "hi\n" {
		t.Fatalf("exec = %+v", out)
	}
}

// The engine follows symlinks in a bind source. A directory that is a
// symlink out of the volume while the box starts, and the real directory
// again when sandboxd looks, must never leave a box behind.
func TestBindModeRefusesSwappedSources(t *testing.T) {
	for _, tc := range []struct {
		name, mcp string
		dir       func(work, run string) string
		target    string
	}{
		{"workspace", "", func(w, _ string) string { return filepath.Join(w, "ws", testWS) }, "/workspace"},
		{"egress", "", func(_, r string) string { return filepath.Join(r, proto.EgressSubpath(testKey)) }, "/run/egress"},
		{"skills", "", func(w, _ string) string { return filepath.Join(w, "skills") }, "/skills"},
		{"mcp tree", testMCP, func(w, _ string) string { return filepath.Join(w, testMCP) }, "/opt/mcp"},
	} {
		for _, swapBack := range []bool{true, false} {
			name := tc.name + " swapped back"
			if !swapBack {
				name = tc.name + " left a symlink"
			}
			t.Run(name, func(t *testing.T) {
				h, work := newBindHarness(t, nil)
				outside := shortTempDir(t)
				dir := tc.dir(work, h.conf.RunDir)
				real := dir + ".real"
				if err := os.Rename(dir, real); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, dir); err != nil {
					t.Fatal(err)
				}
				box := h.d.containerName(testKey)
				if swapBack {
					h.eng.OnStart = func(started string) {
						if started == box {
							_ = os.Remove(dir)
							_ = os.Rename(real, dir)
						}
					}
				}
				body := fmt.Sprintf(`{"key":%q,"profile":"base","workspace":%q,"mcp_tree":%q,"mcp_tree_rw":%t,`+
					`"limits":{"memory_mb":256,"cpus":1,"pids":64}}`, testKey, testWS, tc.mcp, tc.mcp != "")
				resp, b := h.do("POST", "/boxes", body)
				var e proto.Error
				if resp.StatusCode != http.StatusBadGateway || json.Unmarshal(b, &e) != nil || e.Code != "engine" ||
					!strings.Contains(e.Message, "bind mount check failed") || !strings.Contains(e.Message, tc.target) {
					t.Fatalf("create = %d %s", resp.StatusCode, b)
				}
				if slices.Contains(h.eng.Running(), box) || len(h.d.list()) != 0 || !slices.Contains(h.eng.Removed(), box) {
					t.Fatalf("box left behind: running %v, listed %v", h.eng.Running(), h.d.list())
				}
				if !swapBack {
					return
				}
				// The same request with nothing swapped works.
				h.eng.OnStart = nil
				if resp, b := h.do("POST", "/boxes", body); resp.StatusCode != http.StatusCreated {
					t.Fatalf("create after the swap = %d %s", resp.StatusCode, b)
				}
			})
		}
	}
}

func TestBindModeSetupFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		mut        func(*Config, *enginetest.Fake)
		wantReason string
	}{
		"volume driver": {func(c *Config, e *enginetest.Fake) {
			e.Volumes[c.WorkVolume] = engine.Volume{Name: c.WorkVolume, Driver: "nfs-plugin", Mountpoint: c.WorkDir}
		}, `uses the "nfs-plugin" driver`},
		"relative mount point": {func(c *Config, e *enginetest.Fake) {
			e.Volumes[c.RunVolume] = engine.Volume{Name: c.RunVolume, Driver: "local", Mountpoint: "volumes/run"}
		}, "unusable mount point"},
		"work volume not mounted into sandboxd": {func(c *Config, _ *enginetest.Fake) {
			c.WorkDir = filepath.Join(c.WorkDir, "not-mounted")
		}, "is not a mount point in sandboxd"},
		"runsc forced": {func(c *Config, e *enginetest.Fake) {
			c.Runtime = "runsc"
			e.EngineInfo.Runtimes["runsc"] = engine.Runtime{Path: "runsc"}
		}, "BOTHY_SBX_RUNTIME=runsc needs Docker 26+"},
		// sandboxd's run directory is not the run volume the engine binds
		// from: the canary's own bind check catches it.
		"run directory is another volume": {func(c *Config, e *enginetest.Fake) {
			other := shortTempDir(t)
			if err := os.MkdirAll(filepath.Join(other, proto.EgressSubpath(canaryKey)), 0o755); err != nil {
				t.Fatal(err)
			}
			e.Volumes[c.RunVolume] = engine.Volume{Name: c.RunVolume, Driver: "local", Mountpoint: other}
		}, "the canary box could not run: bind mount check: /run/egress is not the directory sandboxd sees"},
	} {
		t.Run(name, func(t *testing.T) {
			h, _ := newBindHarness(t, tc.mut)
			hl := h.d.healthSnapshot()
			if hl.OK || !strings.Contains(hl.Reason, tc.wantReason) || hl.Fix == "" {
				t.Fatalf("health = OK %v reason %q fix %q, want %q", hl.OK, hl.Reason, hl.Fix, tc.wantReason)
			}
			h.wantError("POST", "/boxes", createBody(testKey, testWS), http.StatusServiceUnavailable, "unavailable")
		})
	}
}

// On auto, a registered runsc is skipped without volume subpaths instead of
// failing its canary or raising the runsc_host_uds finding.
func TestBindModeUsesRuncOnAuto(t *testing.T) {
	h, _ := newBindHarness(t, func(c *Config, e *enginetest.Fake) {
		c.Runtime = "auto"
		e.EngineInfo.Runtimes["runsc"] = engine.Runtime{Path: "runsc"}
	})
	hl := h.d.healthSnapshot()
	if !hl.OK || hl.Runtime != "runc" {
		t.Fatalf("health = %+v", hl)
	}
	for _, c := range hl.SelfTest {
		if c.Name == proto.CheckRunsc {
			t.Fatalf("runsc check reported: %+v", c)
		}
		if c.Name == proto.CheckBindMounts && !strings.Contains(c.Detail, "runsc is registered but not used") {
			t.Fatalf("bind_mounts detail = %q", c.Detail)
		}
	}
	for _, c := range h.eng.Created() {
		if c.HostConfig.Runtime == "runsc" {
			t.Fatal("a box ran under runsc")
		}
	}
}
