package run

import (
	"context"
	"fmt"
	"time"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

func RecordReservation(ctx context.Context, runs RunStore, node vo.NodeRef, shardID vo.ShardID, at time.Time) error {
	return runs.Update(ctx, node, func(state *RunState) { *state = RunState{Shard: shardID, ReservedAt: at} })
}

func RecordDeploy(ctx context.Context, runs RunStore, node vo.NodeRef, shardID vo.ShardID, spec RunSpec) error {
	return runs.Update(ctx, node, func(state *RunState) {
		state.Shard, state.Spec, state.Start = shardID, spec, false
		state.Fault, state.FaultAt = nil, time.Time{}
	})
}

func RecordStart(ctx context.Context, runs RunStore, node vo.NodeRef) error {
	return runs.Update(ctx, node, func(state *RunState) { state.Start = true })
}

func RecordStop(ctx context.Context, runs RunStore, node vo.NodeRef, grace time.Duration) error {
	return runs.Update(ctx, node, func(state *RunState) { state.Start, state.StopGrace = false, grace })
}

func RecordImage(ctx context.Context, runs RunStore, node vo.NodeRef, image vo.ImageDigest, at time.Time) error {
	return runs.Update(ctx, node, func(state *RunState) {
		state.Images = append(state.Images, ImageRun{Image: image, At: at})
	})
}

func RecordFault(ctx context.Context, runs RunStore, node vo.NodeRef, action Action, cause error, at time.Time) error {
	failure := fmt.Errorf("%s: %w", action.Kind, cause)

	change := func(state *RunState) {
		if state.Fault == nil {
			state.FaultAt = at
		}
		state.Fault = shared.NewFault(failure)
	}
	if err := runs.Update(ctx, node, change); err != nil {
		return fmt.Errorf("%w (recording it also failed: %v)", failure, err)
	}
	return failure
}

func ClearFault(ctx context.Context, runs RunStore, node vo.NodeRef) error {
	return runs.Update(ctx, node, func(state *RunState) { state.Fault, state.FaultAt = nil, time.Time{} })
}
