package run

import (
	"context"
	"slices"
	"time"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/domain/shared/vo"
)

type Machine struct {
	Images     Images
	Containers Containers
	Volumes    Volumes
	GPU        GPU
	Mesh       RunNetwork
	Egress     Egress
	Control    NodeControl
	Runs       RunStore
	Clock      ports.Clock
	StopGrace  time.Duration
}

func (m Machine) Observe(ctx context.Context, node vo.NodeRef, desired Desired) (Observed, error) {
	shardID := desired.Shard

	drained, err := m.Control.Drained(ctx, node)
	if err != nil {
		return Observed{}, err
	}
	foreign, err := m.GPU.ForeignWork(ctx, shardID, node)
	if err != nil {
		return Observed{}, err
	}
	inUse, err := m.GPU.InUse(ctx, node)
	if err != nil {
		return Observed{}, err
	}
	leftovers, err := m.GPU.TrainingProcesses(ctx, shardID, node)
	if err != nil {
		return Observed{}, err
	}
	images, err := m.cachedImages(ctx, desired.BaseImage, desired.Run.Image)
	if err != nil {
		return Observed{}, err
	}
	container, err := m.Containers.Inspect(ctx, shardID, node)
	if err != nil {
		return Observed{}, err
	}
	key, up, err := m.Mesh.Present(ctx, shardID, node)
	if err != nil {
		return Observed{}, err
	}
	used, quota, volumes, err := m.Volumes.Usage(ctx, shardID, node)
	if err != nil {
		return Observed{}, err
	}

	return Observed{
		Drained:           drained,
		ForeignGPUWork:    foreign,
		GPUsInUse:         inUse,
		Images:            images,
		Container:         container.State,
		ContainerImage:    container.Image,
		ContainerRevision: container.Revision,
		ExitCode:          container.ExitCode,
		MeshKey:           key,
		MeshUp:            up,
		VolumesPresent:    volumes,
		DiskUsedBytes:     used,
		DiskQuotaBytes:    quota,
		TrainingProcesses: leftovers,
	}, nil
}

// Sweep wipes what a shard this node no longer serves left behind, so a run that outlived
// the state describing it cannot follow the node into the next one
func (m Machine) Sweep(ctx context.Context, node vo.NodeRef, serving vo.ShardID) error {
	held, err := m.leftovers(ctx, node)
	if err != nil {
		return err
	}

	for _, shardID := range held {
		if shardID == serving {
			continue
		}
		stale := Desired{Reservation: Reservation{Shard: shardID}}

		observed, err := m.Observe(ctx, node, stale)
		if err != nil {
			return err
		}
		for _, action := range WipePlan(observed) {
			if err := m.Apply(ctx, node, stale, action); err != nil {
				return RecordFault(ctx, m.Runs, node, action, err, m.Clock.Now())
			}
		}
	}
	return nil
}

func (m Machine) leftovers(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error) {
	boxed, err := m.Containers.Shards(ctx, node)
	if err != nil {
		return nil, err
	}
	stored, err := m.Volumes.Shards(ctx, node)
	if err != nil {
		return nil, err
	}
	keyed, err := m.Mesh.Shards(ctx, node)
	if err != nil {
		return nil, err
	}

	held := slices.Concat(boxed, stored, keyed)
	slices.Sort(held)
	return slices.Compact(held), nil
}

func (m Machine) Apply(ctx context.Context, node vo.NodeRef, desired Desired, action Action) error {
	shardID := desired.Shard

	switch action.Kind {
	case ActionDrainNode:
		_, err := m.Control.Drain(ctx, node)
		return err
	case ActionPullImage:
		return m.Images.Pull(ctx, action.Image)
	case ActionCreateMeshIdentity:
		return m.Mesh.Create(ctx, shardID, node)
	case ActionApplyMeshConfig:
		return m.Mesh.Apply(ctx, shardID, node)
	case ActionCreateContainer, ActionReplaceContainer:
		return m.createContainer(ctx, node, desired, action.Kind == ActionReplaceContainer)
	case ActionStartContainer:
		return m.Containers.Start(ctx, shardID, node)
	case ActionStopContainer:
		return m.Containers.Stop(ctx, shardID, node, m.grace(desired))
	case ActionRemoveContainer:
		return m.Containers.Remove(ctx, shardID, node)
	case ActionRemoveMesh:
		return m.Mesh.Remove(ctx, shardID, node)
	case ActionWipeVolumes:
		return m.Volumes.Wipe(ctx, shardID, node)
	case ActionKillGPUProcesses:
		return m.GPU.KillTraining(ctx, shardID, node)
	case ActionReturnNode:
		if err := m.Control.Return(ctx, node); err != nil {
			return err
		}
		return m.Runs.Forget(ctx, node)
	case ActionForgetRun:
		return m.Runs.Forget(ctx, node)
	default:
		return shared.New("UNKNOWN_ACTION", shared.ErrValidation, "unknown action "+string(action.Kind))
	}
}

func (m Machine) grace(desired Desired) time.Duration {
	if desired.StopGrace <= 0 || desired.StopGrace > m.StopGrace {
		return m.StopGrace
	}
	return desired.StopGrace
}

// createContainer runs everything that can still refuse the run before it takes the old
// container away, so a deploy that falls over leaves the node on the image it already had
func (m Machine) createContainer(ctx context.Context, node vo.NodeRef, desired Desired, replacing bool) error {
	if err := m.verifyImage(ctx, desired); err != nil {
		return err
	}
	if err := m.Volumes.Ensure(ctx, desired.Shard, node, desired.Run.Resources.DiskBytes); err != nil {
		return err
	}
	pinned, err := m.Egress.Allow(ctx, desired.Shard, node, desired.Run.Sources)
	if err != nil {
		return err
	}
	if replacing {
		if err := m.Containers.Remove(ctx, desired.Shard, node); err != nil {
			return err
		}
	}
	spec := ContainerSpec{Shard: desired.Shard, Node: node, Run: desired.Run, Revision: desired.Revision, Hosts: pinned}
	if err := m.Containers.Create(ctx, spec); err != nil {
		return err
	}
	return RecordImage(ctx, m.Runs, node, desired.Run.Image, m.Clock.Now())
}

func (m Machine) verifyImage(ctx context.Context, desired Desired) error {
	image, err := m.Images.Layers(ctx, desired.Run.Image)
	if err != nil {
		return err
	}
	base, err := m.Images.Layers(ctx, desired.BaseImage)
	if err != nil {
		return err
	}
	return VerifyImage(image, base)
}

func (m Machine) cachedImages(ctx context.Context, digests ...vo.ImageDigest) ([]vo.ImageDigest, error) {
	cached := make([]vo.ImageDigest, 0, len(digests))
	for _, digest := range digests {
		if digest.IsZero() {
			continue
		}
		present, err := m.Images.Has(ctx, digest)
		if err != nil {
			return nil, err
		}
		if present {
			cached = append(cached, digest)
		}
	}
	return cached, nil
}
