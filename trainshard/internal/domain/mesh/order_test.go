package mesh_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/vo"
)

var (
	nodeA = vo.NodeRef{Participant: "gonka1aaa", NodeID: "node-1"}
	nodeB = vo.NodeRef{Participant: "gonka1bbb", NodeID: "node-1"}
	nodeC = vo.NodeRef{Participant: "gonka1bbb", NodeID: "node-2"}
)

func member(node vo.NodeRef, address string) mesh.Member {
	return mesh.Member{Node: node, Address: address, PublicKey: "key-" + address}
}

func members(n int) []mesh.Member {
	all := make([]mesh.Member, 0, n)
	for i := range n {
		node := vo.NodeRef{Participant: "gonka1aaa", NodeID: vo.NodeID(fmt.Sprintf("node-%03d", i))}
		all = append(all, member(node, fmt.Sprintf("198.51.100.%d:%d", i%256, 51820+i)))
	}
	return all
}

func TestOrderIsTheSameWhateverTheInputOrder(t *testing.T) {
	// arrange
	forward := []mesh.Member{member(nodeA, "10.0.0.1"), member(nodeB, "10.0.0.2"), member(nodeC, "10.0.0.3")}
	shuffled := []mesh.Member{member(nodeC, "10.0.0.3"), member(nodeA, "10.0.0.1"), member(nodeB, "10.0.0.2")}

	// act
	first, err := mesh.Order(7, forward)
	if err != nil {
		t.Fatalf("order forward: %v", err)
	}
	second, err := mesh.Order(7, shuffled)
	if err != nil {
		t.Fatalf("order shuffled: %v", err)
	}

	// assert
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ranks differ between input orders: %v then %v", first, second)
	}
	master, ok := first.Master()
	if !ok || master.Node != nodeA {
		t.Fatalf("rank 0 must be the lowest node, got %v ok=%v", master.Node, ok)
	}
}

func TestOrderRejectsUnusableMembers(t *testing.T) {
	// arrange
	cases := []struct {
		name    string
		members []mesh.Member
		wantErr error
	}{
		{
			name:    "no members",
			wantErr: mesh.ErrNoMembers,
		},
		{
			name:    "the same node twice",
			members: []mesh.Member{member(nodeA, "10.0.0.1"), member(nodeA, "10.0.0.9")},
			wantErr: mesh.ErrDuplicateNode,
		},
		{
			name: "two nodes publishing the same public key",
			members: []mesh.Member{
				{Node: nodeA, Address: "10.0.0.1", PublicKey: "victim-key"},
				{Node: nodeB, Address: "10.0.0.2", PublicKey: "victim-key"},
			},
			wantErr: mesh.ErrDuplicateKey,
		},
		{
			name:    "member without an address",
			members: []mesh.Member{{Node: nodeA, PublicKey: "key"}},
			wantErr: mesh.ErrIncompleteMember,
		},
		{
			name:    "member without a public key",
			members: []mesh.Member{{Node: nodeA, Address: "10.0.0.1"}},
			wantErr: mesh.ErrIncompleteMember,
		},
		{
			name:    "more members than the mesh has addresses",
			members: members(255),
			wantErr: mesh.ErrRankOffMesh,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			_, err := mesh.Order(7, tc.members)

			// assert
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestOrderGivesEveryRankAnAddressUpToTheLastOne(t *testing.T) {
	// arrange
	full := members(254)

	// act
	cfg, err := mesh.Order(7, full)

	// assert
	if err != nil {
		t.Fatalf("a mesh with an address for every rank must be ranked: %v", err)
	}
	for _, peer := range cfg.Peers {
		if _, err := mesh.Address(cfg.Shard, peer.Rank); err != nil {
			t.Fatalf("rank %d has no address: %v", peer.Rank, err)
		}
	}
}

func TestPeersForExcludesTheNodeItself(t *testing.T) {
	// arrange
	cfg, err := mesh.Order(7, []mesh.Member{member(nodeA, "10.0.0.1"), member(nodeB, "10.0.0.2")})
	if err != nil {
		t.Fatalf("order: %v", err)
	}

	// act
	peers := cfg.PeersFor(nodeA)

	// assert
	if len(peers) != 1 || peers[0].Node != nodeB {
		t.Fatalf("got %v, want only %v", peers, nodeB)
	}
}
