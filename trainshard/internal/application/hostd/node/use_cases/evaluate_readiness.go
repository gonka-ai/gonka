package usecases

import (
	"context"
	"errors"

	"trainshard/internal/domain/readiness"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type EvaluateReadinessUseCase struct {
	probe            ports.Probe
	gpu              run.GPU
	chain            shard.ChainReader
	version          string
	supportedVersion string
	minFreeDiskBytes int64
}

func NewEvaluateReadinessUseCase(
	probe ports.Probe,
	gpu run.GPU,
	chain shard.ChainReader,
	version string,
	supportedVersion string,
	minFreeDiskBytes int64,
) *EvaluateReadinessUseCase {
	return &EvaluateReadinessUseCase{
		probe:            probe,
		gpu:              gpu,
		chain:            chain,
		version:          version,
		supportedVersion: supportedVersion,
		minFreeDiskBytes: minFreeDiskBytes,
	}
}

func (uc *EvaluateReadinessUseCase) Execute(ctx context.Context, node vo.NodeRef) readiness.Result {
	// 1. Check docker can give a container the GPUs
	dockerGPU := readiness.From(readiness.CheckDockerGPU, uc.probe.GPUContainer(ctx))

	// 2. Check hardware matches chain
	machine, machineErr := uc.gpu.Inventory(ctx, node)
	claimed, claimedErr := uc.chain.Hardware(ctx, node)
	hardware := readiness.SameGPUs(machine, claimed, errors.Join(machineErr, claimedErr))

	// 3. Check free disk
	free, diskErr := uc.probe.FreeDiskBytes(ctx)
	disk := readiness.FreeDisk(free, uc.minFreeDiskBytes, diskErr)

	// 4. Check mesh port from outside
	meshPort := readiness.From(readiness.CheckMeshPort, uc.probe.MeshPortReachable(ctx))

	// 5. Check supported build
	build := readiness.SupportedBuild(uc.version, uc.supportedVersion)

	// 6. Any fail keeps the node out
	return readiness.Evaluate([]readiness.Check{dockerGPU, hardware, disk, meshPort, build})
}
