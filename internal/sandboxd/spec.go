package sandboxd

import (
	"path"

	"github.com/rielicat/bothy/internal/sandboxd/engine"
	"github.com/rielicat/bothy/internal/sandboxd/proto"
)

const (
	sbxInit     = "/opt/bothy/sbx-init"
	boxUser     = "10001:10001"
	labelBox    = "bothy.box"
	labelKey    = "bothy.key"
	labelProf   = "bothy.profile"
	labelVolume = "bothy.run_volume" // scopes orphan cleanup to this install's boxes
)

// boxEnv is the container environment of every box. Proxy-aware clients reach
// the network only through sbx-init's relay to the box's egress socket.
var boxEnv = []string{"HOME=/home/sbx", "LANG=C.UTF-8", "PATH=/usr/local/bin:/usr/bin:/bin",
	"HTTP_PROXY=http://127.0.0.1:3128", "HTTPS_PROXY=http://127.0.0.1:3128",
	"http_proxy=http://127.0.0.1:3128", "https_proxy=http://127.0.0.1:3128",
	"NO_PROXY=", "NODE_USE_ENV_PROXY=1", "PIP_DISABLE_PIP_VERSION_CHECK=1", "GIT_TERMINAL_PROMPT=0"}

// specInput is everything a box spec depends on. Only Key, Profile,
// Workspace, MCPTree, MCPTreeRW and Limits come from the gateway, already
// validated; the rest is sandboxd configuration and engine probing.
type specInput struct {
	Key, Profile, Workspace, MCPTree string
	MCPTreeRW                        bool
	Limits                           proto.Limits
	Image, Runtime                   string
	WorkVolume, RunVolume            string
	// SeccompBuiltin requests Docker's default profile explicitly, for engines
	// whose daemon-wide default is unconfined.
	SeccompBuiltin bool
	// Canary omits the skills mount: sandboxd cannot create directories on the
	// work volume, and a volume subpath must exist before the mount.
	Canary bool
	// Binds selects the bind-mount mode for engines without volume subpaths
	// (nil: subpath mounts).
	Binds *bindRoots
}

// bindRoots are the host paths of the work and run volumes as the engine
// reports them.
type bindRoots struct{ work, run string }

// boxMount is one mount of the spec: a subpath of the work or run volume,
// derived from validated ids only.
type boxMount struct {
	work     bool // work volume, else run volume
	sub      string
	target   string
	readOnly bool
}

func boxMounts(in specInput) []boxMount {
	var ms []boxMount
	if in.Workspace != "" {
		ms = append(ms, boxMount{work: true, sub: "ws/" + in.Workspace, target: "/workspace"})
	}
	ms = append(ms, boxMount{sub: proto.EgressSubpath(in.Key), target: "/run/egress", readOnly: true})
	if !in.Canary {
		ms = append(ms, boxMount{work: true, sub: "skills", target: "/skills", readOnly: true})
	}
	if in.MCPTree != "" {
		ms = append(ms, boxMount{work: true, sub: in.MCPTree, target: "/opt/mcp", readOnly: !in.MCPTreeRW})
	}
	return ms
}

// engineMount is m as a volume subpath mount or, in the bind-mount mode, as a
// non-recursive bind of the same directory under the volume's host path.
// The engine follows symlinks in a bind source, so such a box is used only
// after verifyBinds. Propagation stays the engine's default: Docker refuses
// rprivate for sources inside its data root and uses rslave there.
func (in specInput) engineMount(m boxMount) engine.Mount {
	if in.Binds == nil {
		vol := in.RunVolume
		if m.work {
			vol = in.WorkVolume
		}
		return engine.Mount{Type: "volume", Source: vol, Target: m.target, ReadOnly: m.readOnly,
			VolumeOptions: &engine.VolumeOptions{Subpath: m.sub, NoCopy: true}}
	}
	root := in.Binds.run
	if m.work {
		root = in.Binds.work
	}
	return engine.Mount{Type: "bind", Source: path.Join(root, m.sub), Target: m.target, ReadOnly: m.readOnly,
		BindOptions: &engine.BindOptions{NonRecursive: true}}
}

// boxSpec is the compiled-in container spec of SPEC §2.6.
func boxSpec(in specInput) *engine.ContainerConfig {
	lim := in.Limits.Clamp()
	memBytes := int64(lim.MemoryMB) << 20
	pids := int64(lim.Pids)
	initFalse := false
	secOpt := []string{"no-new-privileges:true"}
	if in.SeccompBuiltin {
		secOpt = append(secOpt, "seccomp=builtin")
	}
	var mounts []engine.Mount
	for _, m := range boxMounts(in) {
		mounts = append(mounts, in.engineMount(m))
	}
	return &engine.ContainerConfig{
		Image:      in.Image,
		User:       boxUser,
		WorkingDir: "/workspace",
		Cmd:        []string{sbxInit},
		Env:        append([]string(nil), boxEnv...),
		Labels: map[string]string{labelBox: "1", labelKey: in.Key, labelProf: in.Profile,
			labelVolume: in.RunVolume},
		HostConfig: &engine.HostConfig{
			NetworkMode:    "none",
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    secOpt,
			Init:           &initFalse,
			IpcMode:        "private",
			PidsLimit:      &pids,
			Memory:         memBytes,
			MemorySwap:     memBytes,
			NanoCPUs:       int64(lim.CPUs * 1e9),
			Ulimits:        []engine.Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}, {Name: "core", Soft: 0, Hard: 0}},
			Tmpfs: map[string]string{
				"/tmp":      "rw,nosuid,nodev,size=256m",
				"/home/sbx": "rw,nosuid,nodev,size=64m,uid=10001,gid=10001,mode=0700",
			},
			Runtime:       in.Runtime, // engine runtime name; "" only when runc is the engine default under another name
			LogConfig:     engine.LogConfig{Type: "none", Config: map[string]string{}},
			Mounts:        mounts,
			RestartPolicy: engine.RestartPolicy{Name: "no"},
			AutoRemove:    true,
		},
	}
}
