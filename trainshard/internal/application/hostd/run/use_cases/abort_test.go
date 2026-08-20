package usecases_test

import (
	"context"
	"errors"
	"testing"

	"trainshard/internal/domain/shard"
)

func TestAbortGivesTheReservationBackAndLeavesCleanupToTheLoop(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.rec.reset()

	err := f.abort().Execute(ctx, nodeA)

	if err != nil {
		t.Fatalf("abort: %v", err)
	}
	if len(f.chain.releases) != 1 || f.chain.releases[0] != "7:node-a:operator_abort" {
		t.Fatalf("got %v, want the reservation given back as an operator abort", f.chain.releases)
	}
	if len(f.rec.calls) != 0 {
		t.Fatalf("got %v, want an abort to touch nothing on the machine itself", f.rec.calls)
	}

	if err := f.reconcile().Execute(ctx, nodeA); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if len(f.rec.calls) == 0 {
		t.Fatal("want the loop to clean up after an aborted run")
	}
}

func TestAbortRefusesANodeThatIsNotWorkingForAnyone(t *testing.T) {

	f := newFixture()
	delete(f.chain.reservations, nodeA)

	err := f.abort().Execute(context.Background(), nodeA)

	if !errors.Is(err, shard.ErrNodeNotReserved) {
		t.Fatalf("got %v, want a refusal", err)
	}
	if len(f.chain.releases) != 0 {
		t.Fatalf("got %v, want nothing released", f.chain.releases)
	}
}
