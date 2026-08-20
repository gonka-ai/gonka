package docker

type createRequest struct {
	Image      string            `json:"Image"`
	Cmd        []string          `json:"Cmd,omitempty"`
	Env        []string          `json:"Env,omitempty"`
	User       string            `json:"User"`
	WorkingDir string            `json:"WorkingDir"`
	Labels     map[string]string `json:"Labels"`
	HostConfig hostConfig        `json:"HostConfig"`
}

type hostConfig struct {
	Binds          []string        `json:"Binds"`
	NetworkMode    string          `json:"NetworkMode"`
	ExtraHosts     []string        `json:"ExtraHosts,omitempty"`
	CapDrop        []string        `json:"CapDrop"`
	SecurityOpt    []string        `json:"SecurityOpt"`
	CgroupnsMode   string          `json:"CgroupnsMode"`
	IpcMode        string          `json:"IpcMode"`
	Init           bool            `json:"Init"`
	Memory         int64           `json:"Memory,omitempty"`
	NanoCPUs       int64           `json:"NanoCpus,omitempty"`
	PidsLimit      int64           `json:"PidsLimit,omitempty"`
	ShmSize        int64           `json:"ShmSize,omitempty"`
	RestartPolicy  restartPolicy   `json:"RestartPolicy"`
	DeviceRequests []deviceRequest `json:"DeviceRequests,omitempty"`
}

type restartPolicy struct {
	Name string `json:"Name"`
}

type deviceRequest struct {
	Driver       string     `json:"Driver"`
	Count        int        `json:"Count"`
	Capabilities [][]string `json:"Capabilities"`
}

type containerInspect struct {
	ID     string         `json:"Id"`
	Name   string         `json:"Name"`
	State  containerState `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

type containerState struct {
	Status   string `json:"Status"`
	Running  bool   `json:"Running"`
	ExitCode int    `json:"ExitCode"`
	Pid      int    `json:"Pid"`
}

type execRequest struct {
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
	User         string   `json:"User"`
	WorkingDir   string   `json:"WorkingDir"`
	Cmd          []string `json:"Cmd"`
}

type execStart struct {
	Detach bool `json:"Detach"`
	Tty    bool `json:"Tty"`
}
