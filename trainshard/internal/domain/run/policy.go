package run

import (
	"time"

	"trainshard/internal/domain/shared/vo"
)

// Autokick times readiness from when it was lost, not from the reservation, or a node an hour into
// a run would be handed back on the first busy card
func Autokick(d Desired, o Observed, state RunState, now time.Time, patience time.Duration) (vo.ReleaseReason, bool) {
	// a shard past its expiry is cleanup's: a release sent first only holds the wipe back
	if !d.Reserved || !d.Active || state.ReservedAt.IsZero() {
		return "", false
	}
	if !state.ReleasedAt.IsZero() && now.Sub(state.ReleasedAt) < patience {
		return "", false
	}
	if !Prepared(d, o) {
		since := state.UnpreparedAt
		if since.IsZero() {
			since = state.ReservedAt
		}
		return vo.ReleaseFailedPrepare, now.Sub(since) >= patience
	}
	// an image that does not derive from the base is the tenant's to replace with a deploy, not a
	// failing node
	if state.Fault == nil || state.Fault.Code == ErrImageNotDerived.Code {
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
	if spec.NamesHostEnv() {
		return ErrEnvReserved
	}
	return lim.Allow(spec)
}

func CanStart(container vo.ContainerState) error {
	if !container.Exists() {
		return ErrContainerMissing
	}
	if container == vo.ContainerExited {
		return ErrContainerFinished
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
