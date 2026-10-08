package run

import (
	"context"
	"fmt"
	"time"

	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

// Reserve stamps the patience clock once per shard, so a node under the same shard keeps running
// out of time
func (s *RunState) Reserve(shardID vo.ShardID, at time.Time) bool {
	if s.Shard == shardID && !s.ReservedAt.IsZero() {
		return false
	}
	if s.Shard != shardID {
		*s = RunState{}
	}
	s.Shard, s.ReservedAt = shardID, at
	return true
}

// For hides the state from the given shard until the node is reserved for it: what it holds until
// then belongs to the shard before
func (s RunState) For(shardID vo.ShardID) RunState {
	if s.Shard != shardID {
		return RunState{Shard: shardID}
	}
	return s
}

func RecordReservation(ctx context.Context, runs RunStore, node vo.NodeRef, shardID vo.ShardID, at time.Time) error {
	return runs.Update(ctx, node, func(state *RunState) { state.Reserve(shardID, at) })
}

// RecordDeploy bumps the revision the container is built for, so a rerun of the same spec is still
// a new container; a deploy for another shard starts from nothing
func RecordDeploy(ctx context.Context, runs RunStore, node vo.NodeRef, shardID vo.ShardID, spec RunSpec) error {
	return runs.Update(ctx, node, func(state *RunState) {
		if state.Shard != shardID {
			*state = RunState{}
		}
		state.Shard, state.Spec, state.Start = shardID, spec, false
		state.Revision++
		state.Fault, state.FaultAt = nil, time.Time{}
	})
}

// UndoDeploy puts the revision back too, so the container a refused deploy left on the node is not
// rebuilt for nothing. A fault put back is timed from now: its old time would count the patience
// of a node that was only waiting on a deploy the tenant got refused
func UndoDeploy(ctx context.Context, runs RunStore, node vo.NodeRef, before RunState, now time.Time) error {
	return runs.Update(ctx, node, func(state *RunState) {
		was := before.For(state.Shard)
		state.Spec, state.Revision, state.Start = was.Spec, was.Revision, was.Start
		state.Fault, state.FaultAt = was.Fault, time.Time{}
		if was.Fault != nil {
			state.FaultAt = now
		}
	})
}

// RecordRebuild counts a new place on the mesh as a new container, even for the spec it already
// runs: the place is baked in at create
func RecordRebuild(ctx context.Context, runs RunStore, node vo.NodeRef) error {
	return runs.Update(ctx, node, func(state *RunState) { state.Revision++ })
}

func RecordRelease(ctx context.Context, runs RunStore, node vo.NodeRef, at time.Time) error {
	return runs.Update(ctx, node, func(state *RunState) { state.ReleasedAt = at })
}

func RecordStart(ctx context.Context, runs RunStore, node vo.NodeRef) error {
	return runs.Update(ctx, node, func(state *RunState) { state.Start = true })
}

func RecordStop(ctx context.Context, runs RunStore, node vo.NodeRef, grace time.Duration, given bool) error {
	if !given {
		grace = 0
	}
	return runs.Update(ctx, node, func(state *RunState) {
		state.Start, state.StopGrace, state.StopGraceGiven = false, grace, given
	})
}

func RecordImage(ctx context.Context, runs RunStore, node vo.NodeRef, image vo.ImageDigest, at time.Time) error {
	return runs.Update(ctx, node, func(state *RunState) {
		state.Images = append(state.Images, ImageRun{Image: image, At: at})
	})
}

func RecordFault(ctx context.Context, runs RunStore, node vo.NodeRef, action Action, cause error, at time.Time) error {
	failure := &ActionFailed{Kind: action.Kind, cause: cause}

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

// TrackPreparedness times the wait before a handback from when the node stopped being ready, the
// way a fault is timed; a node the chain no longer holds is not timed at all
func TrackPreparedness(ctx context.Context, runs RunStore, node vo.NodeRef, state *RunState, d Desired, o Observed, at time.Time) error {
	was := state.UnpreparedAt
	switch {
	case !d.Reserved || Prepared(d, o):
		state.UnpreparedAt = time.Time{}
	case was.IsZero():
		state.UnpreparedAt = at
	}
	if state.UnpreparedAt.Equal(was) {
		return nil
	}
	mark := state.UnpreparedAt
	return runs.Update(ctx, node, func(s *RunState) { s.UnpreparedAt = mark })
}
