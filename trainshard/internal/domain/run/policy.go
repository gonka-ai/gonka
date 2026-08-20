package run

import (
	"time"

	"trainshard/internal/domain/shared/vo"
)

// Autokick returns why a reserved node has to be handed back, or false while it still has
// time; a node that never gets ready and one that stays broken both cost the run a slot
func Autokick(d Desired, o Observed, state RunState, now time.Time, patience time.Duration) (vo.ReleaseReason, bool) {
	if !d.Reserved || state.ReservedAt.IsZero() {
		return "", false
	}
	if !Prepared(d, o) {
		return vo.ReleaseFailedPrepare, now.Sub(state.ReservedAt) >= patience
	}
	if state.Fault == nil {
		return "", false
	}
	return vo.ReleaseFailedRun, now.Sub(state.FaultAt) >= patience
}

func CanDeploy(spec RunSpec, lim Limits, container vo.ContainerState) error {
	if container.Running() {
		return ErrContainerRunning
	}
	if spec.Image.IsZero() {
		return ErrImageMissing
	}
	return lim.Allow(spec)
}

func CanStart(container vo.ContainerState) error {
	if !container.Exists() {
		return ErrContainerMissing
	}
	return nil
}

func CanStop(container vo.ContainerState) error {
	if !container.Exists() {
		return ErrContainerMissing
	}
	return nil
}

func VerifyImage(image, base vo.ImageLayers) error {
	if !image.DerivesFrom(base) {
		return ErrImageNotDerived
	}
	return nil
}

func SameImage(nodes []NodeImage) (vo.ImageDigest, error) {
	if len(nodes) == 0 {
		return "", ErrNoNodes
	}

	image := nodes[0].Image
	for _, n := range nodes {
		if n.Image != image || n.Image.IsZero() {
			return "", ErrImagesDiffer
		}
	}
	return image, nil
}
