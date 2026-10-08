package usecases_test

import (
	"context"
	"testing"
	"time"

	usecases "trainshard/internal/application/coord/assembly/use_cases"
	"trainshard/internal/domain/shared/vo"
)

type lifecycleOnlyStub struct {
	proposal uint64
	settled  vo.ShardID
	assigned vo.ShardID
}

func (l *lifecycleOnlyStub) Assemble(_ context.Context, proposal uint64) (vo.ShardID, error) {
	l.proposal = proposal
	return l.assigned, nil
}

func (l *lifecycleOnlyStub) Settle(_ context.Context, shardID vo.ShardID) error {
	l.settled = shardID
	return nil
}

type submitterOnlyStub struct {
	shard  vo.ShardID
	node   vo.NodeRef
	reason vo.ReleaseReason
}

func (*submitterOnlyStub) OptIn(context.Context, vo.NodeRef, time.Duration) error { return nil }

func (s *submitterOnlyStub) Release(_ context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error {
	s.shard, s.node, s.reason = shardID, node, reason
	return nil
}

func TestAssembleUseCaseAsksTheChainToAssembleTheProposal(t *testing.T) {
	// arrange
	lifecycle := &lifecycleOnlyStub{assigned: 7}
	uc := usecases.NewAssembleUseCase(lifecycle)

	// act
	got, err := uc.Execute(context.Background(), usecases.AssembleCommand{Proposal: 3})

	// assert
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if lifecycle.proposal != 3 || got != 7 {
		t.Fatalf("got proposal %d and shard %s, want 3 and 7", lifecycle.proposal, got)
	}
}

func TestSettleUseCaseAsksTheChainToSettleTheShard(t *testing.T) {
	// arrange
	lifecycle := &lifecycleOnlyStub{}
	uc := usecases.NewSettleUseCase(lifecycle)

	// act
	err := uc.Execute(context.Background(), usecases.SettleCommand{Shard: 7})

	// assert
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if lifecycle.settled != 7 {
		t.Fatalf("got shard %s, want 7", lifecycle.settled)
	}
}

func TestKickUseCaseReleasesTheNodeAsManualKick(t *testing.T) {
	// arrange
	submitter := &submitterOnlyStub{}
	uc := usecases.NewKickUseCase(submitter)
	node := vo.NodeRef{Participant: "gonka1host", NodeID: "node1"}

	// act
	err := uc.Execute(context.Background(), usecases.KickCommand{Shard: 7, Node: node})

	// assert
	if err != nil {
		t.Fatalf("kick: %v", err)
	}
	if submitter.shard != 7 || submitter.node != node || submitter.reason != vo.ReleaseManualKick {
		t.Fatalf("got release %+v, want shard 7 node %s reason %s", submitter, node, vo.ReleaseManualKick)
	}
}
