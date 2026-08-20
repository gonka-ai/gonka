package run

import "trainshard/internal/domain/shared/vo"

type Observed struct {
	Drained           bool
	ForeignGPUWork    bool
	GPUsInUse         int
	Images            []vo.ImageDigest
	Container         vo.ContainerState
	ContainerImage    vo.ImageDigest
	ExitCode          *int
	MeshKey           bool
	MeshUp            bool
	VolumesPresent    bool
	DiskUsedBytes     int64
	DiskQuotaBytes    int64
	TrainingProcesses bool
}

func (o Observed) HasImage(digest vo.ImageDigest) bool {
	for _, image := range o.Images {
		if image == digest {
			return true
		}
	}
	return false
}
