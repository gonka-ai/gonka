package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	usecases "trainshard/internal/application/coord/assembly/use_cases"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type lifecycleOnlyStub struct {
	proposal uint64
	settled  vo.ShardID
	assigned vo.ShardID
	closed   int
	asked    int
}

func (l *lifecycleOnlyStub) Assemble(_ context.Context, proposal uint64) (vo.ShardID, error) {
	l.asked++
	if l.asked <= l.closed {
		return 0, shard.ErrAssemblyClosed
	}
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

// windowStub opens at opens and moves one block per read
type windowStub struct {
	now, opens vo.Height
	reads      int
}

func (w *windowStub) AssemblyOpensAt(context.Context) (vo.Height, vo.Height, error) {
	w.reads++
	now := w.now
	w.now++
	return now, max(now, w.opens), nil
}

func TestAssembleUseCaseAsksTheChainToAssembleTheProposal(t *testing.T) {
	// arrange
	lifecycle := &lifecycleOnlyStub{assigned: 7}
	uc := usecases.NewAssembleUseCase(lifecycle, &windowStub{now: 10, opens: 10}, time.Millisecond)

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

func TestAssembleUseCaseWaitsOutPoCBeforeAsking(t *testing.T) {
	// arrange
	lifecycle := &lifecycleOnlyStub{assigned: 7}
	window := &windowStub{now: 10, opens: 13}
	uc := usecases.NewAssembleUseCase(lifecycle, window, time.Millisecond)
	var told []vo.Height

	// act
	got, err := uc.Execute(context.Background(), usecases.AssembleCommand{Proposal: 3, Waiting: func(now, opens vo.Height) {
		told = append(told, now, opens)
	}})

	// assert
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if window.reads != 4 || got != 7 {
		t.Fatalf("got %d reads and shard %s, want 4 reads and shard 7", window.reads, got)
	}
	if len(told) != 2 || told[0] != 10 || told[1] != 13 {
		t.Fatalf("got told %v, want one word at height 10 that it opens at 13", told)
	}
}

func TestAssembleUseCaseWaitsAgainWhenTheChainSawAConfirmationPoCFirst(t *testing.T) {
	// arrange
	lifecycle := &lifecycleOnlyStub{assigned: 7, closed: 1}
	uc := usecases.NewAssembleUseCase(lifecycle, &windowStub{now: 10, opens: 10}, time.Millisecond)

	// act
	got, err := uc.Execute(context.Background(), usecases.AssembleCommand{Proposal: 3})

	// assert
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if lifecycle.asked != 2 || got != 7 {
		t.Fatalf("got %d asks and shard %s, want the refused ask repeated once and shard 7", lifecycle.asked, got)
	}
}

func TestAssembleUseCaseSendsNothingWhenCancelledDuringPoC(t *testing.T) {
	// arrange
	lifecycle := &lifecycleOnlyStub{assigned: 7}
	uc := usecases.NewAssembleUseCase(lifecycle, &windowStub{now: 10, opens: 1_000_000}, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	// act
	_, err := uc.Execute(ctx, usecases.AssembleCommand{Proposal: 3})

	// assert
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want the deadline", err)
	}
	if lifecycle.proposal != 0 {
		t.Fatalf("the chain was asked to assemble proposal %d during PoC", lifecycle.proposal)
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
