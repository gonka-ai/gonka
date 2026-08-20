package readiness

type CheckName string

const (
	CheckDockerGPU      CheckName = "docker_gpu"
	CheckGPUsMatchChain CheckName = "gpus_match_chain"
	CheckFreeDisk       CheckName = "free_disk"
	CheckMeshPort       CheckName = "mesh_port"
	CheckVersion        CheckName = "version"
)

func Required() []CheckName {
	return []CheckName{CheckDockerGPU, CheckGPUsMatchChain, CheckFreeDisk, CheckMeshPort, CheckVersion}
}

type Check struct {
	Name   CheckName
	OK     bool
	Reason string
}

func Passed(name CheckName) Check { return Check{Name: name, OK: true} }

func Failed(name CheckName, reason string) Check { return Check{Name: name, Reason: reason} }
