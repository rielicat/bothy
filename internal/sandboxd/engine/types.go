package engine

// Engine API shapes, limited to the fields sandboxd sends or reads. Field
// names follow the Docker Engine API, which is case-sensitive on input.

type Version struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
	Os            string `json:"Os"`
	Arch          string `json:"Arch"`
}

type Info struct {
	ServerVersion   string             `json:"ServerVersion"`
	SecurityOptions []string           `json:"SecurityOptions"`
	Runtimes        map[string]Runtime `json:"Runtimes"`
	DefaultRuntime  string             `json:"DefaultRuntime"`
	CgroupVersion   string             `json:"CgroupVersion"`
}

type Runtime struct {
	Path string `json:"path"`
}

type ContainerConfig struct {
	Image      string            `json:"Image"`
	User       string            `json:"User"`
	WorkingDir string            `json:"WorkingDir"`
	Cmd        []string          `json:"Cmd"`
	Env        []string          `json:"Env"`
	Labels     map[string]string `json:"Labels"`
	HostConfig *HostConfig       `json:"HostConfig"`
}

type HostConfig struct {
	NetworkMode    string            `json:"NetworkMode"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	CapDrop        []string          `json:"CapDrop"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	Init           *bool             `json:"Init"` // sent explicitly: a daemon-wide "init": true must not apply
	IpcMode        string            `json:"IpcMode"`
	PidsLimit      *int64            `json:"PidsLimit"`
	Memory         int64             `json:"Memory"`
	MemorySwap     int64             `json:"MemorySwap"`
	NanoCPUs       int64             `json:"NanoCpus"`
	Ulimits        []Ulimit          `json:"Ulimits"`
	Tmpfs          map[string]string `json:"Tmpfs"`
	Runtime        string            `json:"Runtime,omitempty"`
	LogConfig      LogConfig         `json:"LogConfig"`
	Mounts         []Mount           `json:"Mounts"`
	RestartPolicy  RestartPolicy     `json:"RestartPolicy"`
	AutoRemove     bool              `json:"AutoRemove"`
	Privileged     bool              `json:"Privileged"`
}

type Ulimit struct {
	Name string `json:"Name"`
	Soft int64  `json:"Soft"`
	Hard int64  `json:"Hard"`
}

type LogConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config"`
}

type Mount struct {
	Type          string         `json:"Type"`
	Source        string         `json:"Source"`
	Target        string         `json:"Target"`
	ReadOnly      bool           `json:"ReadOnly"`
	VolumeOptions *VolumeOptions `json:"VolumeOptions,omitempty"`
}

type VolumeOptions struct {
	NoCopy  bool   `json:"NoCopy"`
	Subpath string `json:"Subpath,omitempty"`
}

type RestartPolicy struct {
	Name string `json:"Name"`
}

type Container struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Labels  map[string]string `json:"Labels"`
	State   string            `json:"State"`
	Created int64             `json:"Created"`
}

type ContainerJSON struct {
	ID    string `json:"Id"`
	State struct {
		Running bool   `json:"Running"`
		Status  string `json:"Status"`
	} `json:"State"`
	HostConfig struct {
		Runtime string `json:"Runtime"`
	} `json:"HostConfig"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

type ExecConfig struct {
	User         string   `json:"User"`
	WorkingDir   string   `json:"WorkingDir,omitempty"`
	Env          []string `json:"Env"`
	Cmd          []string `json:"Cmd"`
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
}

type ExecInspect struct {
	Running  bool `json:"Running"`
	ExitCode int  `json:"ExitCode"`
	Pid      int  `json:"Pid"`
}
