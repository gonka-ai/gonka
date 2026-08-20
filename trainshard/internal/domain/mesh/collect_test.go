package mesh_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/vo"
)

func TestCollectTakesOneMemberPerReservedNode(t *testing.T) {

	hosts := newHostsStub()

	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, shardID,
		[]vo.Participant{hostA, hostB}, []vo.NodeRef{nodeA, nodeB, nodeC})

	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(members) != 3 || len(missing) != 0 {
		t.Fatalf("got %d members and %v missing, want one per reserved node", len(members), missing)
	}
	for i, node := range []vo.NodeRef{nodeA, nodeB, nodeC} {
		if members[i].Node != node {
			t.Fatalf("got %s in place %d, want %s", members[i].Node, i, node)
		}
	}
}

func TestCollectRefusesAMemberAHostHasNoRightToOffer(t *testing.T) {
	cases := map[string]struct {
		arrange func(*hostsStub, *verifierStub)
		want    error
	}{
		"signed by another host": {
			arrange: func(h *hostsStub, _ *verifierStub) {
				stolen := identityOf(nodeA)
				stolen.Signature = []byte(hostB)
				h.identities[hostA] = []mesh.Identity{stolen}
			},
			want: mesh.ErrForeignIdentity,
		},
		"signature that does not verify": {
			arrange: func(_ *hostsStub, v *verifierStub) { v.err = errors.New("bad signature") },
			want:    mesh.ErrForeignIdentity,
		},
		"a member without an address": {
			arrange: func(h *hostsStub, _ *verifierStub) {
				incomplete := identityOf(nodeA)
				incomplete.Member.Address = ""
				h.identities[hostA] = []mesh.Identity{incomplete}
			},
			want: mesh.ErrIncompleteMember,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {

			hosts, verifier := newHostsStub(), &verifierStub{}
			tc.arrange(hosts, verifier)

			_, _, err := mesh.Collect(context.Background(), hosts, verifier, shardID,
				[]vo.Participant{hostA, hostB}, []vo.NodeRef{nodeA, nodeB, nodeC})

			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCollectReportsANodeThatHasNotPreparedYetAsMissing(t *testing.T) {

	hosts := newHostsStub()
	hosts.identities[hostA] = nil

	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, shardID,
		[]vo.Participant{hostA, hostB}, []vo.NodeRef{nodeA, nodeB, nodeC})

	if err != nil {
		t.Fatalf("a node that is not ready yet is not a failure: %v", err)
	}
	if len(members) != 2 || !slices.Contains(missing, nodeA) {
		t.Fatalf("got %d members and %v missing, want the rest of the mesh kept", len(members), missing)
	}
}

func TestCollectKeepsGoingWhenOneHostCannotBeAsked(t *testing.T) {

	hosts := newHostsStub()
	hosts.silent[hostB] = true

	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, shardID,
		[]vo.Participant{hostA, hostB}, []vo.NodeRef{nodeA, nodeB, nodeC})

	if err != nil {
		t.Fatalf("one silent host must not sink the mesh: %v", err)
	}
	if len(members) == 0 || len(missing) == 0 {
		t.Fatalf("got %d members and %v missing, want the silent host's nodes named", len(members), missing)
	}
}

func TestCollectStopsWhenNoHostAnswersAtAll(t *testing.T) {

	hosts := newHostsStub()
	hosts.silent[hostA], hosts.silent[hostB] = true, true

	_, _, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, shardID,
		[]vo.Participant{hostA, hostB}, []vo.NodeRef{nodeA, nodeB, nodeC})

	if !errors.Is(err, errHost) {
		t.Fatalf("got %v, want the failure reported as ours rather than every node's", err)
	}
}
