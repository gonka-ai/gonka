package usecases_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

func TestReconcilePullsTheBaseImageWhileTheNodeDrains(t *testing.T) {

	f := newFixture()
	uc := f.reconcile()

	for range 3 {
		if err := uc.Execute(context.Background(), nodeA); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}

	want := []string{"runs.update", "images.pull", "control.drain", "mesh.identity", "mesh_store.save_identity"}
	if !reflect.DeepEqual(f.rec.sequence(), want) {
		t.Fatalf("got %v, want %v", f.rec.sequence(), want)
	}
	if f.runs.states[nodeA].Shard != shardID {
		t.Fatalf("the shard must be recorded before the machine is touched, got %v", f.runs.states[nodeA].Shard)
	}
}

func TestReconcileSignsTheMeshMemberItPublishes(t *testing.T) {

	f := newFixture()
	f.control.drained = true
	f.images.present[baseImage] = true

	if err := f.reconcile().Execute(context.Background(), nodeA); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	identity := f.store.identities[nodeA]
	if len(f.attestor.payloads) != 1 {
		t.Fatalf("the member must be signed exactly once, got %d signatures", len(f.attestor.payloads))
	}
	if !reflect.DeepEqual(f.attestor.payloads[0], mesh.IdentityPayload(shardID, identity.Member)) {
		t.Fatal("the signature must cover the member the peers will use")
	}
}

func TestReconcileDoesNothingWhenTheRunAlreadyMatches(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec(), Start: true}
	f.images.present[runImage] = true
	for range 2 {
		if err := f.reconcile().Execute(ctx, nodeA); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}
	f.rec.reset()

	if err := f.reconcile().Execute(ctx, nodeA); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if calls := f.rec.sequence(); len(calls) != 0 {
		t.Fatalf("a settled run must not touch the machine, got %v", calls)
	}
}

func TestReconcileCreatesNoContainerBehindANetworkItCouldNotClose(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec()}
	f.egress.err = errors.New("nft is not there")

	err := f.reconcile().Execute(ctx, nodeA)

	if err == nil {
		t.Fatal("a run whose box cannot be closed must not come up")
	}
	if slices.Contains(f.rec.sequence(), "containers.create") {
		t.Fatalf("got %v, want no container created while the network is still open", f.rec.sequence())
	}
	if fault := f.runs.states[nodeA].Fault; fault == nil {
		t.Fatal("the node must carry the reason it did not come up")
	}
}

func TestReconcileBringsUpTheRunInOrder(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec(), Start: true}

	for range 3 {
		if err := f.reconcile().Execute(ctx, nodeA); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}

	want := []string{"images.pull", "volumes.ensure", "egress.allow", "containers.create", "runs.update", "containers.start"}
	if !reflect.DeepEqual(f.rec.sequence(), want) {
		t.Fatalf("got %v, want %v", f.rec.sequence(), want)
	}
	if images := f.runs.states[nodeA].Images; len(images) != 1 || images[0].Image != runImage || !images[0].At.Equal(now) {
		t.Fatalf("every image the run held must be recorded with its time, got %v", images)
	}
}

func TestReconcileRefusesAnImageNotBuiltOnTheBase(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec()}
	f.images.present[runImage] = true
	f.images.layers[runImage] = vo.ImageLayers{"someone-elses-layer"}

	err := f.reconcile().Execute(ctx, nodeA)

	if !errors.Is(err, run.ErrImageNotDerived) {
		t.Fatalf("got %v, want %v", err, run.ErrImageNotDerived)
	}
	fault := f.runs.states[nodeA].Fault
	if fault == nil || fault.Code != "IMAGE_NOT_DERIVED" {
		t.Fatalf("the reason must be readable from status, got %v", fault)
	}
}

func TestReconcileClearsTheReasonOnceTheRunRecovers(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec(), Fault: &oldFault}
	f.images.present[runImage] = true

	for range 2 {
		if err := f.reconcile().Execute(ctx, nodeA); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}

	if fault := f.runs.states[nodeA].Fault; fault != nil {
		t.Fatalf("a recovered run must stop reporting a fault, got %v", fault)
	}
}

func TestReconcileWipesTheRunWhenTheReservationIsGone(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec(), Start: true}
	f.images.present[runImage] = true
	for range 2 {
		if err := f.reconcile().Execute(ctx, nodeA); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}
	f.gpu.leftovers = true
	delete(f.chain.reservations, nodeA)
	f.rec.reset()

	if err := f.reconcile().Execute(ctx, nodeA); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	want := []string{
		"containers.stop",
		"containers.remove",
		"mesh.remove",
		"mesh_store.forget",
		"volumes.wipe",
		"gpu.kill_training",
	}
	if !reflect.DeepEqual(f.rec.sequence(), want) {
		t.Fatalf("got %v, want %v", f.rec.sequence(), want)
	}
}

func TestReconcileReturnsTheNodeOnlyAfterCleanupIsDone(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	delete(f.chain.reservations, nodeA)

	first := f.reconcile().Execute(ctx, nodeA)
	returnedEarly := f.control.drained == false
	second := f.reconcile().Execute(ctx, nodeA)

	if first != nil || second != nil {
		t.Fatalf("cleanup must not fail: %v then %v", first, second)
	}
	if returnedEarly {
		t.Fatal("the node was handed back before its mesh key was removed")
	}
	if f.control.drained {
		t.Fatal("a cleaned node must be handed back to inference")
	}
	if _, found := f.runs.states[nodeA]; found {
		t.Fatal("a returned node must leave no run state behind")
	}
}

func TestReconcileWipesWhatAForgottenShardLeftBeforeHandingTheNodeBack(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	f.control.drained = true
	f.volumes.present[shardID+1] = true
	delete(f.chain.reservations, nodeA)

	if err := f.reconcile().Execute(ctx, nodeA); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	calls := f.rec.sequence()
	wiped, handed := slices.Index(calls, "volumes.wipe"), slices.Index(calls, "control.return")
	if wiped < 0 {
		t.Fatalf("got %v, want the leftovers of a shard local state forgot wiped", calls)
	}
	if handed >= 0 && handed < wiped {
		t.Fatalf("got %v, want the node handed back only once nothing of any run is left", calls)
	}
}

func TestReconcileWipesAShardKnownOnlyByItsMeshKey(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	f.control.drained = true
	f.network.keys[shardID+1] = true
	delete(f.chain.reservations, nodeA)

	if err := f.reconcile().Execute(ctx, nodeA); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if !slices.Contains(f.rec.sequence(), "mesh.remove") {
		t.Fatalf("got %v, want the key of a shard local state forgot removed", f.rec.sequence())
	}
}

func TestReconcileLeavesTheShardItIsServingToItsOwnPlan(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	f.runs.states[nodeA] = run.RunState{Shard: shardID, Spec: runSpec(), Start: true}
	f.images.present[runImage] = true
	for range 2 {
		if err := f.reconcile().Execute(ctx, nodeA); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}
	f.containers.leftover = []vo.ShardID{shardID}
	f.rec.reset()

	if err := f.reconcile().Execute(ctx, nodeA); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if calls := f.rec.sequence(); len(calls) != 0 {
		t.Fatalf("got %v, want the running shard's own labels left alone", calls)
	}
}

func TestReconcileHandsBackANodeThatNeverGetsReady(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	f.control.stuck = true

	if err := f.reconcile().Execute(ctx, nodeA); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	f.clock.Advance(f.patience)
	if err := f.reconcile().Execute(ctx, nodeA); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	want := fmt.Sprintf("%s:%s:%s", shardID, nodeA.NodeID, vo.ReleaseFailedPrepare)
	if len(f.chain.releases) != 1 || string(f.chain.releases[0]) != want {
		t.Fatalf("got %v, want the reservation released as %s", f.chain.releases, want)
	}
}

func TestReconcileKeepsANodeThatIsStillWithinThePrepareDeadline(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	f.control.stuck = true

	for range 3 {
		if err := f.reconcile().Execute(ctx, nodeA); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		f.clock.Advance(f.patience / 4)
	}

	if len(f.chain.releases) != 0 {
		t.Fatalf("got %v, want a draining node left alone until its deadline", f.chain.releases)
	}
}

func TestReconcileCleansTheOldShardBeforeServingANewReservation(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	next := activeShard()
	next.ID = shardID + 1
	f.chain.shards[next.ID] = next
	f.chain.reservations[nodeA] = next.ID

	if err := f.reconcile().Execute(ctx, nodeA); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if calls := f.rec.sequence(); !reflect.DeepEqual(calls, []string{"mesh.remove", "mesh_store.forget"}) {
		t.Fatalf("the previous shard must be cleaned up first, got %v", calls)
	}
}
