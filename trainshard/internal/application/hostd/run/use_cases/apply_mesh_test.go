package usecases_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	usecases "trainshard/internal/application/hostd/run/use_cases"
	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

func meshConfig(t *testing.T, nodes ...vo.NodeRef) mesh.Config {
	t.Helper()
	members := make([]mesh.Member, 0, len(nodes))
	for i, node := range nodes {
		members = append(members, mesh.Member{Node: node, Address: "10.0.0." + string(rune('1'+i)), PublicKey: "public-key"})
	}
	config, err := mesh.Order(shardID, members)
	if err != nil {
		t.Fatalf("order mesh: %v", err)
	}
	return config
}

func meshCommand(t *testing.T, nodes ...vo.NodeRef) usecases.MeshCommand {
	return usecases.MeshCommand{NodesCommand: nodesCommand(), Config: meshConfig(t, nodes...)}
}

func TestApplyMeshRejectsAPeerOutsideTheShard(t *testing.T) {

	f := newFixture()

	_, err := f.applyMesh().Execute(context.Background(), meshCommand(t, nodeA, nodeB))

	if !errors.Is(err, shard.ErrNodeNotReserved) {
		t.Fatalf("got %v, want %v", err, shard.ErrNodeNotReserved)
	}
	if len(f.store.configs) != 0 {
		t.Fatalf("nothing may be stored from a rejected peer list, got %v", f.store.configs)
	}
}

func TestApplyMeshRefusesANodeMissingFromThePeerList(t *testing.T) {

	f := newFixture()
	record := activeShard()
	record.Nodes = append(record.Nodes, shard.ReservedNode{Ref: nodeB, ModelID: "model-1"})
	f.chain.shards[shardID] = record
	cmd := meshCommand(t, nodeB)
	cmd.Nodes = []vo.NodeRef{nodeA}

	results, err := f.applyMesh().Execute(context.Background(), cmd)

	if err != nil {
		t.Fatalf("a per-node refusal must not fail the request: %v", err)
	}
	if len(results) != 1 || results[0].Fault == nil || results[0].Fault.Code != "MESH_NODE_NOT_IN_MESH" {
		t.Fatalf("got %+v, want a single MESH_NODE_NOT_IN_MESH failure", results)
	}
}

func TestApplyMeshRefusesANodeThatIsStillServingInference(t *testing.T) {

	f := newFixture()

	results, err := f.applyMesh().Execute(context.Background(), meshCommand(t, nodeA))

	if err != nil {
		t.Fatalf("a per-node refusal must not fail the request: %v", err)
	}
	if len(results) != 1 || results[0].Fault == nil || results[0].Fault.Code != "NODE_NOT_PREPARED" {
		t.Fatalf("got %+v, want a single NODE_NOT_PREPARED failure", results)
	}
}

func TestApplyMeshBringsTheInterfaceUpBeforeItAnswers(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	results, err := f.applyMesh().Execute(ctx, meshCommand(t, nodeA))
	if err != nil {
		t.Fatalf("apply mesh: %v", err)
	}

	if len(results) != 1 || !results[0].OK() {
		t.Fatalf("got %+v, want one accepted node", results)
	}
	if !f.network.up {
		t.Fatal("the peer list must be applied within the request, not left to the next tick")
	}
}

func TestApplyMeshTakesTheListWhenOnlyTheRunIsRefused(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	foreign := vo.ImageDigest("foreign@sha256:" + strings.Repeat("c", 64))
	f.images.layers[foreign] = vo.ImageLayers{"someone-elses-layer"}
	cmd := deployCommand()
	cmd.Run.Image = foreign
	f.images.pullErr = context.DeadlineExceeded
	if _, err := f.deploy().Execute(ctx, cmd); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	f.images.pullErr = nil

	results, err := f.applyMesh().Execute(ctx, meshCommand(t, nodeA))

	if err != nil || len(results) != 1 || !results[0].OK() {
		t.Fatalf("got %+v %v, want the peer list taken: a refused image is not a node that failed its mesh", results, err)
	}
	if !f.network.up {
		t.Fatal("the interface must be up for the tenant's next deploy")
	}
	if state := f.runs.states[nodeA]; state.Fault == nil || state.Fault.Code != "IMAGE_NOT_DERIVED" {
		t.Fatalf("got %+v, want the refusal left for status to show", state)
	}
}

func TestApplyMeshRebuildsAContainerWhosePlaceOnTheMeshMoved(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	record := activeShard()
	record.Nodes = append(record.Nodes, shard.ReservedNode{Ref: nodeB, ModelID: "model-1"})
	f.chain.shards[shardID] = record
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.applyMesh().Execute(ctx, meshCommand(t, nodeA, nodeB)); err != nil {
		t.Fatalf("apply mesh: %v", err)
	}
	revision := f.runs.states[nodeA].Revision
	f.rec.reset()

	smaller := meshCommand(t, nodeA)
	smaller.RequestID = "req-2"
	results, err := f.applyMesh().Execute(ctx, smaller)

	if err != nil || len(results) != 1 || !results[0].OK() {
		t.Fatalf("got %+v %v, want the smaller list accepted", results, err)
	}
	if f.runs.states[nodeA].Revision != revision+1 {
		t.Fatalf("got revision %d, want %d: the container carries its place on the mesh", f.runs.states[nodeA].Revision, revision+1)
	}
	if !slices.Contains(f.rec.sequence(), "mesh.apply") || len(f.network.applied) != 1 {
		t.Fatalf("got %v with peers %v, want the interface brought to the new list", f.rec.sequence(), f.network.applied)
	}
}

func TestApplyMeshLeavesTheContainerAloneWhenTheListIsTheSame(t *testing.T) {

	f := newFixture()
	ctx := context.Background()
	if err := f.prepared(ctx); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.applyMesh().Execute(ctx, meshCommand(t, nodeA)); err != nil {
		t.Fatalf("apply mesh: %v", err)
	}
	revision := f.runs.states[nodeA].Revision
	f.rec.reset()

	again := meshCommand(t, nodeA)
	again.RequestID = "req-2"
	if _, err := f.applyMesh().Execute(ctx, again); err != nil {
		t.Fatalf("apply mesh: %v", err)
	}

	if f.runs.states[nodeA].Revision != revision {
		t.Fatalf("got revision %d, want %d unchanged", f.runs.states[nodeA].Revision, revision)
	}
	if slices.Contains(f.rec.sequence(), "mesh.apply") {
		t.Fatalf("got %v, want an interface already holding the list left as it is", f.rec.sequence())
	}
}
