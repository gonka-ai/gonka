package run_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared"
	"trainshard/internal/domain/shared/vo"
)

func TestAnUndoneDeployTimesTheOldFaultFromNow(t *testing.T) {
	// arrange
	reservedAt := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	faultAt := reservedAt.Add(time.Minute)
	now := reservedAt.Add(time.Hour)
	fault := shared.NewFault(errors.New("run failed"))
	before := run.RunState{Shard: 1, ReservedAt: reservedAt, Fault: fault, FaultAt: faultAt}
	runs := &runStoreStub{states: map[vo.NodeRef]run.RunState{nodeA: {Shard: 1, ReservedAt: reservedAt}}}

	// act
	err := run.UndoDeploy(context.Background(), runs, nodeA, before, now)

	// assert
	if err != nil {
		t.Fatalf("got %v, want the deploy undone", err)
	}
	state := runs.states[nodeA]
	if state.Fault == nil || !state.FaultAt.Equal(now) {
		t.Fatalf("got fault %v at %v, want the old fault timed from %v", state.Fault, state.FaultAt, now)
	}
}

func TestAnUndoneDeployWithoutAFaultLeavesNoFaultTime(t *testing.T) {
	// arrange
	before := run.RunState{Shard: 1}
	runs := &runStoreStub{states: map[vo.NodeRef]run.RunState{
		nodeA: {Shard: 1, Fault: shared.NewFault(errors.New("deploy failed")), FaultAt: time.Now()},
	}}

	// act
	err := run.UndoDeploy(context.Background(), runs, nodeA, before, time.Now())

	// assert
	if err != nil {
		t.Fatalf("got %v, want the deploy undone", err)
	}
	if state := runs.states[nodeA]; state.Fault != nil || !state.FaultAt.IsZero() {
		t.Fatalf("got fault %v at %v, want none", state.Fault, state.FaultAt)
	}
}
