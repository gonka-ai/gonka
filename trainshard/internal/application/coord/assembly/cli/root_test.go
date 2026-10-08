package cli_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"trainshard/internal/application/coord/assembly/cli"
	usecases "trainshard/internal/application/coord/assembly/use_cases"
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

type released struct {
	shard  vo.ShardID
	node   vo.NodeRef
	reason vo.ReleaseReason
}

type submitterStub struct {
	released []released
}

func (s *submitterStub) OptIn(context.Context, vo.NodeRef, time.Duration) error { return nil }

func (s *submitterStub) Release(_ context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error {
	s.released = append(s.released, released{shard: shardID, node: node, reason: reason})
	return nil
}

func TestAssemblingAProposalAnswersWithTheShardTheChainNamed(t *testing.T) {
	// arrange
	lifecycle := &lifecycleStub{assigned: 7}
	out := &bytes.Buffer{}
	commands := cli.New(cli.UseCases{Assemble: usecases.NewAssembleUseCase(lifecycle)}, nil, out)

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
	commands := cli.New(cli.UseCases{Settle: usecases.NewSettleUseCase(lifecycle)}, nil, &bytes.Buffer{})

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

func TestKickingReleasesTheNodeItNamesFromTheShardItNames(t *testing.T) {
	// arrange
	submitter := &submitterStub{}
	commands := cli.New(cli.UseCases{Kick: usecases.NewKickUseCase(submitter)}, nil, &bytes.Buffer{})

	// act
	err := commands.Kick(context.Background(), []string{"7", "gonka1host/node1"})

	// assert
	if err != nil {
		t.Fatalf("kick: %v", err)
	}
	want := released{shard: 7, node: vo.NodeRef{Participant: "gonka1host", NodeID: "node1"}, reason: vo.ReleaseManualKick}
	if len(submitter.released) != 1 || submitter.released[0] != want {
		t.Fatalf("got %+v, want only %+v", submitter.released, want)
	}
}

func TestAKickThatDoesNotNameANodeIsRefusedBeforeTheChainIsAsked(t *testing.T) {
	cases := map[string][]string{
		"no participant":       {"7", "node1"},
		"not an address":       {"7", "host/node1"},
		"a node id that walks": {"7", "gonka1host/.."},
		"no node at all":       {"7"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			// arrange
			submitter := &submitterStub{}
			commands := cli.New(cli.UseCases{Kick: usecases.NewKickUseCase(submitter)}, nil, &bytes.Buffer{})

			// act
			err := commands.Kick(context.Background(), args)

			// assert
			if err == nil {
				t.Fatal("want the kick refused")
			}
			if len(submitter.released) != 0 {
				t.Fatalf("got %+v, want the chain left alone", submitter.released)
			}
		})
	}
}

func TestAProposalThatIsNotANumberIsRefusedBeforeTheChainIsAsked(t *testing.T) {
	// arrange
	lifecycle := &lifecycleStub{}
	commands := cli.New(cli.UseCases{Assemble: usecases.NewAssembleUseCase(lifecycle)}, nil, &bytes.Buffer{})

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
