package mesh_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shared/vo"
)

func TestCollectTakesOneMemberPerReservedNode(t *testing.T) {
	// arrange
	hosts := newHostsStub()

	// act
	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
		machines, []vo.NodeRef{nodeA, nodeB, nodeC})

	// assert
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

func TestCollectTakesAMemberFromTheHostTheChainPlacesItOnOnly(t *testing.T) {
	// arrange
	hosts := newHostsStub()
	forged := identityOf(nodeA)
	forged.Member.Address = "10.9.9.9:51820"
	hosts.identities[hostB] = []mesh.Identity{identityOf(nodeB), identityOf(nodeC), forged}

	// act
	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
		machines, []vo.NodeRef{nodeA, nodeB, nodeC})

	// assert
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(members) != 3 || len(missing) != 0 {
		t.Fatalf("got %d members and %v missing, want one per reserved node", len(members), missing)
	}
	if members[0].Node != nodeA || members[0].Address == forged.Member.Address {
		t.Fatalf("got %+v for %s, want the member its own host offered, not the one another host did", members[0], nodeA)
	}
}

func TestCollectNamesANodeMissingWhenItsIdentityDoesNotVerify(t *testing.T) {
	// arrange
	offerForB := func(h *hostsStub, change func(*mesh.Identity)) {
		bad := identityOf(nodeB)
		change(&bad)
		h.identities[hostB] = []mesh.Identity{bad, identityOf(nodeC)}
	}
	cases := map[string]func(*hostsStub, *verifierStub){
		"signed by another host": func(h *hostsStub, _ *verifierStub) {
			offerForB(h, func(i *mesh.Identity) { i.Signature = []byte(hostA) })
		},
		"signed by a key the host never granted": func(h *hostsStub, _ *verifierStub) {
			offerForB(h, func(i *mesh.Identity) { i.Signature = []byte("gonka1warm") })
		},
		"signature that does not verify": func(h *hostsStub, v *verifierStub) {
			offerForB(h, func(i *mesh.Identity) { i.Signature = []byte("garbage") })
			v.rejected = map[string]bool{"garbage": true}
		},
		"a member without an address": func(h *hostsStub, _ *verifierStub) {
			offerForB(h, func(i *mesh.Identity) { i.Member.Address = "" })
		},
	}

	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			// arrange
			hosts, verifier := newHostsStub(), &verifierStub{}
			arrange(hosts, verifier)

			// act
			members, missing, err := mesh.Collect(context.Background(), hosts, verifier, &delegationStub{}, shardID,
				machines, []vo.NodeRef{nodeA, nodeB, nodeC})

			// assert
			if err != nil {
				t.Fatalf("one bad identity must not sink the mesh: %v", err)
			}
			if !slices.Equal(missing, []vo.NodeRef{nodeB}) {
				t.Fatalf("got %v missing, want only %s", missing, nodeB)
			}
			if len(members) != 2 || members[0].Node != nodeA || members[1].Node != nodeC {
				t.Fatalf("got %+v, want the other nodes of the host and of the mesh kept", members)
			}
		})
	}
}

func TestCollectChecksOnlyTheFirstIdentityAHostOffersForANode(t *testing.T) {
	// arrange
	hosts := newHostsStub()
	flood := make([]mesh.Identity, 0, 102)
	for i := range 101 {
		stranger := identityOf(nodeA)
		stranger.Signature = []byte(fmt.Sprintf("gonka1stranger%d", i))
		flood = append(flood, stranger)
	}
	hosts.identities[hostA] = append(flood, identityOf(nodeA))
	delegation := &delegationStub{}

	// act
	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, delegation, shardID,
		machines, []vo.NodeRef{nodeA, nodeB, nodeC})

	// assert
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !slices.Equal(missing, []vo.NodeRef{nodeA}) || len(members) != 2 {
		t.Fatalf("got %d members and %v missing, want the node judged by its first identity", len(members), missing)
	}
	if delegation.calls != 3 {
		t.Fatalf("got %d delegation lookups, want one per reserved node", delegation.calls)
	}
}

func TestCollectStopsWhenTheChainCannotSayWhoTheSignerIs(t *testing.T) {
	// arrange
	hosts := newHostsStub()
	warm := identityOf(nodeA)
	warm.Signature = []byte("gonka1warm")
	hosts.identities[hostA] = []mesh.Identity{warm}
	delegation := &delegationStub{err: errors.New("chain down")}

	// act
	_, _, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, delegation, shardID,
		machines, []vo.NodeRef{nodeA, nodeB, nodeC})

	// assert
	if !errors.Is(err, mesh.ErrDelegationUnknown) {
		t.Fatalf("got %v, want %v: a chain that cannot answer is no reason to drop the node", err, mesh.ErrDelegationUnknown)
	}
}

func TestCollectNamesBothNodesMissingWhenTheyOfferOneKey(t *testing.T) {
	// arrange
	hosts := newHostsStub()
	copied := identityOf(nodeC)
	copied.Member.PublicKey = identityOf(nodeA).Member.PublicKey
	hosts.identities[hostB] = []mesh.Identity{identityOf(nodeB), copied}

	// act
	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
		machines, []vo.NodeRef{nodeA, nodeB, nodeC})

	// assert
	if err != nil {
		t.Fatalf("a copied key must not sink the mesh: %v", err)
	}
	if !slices.Equal(missing, []vo.NodeRef{nodeA, nodeC}) {
		t.Fatalf("got %v missing, want both nodes that offer the key", missing)
	}
	if len(members) != 1 || members[0].Node != nodeB {
		t.Fatalf("got %+v, want only %s kept", members, nodeB)
	}
}

func TestCollectNamesBothNodesMissingWhenTheyOfferOneKeySpelledTwoWays(t *testing.T) {
	// arrange
	hosts := newHostsStub()
	original := identityOf(nodeA)
	original.Member.PublicKey = "LJbqQbbQnNjJmpaBNCiu3X/Ia1/8U2pPHRVCbzRTwy0="
	hosts.identities[hostA] = []mesh.Identity{original}
	aliased := identityOf(nodeC)
	aliased.Member.PublicKey = "LJbqQbbQnNjJmpaBNCiu3X/Ia1/8U2pPHRVCbzRTwy1="
	hosts.identities[hostB] = []mesh.Identity{identityOf(nodeB), aliased}

	// act
	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
		machines, []vo.NodeRef{nodeA, nodeB, nodeC})

	// assert
	if err != nil {
		t.Fatalf("an aliased key must not sink the mesh: %v", err)
	}
	if !slices.Equal(missing, []vo.NodeRef{nodeA, nodeC}) {
		t.Fatalf("got %v missing, want both nodes that offer the key", missing)
	}
	if len(members) != 1 || members[0].Node != nodeB {
		t.Fatalf("got %+v, want only %s kept", members, nodeB)
	}
}

func TestCollectNamesANodeMissingWhenItOffersNoWireguardKey(t *testing.T) {
	for name, key := range map[string]string{
		"not base64":   "victim-key",
		"not 32 bytes": "LJbqQbbQnNjJmpaBNCiu3X/Ia1/8U2pPHRVCbzRT",
		"33 bytes":     "LJbqQbbQnNjJmpaBNCiu3X/Ia1/8U2pPHRVCbzRTwy0A",
		"unpadded":     "LJbqQbbQnNjJmpaBNCiu3X/Ia1/8U2pPHRVCbzRTwy0",
	} {
		t.Run(name, func(t *testing.T) {
			// arrange
			hosts := newHostsStub()
			broken := identityOf(nodeC)
			broken.Member.PublicKey = key
			hosts.identities[hostB] = []mesh.Identity{identityOf(nodeB), broken}

			// act
			members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
				machines, []vo.NodeRef{nodeA, nodeB, nodeC})

			// assert
			if err != nil {
				t.Fatalf("one node's broken key must not sink the mesh: %v", err)
			}
			if !slices.Equal(missing, []vo.NodeRef{nodeC}) {
				t.Fatalf("got %v missing, want only %s", missing, nodeC)
			}
			if len(members) != 2 {
				t.Fatalf("got %+v, want %s and %s kept", members, nodeA, nodeB)
			}
		})
	}
}

func TestCollectTakesAMemberSignedByTheHostsWarmKey(t *testing.T) {
	// arrange
	hosts := newHostsStub()
	warm := identityOf(nodeA)
	warm.Signature = []byte("gonka1warm")
	hosts.identities[hostA] = []mesh.Identity{warm}
	delegation := &delegationStub{warm: map[vo.Address]vo.Participant{"gonka1warm": hostA}}

	// act
	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, delegation, shardID,
		machines, []vo.NodeRef{nodeA, nodeB, nodeC})

	// assert
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(members) != 3 || len(missing) != 0 {
		t.Fatalf("got %d members and %v missing, want the warm-signed node taken", len(members), missing)
	}
}

func TestCollectReportsANodeThatHasNotPreparedYetAsMissing(t *testing.T) {
	// arrange
	hosts := newHostsStub()
	hosts.identities[hostA] = nil

	// act
	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
		machines, []vo.NodeRef{nodeA, nodeB, nodeC})

	// assert
	if err != nil {
		t.Fatalf("a node that is not ready yet is not a failure: %v", err)
	}
	if len(members) != 2 || !slices.Contains(missing, nodeA) {
		t.Fatalf("got %d members and %v missing, want the rest of the mesh kept", len(members), missing)
	}
}

func TestCollectKeepsGoingWhenOneHostCannotBeAsked(t *testing.T) {
	// arrange
	hosts := newHostsStub()
	hosts.silent[hostB] = true

	// act
	members, missing, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
		machines, []vo.NodeRef{nodeA, nodeB, nodeC})

	// assert
	if err != nil {
		t.Fatalf("one silent host must not sink the mesh: %v", err)
	}
	if len(members) == 0 || len(missing) == 0 {
		t.Fatalf("got %d members and %v missing, want the silent host's nodes named", len(members), missing)
	}
}

func TestCollectStopsWhenNoHostAnswersAtAll(t *testing.T) {
	// arrange
	hosts := newHostsStub()
	hosts.silent[hostA], hosts.silent[hostB] = true, true

	// act
	_, _, err := mesh.Collect(context.Background(), hosts, &verifierStub{}, &delegationStub{}, shardID,
		machines, []vo.NodeRef{nodeA, nodeB, nodeC})

	// assert
	if !errors.Is(err, errHost) {
		t.Fatalf("got %v, want the failure reported as ours rather than every node's", err)
	}
}
