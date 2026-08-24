package run

import (
	"fmt"
	"maps"
	"strconv"

	"trainshard/internal/domain/shared/vo"
)

// RendezvousPort is where rank 0 waits for the others. Fixed rather than configurable: it is
// reached over the mesh, where nothing but this run listens
const RendezvousPort = 29500

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

// WithEnv lays values over the spec's own, so a run cannot hand itself a placement the host
// did not give it
func (r RunSpec) WithEnv(over map[string]string) RunSpec {
	merged := make(map[string]string, len(r.Env)+len(over))
	maps.Copy(merged, r.Env)
	maps.Copy(merged, over)
	r.Env = merged
	return r
}

// PlacementEnv is what a training image needs to find the others. The names are node level on
// purpose: a launcher inside the container derives the per-process rank from them
func PlacementEnv(p vo.Placement) map[string]string {
	return map[string]string{
		"NODE_RANK":   strconv.Itoa(p.Rank),
		"NNODES":      strconv.Itoa(p.Size),
		"MASTER_ADDR": p.Master,
		"MASTER_PORT": strconv.Itoa(RendezvousPort),
	}
}

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
