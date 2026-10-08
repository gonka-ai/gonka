package usecases_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

func TestStatusReportsWhatTheMachineHoldsAndWhyItStopped(t *testing.T) {
	// arrange
	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec(), Fault: &oldFault}
	f.gpu.inUse = 8

	// act
	items, err := f.status().Execute(ctx, nodesCommand())

	// assert
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d entries, want one per node", len(items))
	}
	item := items[0]
	if !item.Prepared || item.GPUsInUse != 8 {
		t.Fatalf("got %+v, want a prepared node with its gpus reported", item)
	}
	if item.Fault == nil || item.Fault.Code != oldFault.Code {
		t.Fatalf("got %+v, want the recorded reason reported back", item.Fault)
	}
}

func TestStatusNamesThePeersAMeshThatIsUpHasNotHeardFrom(t *testing.T) {
	// arrange
	f := newFixture()
	ctx := context.Background()
	if err := f.meshed(ctx); err != nil {
		t.Fatalf("mesh: %v", err)
	}
	f.network.silent = []vo.NodeRef{nodeB}

	// act
	items, err := f.status().Execute(ctx, nodesCommand())

	// assert
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(items) != 1 || !items[0].MeshUp || !slices.Equal(items[0].MeshSilent, []vo.NodeRef{nodeB}) {
		t.Fatalf("got %+v, want a mesh that is up and node-b named as not heard from", items)
	}
}

func TestStatusStaysAnAnswerWhenThePeersCannotBeRead(t *testing.T) {
	// arrange
	f := newFixture()
	ctx := context.Background()
	if err := f.meshed(ctx); err != nil {
		t.Fatalf("mesh: %v", err)
	}
	f.network.silentErr = errors.New("wireguard device gone")

	// act
	items, err := f.status().Execute(ctx, nodesCommand())

	// assert
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(items) != 1 || !items[0].OK() || !items[0].MeshUp || len(items[0].MeshSilent) != 0 || !items[0].MeshSilentUnknown {
		t.Fatalf("got %+v, want the node's status with no peers named and the peers marked unknown", items)
	}
}

func TestStatusAsksNoPeersOfAMeshThatIsNotUp(t *testing.T) {
	// arrange
	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.network.silent = []vo.NodeRef{nodeB}

	// act
	items, err := f.status().Execute(ctx, nodesCommand())

	// assert
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(items) != 1 || items[0].MeshUp || len(items[0].MeshSilent) != 0 {
		t.Fatalf("got %+v, want a mesh that is down and no peers named", items)
	}
}

func TestStatusHidesWhatTheShardBeforeLeftOnTheNode(t *testing.T) {
	// arrange
	f := newFixture()
	f.runs.states[nodeA] = run.RunState{Shard: shardID - 1, Spec: runSpec(), Fault: &oldFault}

	// act
	items, err := f.status().Execute(context.Background(), nodesCommand())

	// assert
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(items) != 1 || items[0].Fault != nil {
		t.Fatalf("got %+v, want nothing of the previous shard's run", items)
	}
}

func TestStatusKeepsARefusalInTheNodeEntry(t *testing.T) {
	// arrange
	f := newFixture()
	cmd := nodesCommand()
	cmd.Actor = shard.Actor{Address: stranger}

	// act
	items, err := f.status().Execute(context.Background(), cmd)

	// assert
	if err != nil {
		t.Fatalf("a per-node refusal must not fail the request: %v", err)
	}
	if len(items) != 1 || items[0].Fault == nil || items[0].Fault.Code != "NOT_AUTHORIZED" {
		t.Fatalf("got %+v, want a single NOT_AUTHORIZED failure", items)
	}
}
