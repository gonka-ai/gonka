package usecases_test

import (
	"context"
	"testing"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
)

func TestStatusReportsWhatTheMachineHoldsAndWhyItStopped(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec(), Fault: &oldFault}
	f.gpu.inUse = 8

	items, err := f.status().Execute(ctx, nodesCommand())

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

func TestStatusKeepsARefusalInTheNodeEntry(t *testing.T) {

	f := newFixture()
	cmd := nodesCommand()
	cmd.Actor = shard.Actor{Address: stranger}

	items, err := f.status().Execute(context.Background(), cmd)

	if err != nil {
		t.Fatalf("a per-node refusal must not fail the request: %v", err)
	}
	if len(items) != 1 || items[0].Fault == nil || items[0].Fault.Code != "NOT_AUTHORIZED" {
		t.Fatalf("got %+v, want a single NOT_AUTHORIZED failure", items)
	}
}
