package cli_test

import (
	"bytes"
	"context"
	"testing"

	"trainshard/internal/application/coord/assembly/cli"
	"trainshard/internal/domain/shared/vo"
)

type lifecycleStub struct {
	proposal uint64
	settled  vo.ShardID
	assigned vo.ShardID
}

func (l *lifecycleStub) Assemble(_ context.Context, proposal uint64) (vo.ShardID, error) {
	l.proposal = proposal
	return l.assigned, nil
}

func (l *lifecycleStub) Settle(_ context.Context, shardID vo.ShardID) error {
	l.settled = shardID
	return nil
}

func TestAssemblingAProposalAnswersWithTheShardTheChainNamed(t *testing.T) {
	// arrange
	lifecycle := &lifecycleStub{assigned: 7}
	out := &bytes.Buffer{}
	commands := cli.New(nil, lifecycle, nil, out)

	// act
	err := commands.Assemble(context.Background(), []string{"3"})

	// assert
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if lifecycle.proposal != 3 {
		t.Fatalf("got proposal %d, want the one asked for", lifecycle.proposal)
	}
	if out.String() != "7\n" {
		t.Fatalf("got %q, want the shard the chain named", out.String())
	}
}

func TestSettlingClosesTheShardItWasGiven(t *testing.T) {
	// arrange
	lifecycle := &lifecycleStub{}
	commands := cli.New(nil, lifecycle, nil, &bytes.Buffer{})

	// act
	err := commands.Settle(context.Background(), []string{"7"})

	// assert
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if lifecycle.settled != 7 {
		t.Fatalf("got shard %s, want the one asked for", lifecycle.settled)
	}
}

func TestAProposalThatIsNotANumberIsRefusedBeforeTheChainIsAsked(t *testing.T) {
	// arrange
	lifecycle := &lifecycleStub{}
	commands := cli.New(nil, lifecycle, nil, &bytes.Buffer{})

	// act
	err := commands.Assemble(context.Background(), []string{"seven"})

	// assert
	if err == nil {
		t.Fatal("want a proposal that is not a number refused")
	}
	if lifecycle.proposal != 0 {
		t.Fatalf("got proposal %d, want the chain left alone", lifecycle.proposal)
	}
}
