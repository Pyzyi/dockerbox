package dockerapi

// Only the fields Dockerbox reads or writes are modelled. Field names follow
// the Docker Engine API.

// ContainerSummary is an entry of GET /containers/json.
type ContainerSummary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
}

// ContainerSpec is the body of POST /containers/create.
type ContainerSpec struct {
	Image            string            `json:"Image"`
	Hostname         string            `json:"Hostname,omitempty"`
	User             string            `json:"User,omitempty"`
	Env              []string          `json:"Env,omitempty"`
	Labels           map[string]string `json:"Labels,omitempty"`
	WorkingDir       string            `json:"WorkingDir,omitempty"`
	Tty              bool              `json:"Tty"`
	OpenStdin        bool              `json:"OpenStdin"`
	StopTimeout      *int              `json:"StopTimeout,omitempty"`
	HostConfig       HostConfig        `json:"HostConfig"`
	NetworkingConfig *NetworkingConfig `json:"NetworkingConfig,omitempty"`
}

// HostConfig is the security-relevant part of a container spec. Fields that
// Dockerbox must never set are still modelled so the policy can assert them.
type HostConfig struct {
	NetworkMode     string            `json:"NetworkMode"`
	Privileged      bool              `json:"Privileged"`
	CapAdd          []string          `json:"CapAdd"`
	CapDrop         []string          `json:"CapDrop"`
	SecurityOpt     []string          `json:"SecurityOpt"`
	Binds           []string          `json:"Binds,omitempty"`
	Mounts          []Mount           `json:"Mounts,omitempty"`
	Devices         []any             `json:"Devices,omitempty"`
	PortBindings    map[string]any    `json:"PortBindings,omitempty"`
	PublishAllPorts bool              `json:"PublishAllPorts"`
	PidMode         string            `json:"PidMode,omitempty"`
	IpcMode         string            `json:"IpcMode,omitempty"`
	UTSMode         string            `json:"UTSMode,omitempty"`
	UsernsMode      string            `json:"UsernsMode,omitempty"`
	CgroupnsMode    string            `json:"CgroupnsMode,omitempty"`
	Sysctls         map[string]string `json:"Sysctls,omitempty"`
	ExtraHosts      []string          `json:"ExtraHosts,omitempty"`
	Runtime         string            `json:"Runtime,omitempty"`
	Memory          int64             `json:"Memory"`
	MemorySwap      int64             `json:"MemorySwap"`
	NanoCpus        int64             `json:"NanoCpus"`
	PidsLimit       *int64            `json:"PidsLimit,omitempty"`
	ShmSize         int64             `json:"ShmSize,omitempty"`
	RestartPolicy   RestartPolicy     `json:"RestartPolicy"`
	LogConfig       *LogConfig        `json:"LogConfig,omitempty"`
	OomKillDisable  *bool             `json:"OomKillDisable,omitempty"`
	ReadonlyRootfs  bool              `json:"ReadonlyRootfs"`
	Init            *bool             `json:"Init,omitempty"`
}

// Mount describes a mount in HostConfig.Mounts.
type Mount struct {
	Type          string         `json:"Type"`
	Source        string         `json:"Source"`
	Target        string         `json:"Target"`
	ReadOnly      bool           `json:"ReadOnly,omitempty"`
	VolumeOptions *VolumeOptions `json:"VolumeOptions,omitempty"`
}

// VolumeOptions are applied when a named volume is created implicitly.
type VolumeOptions struct {
	Labels map[string]string `json:"Labels,omitempty"`
}

// RestartPolicy controls automatic restarts.
type RestartPolicy struct {
	Name string `json:"Name"`
}

// LogConfig configures the logging driver.
type LogConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config,omitempty"`
}

// NetworkingConfig attaches the container to networks on creation.
type NetworkingConfig struct {
	EndpointsConfig map[string]EndpointSettings `json:"EndpointsConfig"`
}

// EndpointSettings configures one network attachment.
type EndpointSettings struct {
	Aliases []string `json:"Aliases,omitempty"`
}

// NetworkSpec is the body of POST /networks/create.
type NetworkSpec struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Internal   bool              `json:"Internal"`
	Attachable bool              `json:"Attachable"`
	EnableIPv6 bool              `json:"EnableIPv6"`
	Options    map[string]string `json:"Options,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
}

// NetworkJSON is a network as returned by the engine.
type NetworkJSON struct {
	ID         string                     `json:"Id"`
	Name       string                     `json:"Name"`
	Internal   bool                       `json:"Internal"`
	Options    map[string]string          `json:"Options"`
	Labels     map[string]string          `json:"Labels"`
	Containers map[string]NetworkEndpoint `json:"Containers"`
}

// NetworkEndpoint is a container attached to a network.
type NetworkEndpoint struct {
	Name string `json:"Name"`
}

// VolumeJSON is a volume as returned by the engine.
type VolumeJSON struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

// ExecSpec is the body of POST /containers/{id}/exec.
type ExecSpec struct {
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
	Cmd          []string `json:"Cmd"`
	Env          []string `json:"Env,omitempty"`
	User         string   `json:"User,omitempty"`
	WorkingDir   string   `json:"WorkingDir,omitempty"`
	ConsoleSize  *[2]int  `json:"ConsoleSize,omitempty"`
}

// Stats is a subset of GET /containers/{id}/stats.
type Stats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}
