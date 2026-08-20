package usecases_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	usecases "trainshard/internal/application/hostd/run/use_cases"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

func deployCommand() usecases.DeployCommand {
	return usecases.DeployCommand{NodesCommand: nodesCommand(), Run: runSpec()}
}

func TestDeployRefusesANodeWithoutFailingTheRequest(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fixture, *usecases.DeployCommand)
		want   string
	}{
		{
			name:   "actor that is neither creator nor run key",
			mutate: func(_ *fixture, c *usecases.DeployCommand) { c.Actor = shard.Actor{Address: stranger} },
			want:   "NOT_AUTHORIZED",
		},
		{
			name:   "node the chain did not reserve",
			mutate: func(_ *fixture, c *usecases.DeployCommand) { c.Nodes = []vo.NodeRef{nodeB} },
			want:   "NODE_NOT_RESERVED",
		},
		{
			name: "container still running",
			mutate: func(f *fixture, _ *usecases.DeployCommand) {
				f.containers.infos[nodeA] = run.ContainerInfo{State: vo.ContainerRunning, Image: runImage}
			},
			want: "CONTAINER_RUNNING",
		},
		{
			name:   "more gpus than the host allows",
			mutate: func(_ *fixture, c *usecases.DeployCommand) { c.Run.Resources.GPUs = 16 },
			want:   "GPUS_EXCEEDED",
		},
		{
			name:   "deadline already passed",
			mutate: func(_ *fixture, c *usecases.DeployCommand) { c.Deadline = now.Add(-time.Second) },
			want:   "DEADLINE_PASSED",
		},
		{
			name: "shard past its expiry height",
			mutate: func(f *fixture, _ *usecases.DeployCommand) {
				record := activeShard()
				record.ExpiresAtHeight = height
				f.chain.shards[shardID] = record
			},
			want: "SHARD_CLOSED",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			f := newFixture()
			cmd := deployCommand()
			tc.mutate(f, &cmd)

			results, err := f.deploy().Execute(context.Background(), cmd)

			if err != nil {
				t.Fatalf("a refused node must not fail the request: %v", err)
			}
			if len(results) != 1 || results[0].Fault == nil || results[0].Fault.Code != tc.want {
				t.Fatalf("got %+v, want a single %s failure", results, tc.want)
			}
			if len(f.runs.states) != 0 {
				t.Fatalf("a refused deploy must record nothing, got %v", f.runs.states)
			}
		})
	}
}

func TestDeployRecordsTheRunAndDropsAnEarlierStart(t *testing.T) {

	f := newFixture()
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Start: true, Fault: &oldFault}

	results, err := f.deploy().Execute(context.Background(), deployCommand())

	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if len(results) != 1 || !results[0].OK() {
		t.Fatalf("got %+v, want one accepted node", results)
	}
	state := f.runs.states[nodeA]
	if state.Spec.Image != runImage || state.Start || state.Fault != nil {
		t.Fatalf("got %+v, want the new run stopped and the old failure cleared", state)
	}
}

func TestDeployAnsweredTwiceActsOnce(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	first, err := f.deploy().Execute(ctx, deployCommand())
	if err != nil {
		t.Fatalf("first deploy: %v", err)
	}

	settled := f.rec.sequence()
	second, err := f.deploy().Execute(ctx, deployCommand())

	if err != nil {
		t.Fatalf("second deploy: %v", err)
	}
	if len(second) != len(first) || second[0].Node != first[0].Node {
		t.Fatalf("a repeated request must answer as the first did: %+v then %+v", first, second)
	}
	if !slices.Equal(f.rec.sequence(), settled) {
		t.Fatalf("a repeated request must touch nothing again, got %v after %v", f.rec.sequence(), settled)
	}
}

func TestDeployRejectsAShardTheChainDoesNotHave(t *testing.T) {

	f := newFixture()
	delete(f.chain.shards, shardID)

	_, err := f.deploy().Execute(context.Background(), deployCommand())

	if !errors.Is(err, shard.ErrShardUnknown) {
		t.Fatalf("got %v, want %v", err, shard.ErrShardUnknown)
	}
}
