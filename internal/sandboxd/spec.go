package sandboxd

import (
	"github.com/bothyhq/bothy/internal/sandboxd/engine"
	"github.com/bothyhq/bothy/internal/sandboxd/proto"
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
	if in.Workspace != "" {
		mounts = append(mounts, engine.Mount{Type: "volume", Source: in.WorkVolume, Target: "/workspace",
			VolumeOptions: &engine.VolumeOptions{Subpath: "ws/" + in.Workspace, NoCopy: true}})
	}
	mounts = append(mounts, engine.Mount{Type: "volume", Source: in.RunVolume, Target: "/run/egress", ReadOnly: true,
		VolumeOptions: &engine.VolumeOptions{Subpath: proto.EgressSubpath(in.Key), NoCopy: true}})
	if !in.Canary {
		mounts = append(mounts, engine.Mount{Type: "volume", Source: in.WorkVolume, Target: "/skills", ReadOnly: true,
			VolumeOptions: &engine.VolumeOptions{Subpath: "skills", NoCopy: true}})
	}
	if in.MCPTree != "" {
		mounts = append(mounts, engine.Mount{Type: "volume", Source: in.WorkVolume, Target: "/opt/mcp", ReadOnly: !in.MCPTreeRW,
			VolumeOptions: &engine.VolumeOptions{Subpath: in.MCPTree, NoCopy: true}})
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
