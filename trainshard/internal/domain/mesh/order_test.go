package mesh_test

import (
	"errors"
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

func TestOrderIsTheSameWhateverTheInputOrder(t *testing.T) {

	forward := []mesh.Member{member(nodeA, "10.0.0.1"), member(nodeB, "10.0.0.2"), member(nodeC, "10.0.0.3")}
	shuffled := []mesh.Member{member(nodeC, "10.0.0.3"), member(nodeA, "10.0.0.1"), member(nodeB, "10.0.0.2")}

	first, err := mesh.Order(7, forward)
	if err != nil {
		t.Fatalf("order forward: %v", err)
	}
	second, err := mesh.Order(7, shuffled)
	if err != nil {
		t.Fatalf("order shuffled: %v", err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ranks differ between input orders: %v then %v", first, second)
	}
	master, ok := first.Master()
	if !ok || master.Node != nodeA {
		t.Fatalf("rank 0 must be the lowest node, got %v ok=%v", master.Node, ok)
	}
}

func TestOrderRejectsUnusableMembers(t *testing.T) {
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
			name:    "member without an address",
			members: []mesh.Member{{Node: nodeA, PublicKey: "key"}},
			wantErr: mesh.ErrIncompleteMember,
		},
		{
			name:    "member without a public key",
			members: []mesh.Member{{Node: nodeA, Address: "10.0.0.1"}},
			wantErr: mesh.ErrIncompleteMember,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {

			_, err := mesh.Order(7, tc.members)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestPeersForExcludesTheNodeItself(t *testing.T) {

	cfg, err := mesh.Order(7, []mesh.Member{member(nodeA, "10.0.0.1"), member(nodeB, "10.0.0.2")})
	if err != nil {
		t.Fatalf("order: %v", err)
	}

	peers := cfg.PeersFor(nodeA)

	if len(peers) != 1 || peers[0].Node != nodeB {
		t.Fatalf("got %v, want only %v", peers, nodeB)
	}
}
