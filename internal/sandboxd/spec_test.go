package sandboxd

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bothyhq/bothy/internal/sandboxd/engine"
	"github.com/bothyhq/bothy/internal/sandboxd/proto"
)

const (
	testKey = "box_0123456789abcdefghjkmnpqrs"
	testWS  = "ws_0123456789abcdefghjkmnpqrs"
	testMCP = "mcp/docs/in_0123456789abcdefghjkmnpqrs"
)

func testSpec(mut func(*specInput)) *engine.ContainerConfig {
	in := specInput{Key: testKey, Profile: "base", Workspace: testWS, Limits: proto.Limits{MemoryMB: 512, CPUs: 1, Pids: 256},
		Image: "img@sha256:" + strings.Repeat("a", 64), Runtime: "runc", WorkVolume: "bothy_work", RunVolume: "bothy_run"}
	if mut != nil {
		mut(&in)
	}
	return boxSpec(in)
}

// TestSpecHardening asserts every hardening field of SPEC §2.6.
func TestSpecHardening(t *testing.T) {
	c := testSpec(nil)
	hc := c.HostConfig
	if c.User != "10001:10001" || c.WorkingDir != "/workspace" || !slices.Equal(c.Cmd, []string{"/opt/bothy/sbx-init"}) {
		t.Errorf("user/workdir/cmd = %q %q %q", c.User, c.WorkingDir, c.Cmd)
	}
	wantEnv := []string{"HOME=/home/sbx", "LANG=C.UTF-8", "PATH=/usr/local/bin:/usr/bin:/bin",
		"HTTP_PROXY=http://127.0.0.1:3128", "HTTPS_PROXY=http://127.0.0.1:3128",
		"http_proxy=http://127.0.0.1:3128", "https_proxy=http://127.0.0.1:3128",
		"NO_PROXY=", "NODE_USE_ENV_PROXY=1", "PIP_DISABLE_PIP_VERSION_CHECK=1", "GIT_TERMINAL_PROMPT=0"}
	if !slices.Equal(c.Env, wantEnv) {
		t.Errorf("env = %q", c.Env)
	}
	if c.Labels["bothy.box"] != "1" || c.Labels["bothy.key"] != testKey || c.Labels["bothy.profile"] != "base" ||
		c.Labels["bothy.run_volume"] != "bothy_run" {
		t.Errorf("labels = %v", c.Labels)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"NetworkMode none", hc.NetworkMode == "none"},
		{"ReadonlyRootfs", hc.ReadonlyRootfs},
		{"CapDrop ALL", slices.Equal(hc.CapDrop, []string{"ALL"})},
		{"no-new-privileges", slices.Equal(hc.SecurityOpt, []string{"no-new-privileges:true"})},
		{"Init explicitly false", hc.Init != nil && !*hc.Init},
		{"IpcMode private", hc.IpcMode == "private"},
		{"PidsLimit", hc.PidsLimit != nil && *hc.PidsLimit == 256},
		{"Memory", hc.Memory == 512<<20},
		{"MemorySwap == Memory", hc.MemorySwap == hc.Memory},
		{"NanoCPUs", hc.NanoCPUs == 1e9},
		{"Ulimits", slices.Equal(hc.Ulimits, []engine.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}, {Name: "core", Soft: 0, Hard: 0}})},
		{"tmpfs /tmp", hc.Tmpfs["/tmp"] == "rw,nosuid,nodev,size=256m"},
		{"tmpfs /home/sbx", hc.Tmpfs["/home/sbx"] == "rw,nosuid,nodev,size=64m,uid=10001,gid=10001,mode=0700"},
		{"only two tmpfs", len(hc.Tmpfs) == 2},
		{"Runtime", hc.Runtime == "runc"},
		{"LogConfig none", hc.LogConfig.Type == "none"},
		{"RestartPolicy no", hc.RestartPolicy.Name == "no"},
		{"AutoRemove", hc.AutoRemove},
		{"not Privileged", !hc.Privileged},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s: violated", c.name)
		}
	}
	want := []engine.Mount{
		{Type: "volume", Source: "bothy_work", Target: "/workspace", VolumeOptions: &engine.VolumeOptions{Subpath: "ws/" + testWS, NoCopy: true}},
		{Type: "volume", Source: "bothy_run", Target: "/run/egress", ReadOnly: true, VolumeOptions: &engine.VolumeOptions{Subpath: proto.EgressSubpath(testKey), NoCopy: true}},
		{Type: "volume", Source: "bothy_work", Target: "/skills", ReadOnly: true, VolumeOptions: &engine.VolumeOptions{Subpath: "skills", NoCopy: true}},
	}
	assertMounts(t, hc.Mounts, want)

	// Init must reach the engine as an explicit false, not be omitted.
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, frag := range []string{`"Init":false`, `"NanoCpus":1000000000`, `"ReadonlyRootfs":true`, `"NetworkMode":"none"`} {
		if !strings.Contains(string(raw), frag) {
			t.Errorf("wire JSON lacks %s", frag)
		}
	}
}

func assertMounts(t *testing.T, got, want []engine.Mount) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("mounts:\n got  %s\n want %s", g, w)
	}
}

func TestSpecMountVariants(t *testing.T) {
	noWS := testSpec(func(in *specInput) { in.Workspace = "" })
	for _, m := range noWS.HostConfig.Mounts {
		if m.Target == "/workspace" {
			t.Error("workspace mounted although none was requested")
		}
	}
	ro := testSpec(func(in *specInput) { in.MCPTree = testMCP })
	rw := testSpec(func(in *specInput) { in.MCPTree, in.MCPTreeRW = testMCP, true })
	for _, tc := range []struct {
		c        *engine.ContainerConfig
		readOnly bool
	}{{ro, true}, {rw, false}} {
		i := slices.IndexFunc(tc.c.HostConfig.Mounts, func(m engine.Mount) bool { return m.Target == "/opt/mcp" })
		if i < 0 {
			t.Fatal("no /opt/mcp mount")
		}
		m := tc.c.HostConfig.Mounts[i]
		if m.ReadOnly != tc.readOnly || m.VolumeOptions.Subpath != testMCP || m.Source != "bothy_work" {
			t.Errorf("mcp mount = %+v", m)
		}
	}
	canary := testSpec(func(in *specInput) { in.Canary, in.Workspace = true, "" })
	if slices.ContainsFunc(canary.HostConfig.Mounts, func(m engine.Mount) bool { return m.Target == "/skills" }) {
		t.Error("canary mounts /skills")
	}
	builtin := testSpec(func(in *specInput) { in.SeccompBuiltin = true })
	if !slices.Equal(builtin.HostConfig.SecurityOpt, []string{"no-new-privileges:true", "seccomp=builtin"}) {
		t.Errorf("SecurityOpt = %q", builtin.HostConfig.SecurityOpt)
	}
	runsc := testSpec(func(in *specInput) { in.Runtime = "runsc" })
	if runsc.HostConfig.Runtime != "runsc" {
		t.Error("runtime not passed through")
	}
}

func TestSpecClampsLimits(t *testing.T) {
	for _, tc := range []struct {
		in        proto.Limits
		mem, nano int64
		pids      int64
	}{
		{proto.Limits{MemoryMB: 1, CPUs: 0, Pids: 1}, 64 << 20, 1e8, 16},
		{proto.Limits{MemoryMB: 1 << 30, CPUs: 99, Pids: 1 << 30}, 2048 << 20, 2e9, 512},
	} {
		hc := testSpec(func(in *specInput) { in.Limits = tc.in }).HostConfig
		if hc.Memory != tc.mem || hc.MemorySwap != tc.mem || hc.NanoCPUs != tc.nano || *hc.PidsLimit != tc.pids {
			t.Errorf("limits %+v -> mem %d swap %d nano %d pids %d", tc.in, hc.Memory, hc.MemorySwap, hc.NanoCPUs, *hc.PidsLimit)
		}
	}
}
