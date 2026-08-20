package run

import (
	"fmt"

	"trainshard/internal/domain/shared/vo"
)

type Resources struct {
	GPUs      int
	DiskBytes int64
}

type RunSpec struct {
	Image     vo.ImageDigest
	Command   []string
	Env       map[string]string
	Sources   []vo.Source
	Resources Resources
}

func (r RunSpec) IsZero() bool { return r.Image.IsZero() }

func (r RunSpec) String() string {
	return fmt.Sprintf("RunSpec{image:%s command:%v env_keys:%d sources:%v gpus:%d disk:%d}",
		r.Image, r.Command, len(r.Env), r.Sources, r.Resources.GPUs, r.Resources.DiskBytes)
}

type Limits struct {
	MaxGPUs      int
	MaxDiskBytes int64
	MaxSources   int
}

func (l Limits) Allow(spec RunSpec) error {
	if spec.Resources.GPUs <= 0 || spec.Resources.GPUs > l.MaxGPUs {
		return ErrGPUsExceeded
	}
	if spec.Resources.DiskBytes <= 0 || spec.Resources.DiskBytes > l.MaxDiskBytes {
		return ErrDiskExceeded
	}
	if len(spec.Sources) > l.MaxSources {
		return ErrSourcesExceeded
	}
	return nil
}
