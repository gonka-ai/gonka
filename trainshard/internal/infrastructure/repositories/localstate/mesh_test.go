package localstate_test

import (
	"context"
	"errors"
	"testing"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/repositories/localstate"
)

const held = vo.ShardID(7)

func openStore(t *testing.T) *localstate.Store {
	t.Helper()

	store, err := localstate.New(t.TempDir())
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	return store
}

func reserved(t *testing.T, shardID vo.ShardID) *localstate.Store {
	t.Helper()

	store := openStore(t)
	if err := run.RecordReservation(context.Background(), store.Runs(), node, shardID, now); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	return store
}

func peers(shardID vo.ShardID) mesh.Config {
	return mesh.Config{Shard: shardID, Peers: []mesh.Peer{
		{Rank: 0, Node: node, Address: "198.51.100.7:51820", PublicKey: "key-a"},
		{Rank: 1, Node: vo.NodeRef{Participant: "gonka1host", NodeID: "node-2"}, Address: "198.51.100.8:51820", PublicKey: "key-b"},
	}}
}

func TestAPeerListIsReadBackForTheShardItWasStoredFor(t *testing.T) {
	// arrange
	ctx := context.Background()
	meshes := reserved(t, held).Mesh()

	// act
	if err := meshes.SaveConfig(ctx, held, node, peers(held)); err != nil {
		t.Fatal(err)
	}
	config, found, err := meshes.Config(ctx, held, node)

	// assert
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(config.Peers) != 2 {
		t.Fatalf("found = %v, config = %+v", found, config)
	}
	if config.Peers[1].PublicKey != "key-b" {
		t.Fatalf("peers = %+v", config.Peers)
	}
}

// A write dropped on the floor while the caller is told it succeeded is the worst of both:
// apply mesh answers ok and the run then waits on a peer list nothing will ever read back
func TestAPeerListIsRefusedForAShardThisNodeIsNotHeldFor(t *testing.T) {
	// arrange
	ctx := context.Background()
	meshes := reserved(t, held).Mesh()

	// act
	err := meshes.SaveConfig(ctx, vo.ShardID(9), node, peers(9))

	// assert
	if !errors.Is(err, shard.ErrNodeNotReserved) {
		t.Fatalf("err = %v, want the node to be refused as not reserved", err)
	}
	if _, found, err := meshes.Config(ctx, vo.ShardID(9), node); err != nil || found {
		t.Fatalf("found = %v, err = %v, want nothing stored for the other shard", found, err)
	}
}

func TestAPeerListIsRefusedForANodeWithNoReservation(t *testing.T) {
	// arrange
	ctx := context.Background()
	meshes := openStore(t).Mesh()

	// act
	err := meshes.SaveConfig(ctx, held, node, peers(held))

	// assert
	if !errors.Is(err, shard.ErrNodeNotReserved) {
		t.Fatalf("err = %v, want the node to be refused as not reserved", err)
	}
}

func TestAnIdentityIsRefusedForAShardThisNodeIsNotHeldFor(t *testing.T) {
	// arrange
	ctx := context.Background()
	meshes := reserved(t, held).Mesh()
	identity := mesh.Identity{
		Member:    mesh.Member{Node: node, Address: "198.51.100.7:51820", PublicKey: "key-a"},
		Signature: []byte("signed"),
	}

	// act
	err := meshes.SaveIdentity(ctx, vo.ShardID(9), node, identity)

	// assert
	if !errors.Is(err, shard.ErrNodeNotReserved) {
		t.Fatalf("err = %v, want the node to be refused as not reserved", err)
	}
}

// Sweep removes what a shard this node no longer serves left behind, and asks by that shard's id
func TestForgettingAShardThisNodeNoLongerServesIsNotAFailure(t *testing.T) {
	// arrange
	ctx := context.Background()
	meshes := reserved(t, held).Mesh()
	if err := meshes.SaveConfig(ctx, held, node, peers(held)); err != nil {
		t.Fatal(err)
	}

	// act
	err := meshes.Forget(ctx, vo.ShardID(9), node)

	// assert
	if err != nil {
		t.Fatalf("forgetting a shard that is not here must be a no-op: %v", err)
	}
	if _, found, err := meshes.Config(ctx, held, node); err != nil || !found {
		t.Fatalf("found = %v, err = %v, want the served shard's mesh left alone", found, err)
	}
}

func TestForgettingTheShardThisNodeServesClearsIt(t *testing.T) {
	// arrange
	ctx := context.Background()
	meshes := reserved(t, held).Mesh()
	if err := meshes.SaveConfig(ctx, held, node, peers(held)); err != nil {
		t.Fatal(err)
	}

	// act
	if err := meshes.Forget(ctx, held, node); err != nil {
		t.Fatal(err)
	}

	// assert
	if _, found, err := meshes.Config(ctx, held, node); err != nil || found {
		t.Fatalf("found = %v, err = %v, want the peer list gone", found, err)
	}
}
