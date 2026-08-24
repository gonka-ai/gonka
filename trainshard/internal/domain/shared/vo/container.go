package vo

type ContainerState string

const (
	ContainerUnknown ContainerState = ""
	ContainerAbsent  ContainerState = "absent"
	ContainerCreated ContainerState = "created"
	ContainerRunning ContainerState = "running"
	ContainerExited  ContainerState = "exited"
)

func (s ContainerState) Exists() bool { return s != "" && s != ContainerAbsent }

func (s ContainerState) Running() bool { return s == ContainerRunning }
