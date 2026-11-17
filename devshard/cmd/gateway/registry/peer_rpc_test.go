package registry

import (
	"slices"
	"testing"

	"devshard/transport"
)

type recordedAdoption struct {
	bound    map[string][]string
	released []string
}

func (r *recordedAdoption) BindEscrowHosts(escrowID string, peers []string) {
	r.bound[escrowID] = peers
}

func (r *recordedAdoption) ReleaseEscrow(escrowID string) { r.released = append(r.released, escrowID) }

// Test flow:
//  1. Publish an escrow whose session dials two hosts under one route prefix.
//  2. Retire it.
//  3. Assert the escrow was bound to each host under the identity its peer connection reports readiness by, and released once retired.
func TestAnEscrowIsBoundToItsHostsForPeerRPCAdoptionUntilItRetires(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA", "hostB")
	session.dials = []HostDial{
		{ParticipantKey: "hostA", BaseURL: "http://a:8080", RoutePrefix: "/v2"},
		{ParticipantKey: "hostB", BaseURL: "http://b:8080", RoutePrefix: "/v2"},
	}
	adoption := &recordedAdoption{bound: map[string][]string{}}
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		PeerRPC:         adoption,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")

	want := []string{transport.PeerChildID("hostA", "/v2"), transport.PeerChildID("hostB", "/v2")}
	if got := adoption.bound["1"]; !slices.Equal(got, want) {
		t.Fatalf("bound peers = %v, want %v", got, want)
	}
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire() = %v", err)
	}
	if !slices.Equal(adoption.released, []string{"1"}) {
		t.Fatalf("released escrows = %v, want [1]", adoption.released)
	}
}
