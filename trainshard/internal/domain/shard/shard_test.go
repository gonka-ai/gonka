package shard_test

import (
	"testing"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

func TestHostsGroupTheNodesByTheMachineThatServesThem(t *testing.T) {
	alice, bob := vo.Participant("gonka1alice"), vo.Participant("gonka1bob")
	a1 := vo.NodeRef{Participant: alice, NodeID: "node-1"}
	a2 := vo.NodeRef{Participant: alice, NodeID: "node-2"}
	a3 := vo.NodeRef{Participant: alice, NodeID: "node-3"}
	b1 := vo.NodeRef{Participant: bob, NodeID: "node-1"}

	cases := []struct {
		name  string
		nodes []shard.ReservedNode
		want  []vo.Host
	}{
		{
			name:  "one participant behind one address is one host",
			nodes: []shard.ReservedNode{{Ref: a1}, {Ref: a2}},
			want:  []vo.Host{{Participant: alice, Nodes: []vo.NodeRef{a1, a2}}},
		},
		{
			name:  "nodes of one participant behind different endpoints are different hosts",
			nodes: []shard.ReservedNode{{Ref: a1, Endpoint: "https://a.example"}, {Ref: a2, Endpoint: "https://b.example"}, {Ref: a3, Endpoint: "https://a.example"}},
			want: []vo.Host{
				{Participant: alice, Endpoint: "https://a.example", Nodes: []vo.NodeRef{a1, a3}},
				{Participant: alice, Endpoint: "https://b.example", Nodes: []vo.NodeRef{a2}},
			},
		},
		{
			name:  "hosts come in the order their first node was named",
			nodes: []shard.ReservedNode{{Ref: b1}, {Ref: a1}, {Ref: a2}},
			want: []vo.Host{
				{Participant: bob, Nodes: []vo.NodeRef{b1}},
				{Participant: alice, Nodes: []vo.NodeRef{a1, a2}},
			},
		},
		{
			name:  "a node with no endpoint is not the host that published one",
			nodes: []shard.ReservedNode{{Ref: a1, Endpoint: "https://a.example"}, {Ref: a2}},
			want: []vo.Host{
				{Participant: alice, Endpoint: "https://a.example", Nodes: []vo.NodeRef{a1}},
				{Participant: alice, Nodes: []vo.NodeRef{a2}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// act
			got := shard.Shard{Nodes: tc.nodes}.Hosts()

			// assert
			if len(got) != len(tc.want) {
				t.Fatalf("got %d hosts %+v, want %d", len(got), got, len(tc.want))
			}
			for i := range tc.want {
				if got[i].Participant != tc.want[i].Participant || got[i].Endpoint != tc.want[i].Endpoint {
					t.Fatalf("host %d: got %+v, want %+v", i, got[i], tc.want[i])
				}
				if len(got[i].Nodes) != len(tc.want[i].Nodes) {
					t.Fatalf("host %d: got nodes %v, want %v", i, got[i].Nodes, tc.want[i].Nodes)
				}
				for j := range tc.want[i].Nodes {
					if got[i].Nodes[j] != tc.want[i].Nodes[j] {
						t.Fatalf("host %d: got nodes %v, want %v", i, got[i].Nodes, tc.want[i].Nodes)
					}
				}
			}
		})
	}
}

func TestUnaddressedNamesTheNodesNoEndpointReaches(t *testing.T) {
	alice := vo.Participant("gonka1alice")
	a1 := vo.NodeRef{Participant: alice, NodeID: "node-1"}
	a2 := vo.NodeRef{Participant: alice, NodeID: "node-2"}
	a3 := vo.NodeRef{Participant: alice, NodeID: "node-3"}
	record := shard.Shard{Nodes: []shard.ReservedNode{{Ref: a1, Endpoint: "https://a.example"}, {Ref: a2}, {Ref: a3}}}

	// act
	got := record.Unaddressed()

	// assert
	if len(got) != 2 || got[0] != a2 || got[1] != a3 {
		t.Fatalf("got %v, want the two nodes that published no endpoint, in order", got)
	}
	if addressed := (shard.Shard{Nodes: []shard.ReservedNode{{Ref: a1, Endpoint: "https://a.example"}}}).Unaddressed(); len(addressed) != 0 {
		t.Fatalf("got %v, want nothing when every node has an address", addressed)
	}
}

func TestHostOfFindsTheMachineOfAReservedNodeOnly(t *testing.T) {
	alice := vo.Participant("gonka1alice")
	a1 := vo.NodeRef{Participant: alice, NodeID: "node-1"}
	a2 := vo.NodeRef{Participant: alice, NodeID: "node-2"}
	record := shard.Shard{Nodes: []shard.ReservedNode{{Ref: a1, Endpoint: "https://a.example"}}}

	// act
	host, found := record.HostOf(a1)
	_, foundStranger := record.HostOf(a2)

	// assert
	if !found || host.Endpoint != "https://a.example" {
		t.Fatalf("got %+v found=%v, want the node's own machine", host, found)
	}
	if foundStranger {
		t.Fatal("got a host for a node the shard does not reserve")
	}
}
