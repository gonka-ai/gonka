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

	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
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
		arrange func(*hostsStub, *verifierStub, *delegationStub)
		want    error
	}{
		"signed by another host": {
			arrange: func(h *hostsStub, _ *verifierStub, _ *delegationStub) {
				stolen := identityOf(nodeA)
				stolen.Signature = []byte(hostB)
				h.identities[hostA] = []mesh.Identity{stolen}
			},
			want: mesh.ErrForeignIdentity,
		},
		"signed by a key the host never granted": {
			arrange: func(h *hostsStub, _ *verifierStub, _ *delegationStub) {
				stranger := identityOf(nodeA)
				stranger.Signature = []byte("gonka1warm")
				h.identities[hostA] = []mesh.Identity{stranger}
			},
			want: mesh.ErrForeignIdentity,
		},
		"signature that does not verify": {
			arrange: func(_ *hostsStub, v *verifierStub, _ *delegationStub) { v.err = errors.New("bad signature") },
			want:    mesh.ErrForeignIdentity,
		},
		"chain cannot say who the signer is": {
			arrange: func(h *hostsStub, _ *verifierStub, d *delegationStub) {
				warm := identityOf(nodeA)
				warm.Signature = []byte("gonka1warm")
				h.identities[hostA] = []mesh.Identity{warm}
				d.err = errors.New("chain down")
			},
			want: mesh.ErrDelegationUnknown,
		},
		"a member without an address": {
			arrange: func(h *hostsStub, _ *verifierStub, _ *delegationStub) {
				incomplete := identityOf(nodeA)
				incomplete.Member.Address = ""
				h.identities[hostA] = []mesh.Identity{incomplete}
			},
			want: mesh.ErrIncompleteMember,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {

			hosts, verifier, delegation := newHostsStub(), &verifierStub{}, &delegationStub{}
			tc.arrange(hosts, verifier, delegation)

			_, _, err := mesh.Collect(context.Background(), hosts, verifier, delegation, shardID,
				[]vo.Participant{hostA, hostB}, []vo.NodeRef{nodeA, nodeB, nodeC})

			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCollectTakesAMemberSignedByTheHostsWarmKey(t *testing.T) {

	hosts := newHostsStub()
	warm := identityOf(nodeA)
	warm.Signature = []byte("gonka1warm")
	hosts.identities[hostA] = []mesh.Identity{warm}
	delegation := &delegationStub{warm: map[vo.Address]vo.Participant{"gonka1warm": hostA}}

	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, delegation, shardID,
		[]vo.Participant{hostA, hostB}, []vo.NodeRef{nodeA, nodeB, nodeC})

	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(members) != 3 || len(missing) != 0 {
		t.Fatalf("got %d members and %v missing, want the warm-signed node taken", len(members), missing)
	}
}

func TestCollectReportsANodeThatHasNotPreparedYetAsMissing(t *testing.T) {

	hosts := newHostsStub()
	hosts.identities[hostA] = nil

	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
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

	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
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

	_, _, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
		[]vo.Participant{hostA, hostB}, []vo.NodeRef{nodeA, nodeB, nodeC})

	if !errors.Is(err, errHost) {
		t.Fatalf("got %v, want the failure reported as ours rather than every node's", err)
	}
}
