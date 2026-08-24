package readiness

import (
	"context"
	"errors"

	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type Spec struct {
	Version          string
	SupportedVersion string
	MinFreeDiskBytes int64
}

func Collect(ctx context.Context, probe ports.Probe, cards Cards, claim Claim, node vo.NodeRef, spec Spec) Result {
	machine, machineErr := cards.Inventory(ctx, node)
	claimed, claimedErr := claim.Hardware(ctx, node)
	free, diskErr := probe.FreeDiskBytes(ctx)

	return Evaluate([]Check{
		From(CheckDockerGPU, probe.GPUContainer(ctx)),
		SameGPUs(machine, claimed, errors.Join(machineErr, claimedErr)),
		FreeDisk(free, spec.MinFreeDiskBytes, diskErr),
		From(CheckMeshPort, probe.MeshPortReachable(ctx)),
		SupportedBuild(spec.Version, spec.SupportedVersion),
	})
}
