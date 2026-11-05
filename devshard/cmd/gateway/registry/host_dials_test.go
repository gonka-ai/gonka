package registry

import (
	"reflect"
	"testing"
)

// Test flow:
//  1. Build a registry with two escrows: one session dialing hostA's address twice plus hostB, another session dialing hostB again.
//  2. Add both escrows to the registry.
//  3. Assert `HostDials` reports each address exactly once, for hostA and hostB.
func TestHostDialsNameEachAddressOnce(t *testing.T) {
	t.Parallel()
	first := newFakeSession("hostA", "hostA", "hostB")
	first.dials = []HostDial{
		{ParticipantKey: "hostA", BaseURL: "http://a:8080", RoutePrefix: "/api"},
		{ParticipantKey: "hostA", BaseURL: "http://a:8080", RoutePrefix: "/api"},
		{ParticipantKey: "hostB", BaseURL: "http://b:8080", RoutePrefix: "/api"},
	}
	second := newFakeSession("hostB")
	second.dials = []HostDial{{ParticipantKey: "hostB", BaseURL: "http://b:8080", RoutePrefix: "/api"}}
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": first, "2": second}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	mustAdd(t, registry, "2", "qwen")

	want := []HostDial{
		{ParticipantKey: "hostA", BaseURL: "http://a:8080", RoutePrefix: "/api"},
		{ParticipantKey: "hostB", BaseURL: "http://b:8080", RoutePrefix: "/api"},
	}
	if got := registry.HostDials(); !reflect.DeepEqual(got, want) {
		t.Fatalf("HostDials() = %+v, want %+v", got, want)
	}
}

// Test flow:
//  1. Build a registry with one escrow for hostA and add it.
//  2. Retire that escrow.
//  3. Assert `HostDials` reports no addresses.
func TestARetiredEscrowsHostsAreNotPinged(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	session.dials = []HostDial{{ParticipantKey: "hostA", BaseURL: "http://a:8080"}}
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire() = %v", err)
	}

	if got := registry.HostDials(); len(got) != 0 {
		t.Fatalf("HostDials() = %+v, want none", got)
	}
}

// Test flow:
//  1. Build a registry with one escrow whose dial has a blank BaseURL and add it.
//  2. Assert `HostDials` reports no addresses.
func TestAHostWithNoAddressIsNoTarget(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA")
	session.dials = []HostDial{{ParticipantKey: "hostA", BaseURL: ""}}
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": session}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")

	if got := registry.HostDials(); len(got) != 0 {
		t.Fatalf("HostDials() = %+v, want none", got)
	}
}

// Test flow:
//  1. Add two escrows, then take a hold on one and retire it, so it drains instead of closing.
//  2. Share the chain's max_nonce with the registry.
//  3. Assert the published and the draining session both hold it: a draining session still votes timeouts, which must respect the hosts' cap too.
func TestShareChainMaxNonceReachesPublishedAndDrainingSessions(t *testing.T) {
	t.Parallel()
	published, draining := newFakeSession("hostA"), newFakeSession("hostB")
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": published, "2": draining}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	mustAdd(t, registry, "2", "qwen")
	_, release, acquired := registry.Acquire("2")
	if !acquired {
		t.Fatalf("Acquire(2) acquired = false, want true")
	}
	t.Cleanup(release)
	if err := registry.Retire("2"); err != nil {
		t.Fatalf("Retire(2) = %v", err)
	}

	registry.ShareChainMaxNonce(1_700)

	if got := published.chainMaxNonce.Load(); got != 1_700 {
		t.Fatalf("published session max_nonce = %d, want 1700", got)
	}
	if got := draining.chainMaxNonce.Load(); got != 1_700 {
		t.Fatalf("draining session max_nonce = %d, want 1700", got)
	}
}

// Test flow:
//  1. Add an escrow, take the hold an assignment takes when its nonce commits, and retire the escrow so it drains.
//  2. Resolve the escrow the way dispatch does for that committed nonce.
//  3. Assert routing no longer finds it but dispatch still does: a nonce committed before the retirement owes its send and its vote.
func TestACommittedNonceStillReachesItsEscrowAfterItRetires(t *testing.T) {
	t.Parallel()
	registry := New(Deps{
		ServingSessions: newSessions(map[string]*fakeSession{"1": newFakeSession("hostA")}).open,
		Now:             fixedClock(),
	})
	mustAdd(t, registry, "1", "qwen")
	_, assignmentHold, acquired := registry.Acquire("1")
	if !acquired {
		t.Fatalf("Acquire(1) acquired = false, want true")
	}
	t.Cleanup(assignmentHold)
	if err := registry.Retire("1"); err != nil {
		t.Fatalf("Retire(1) = %v", err)
	}

	_, routingRelease, routed := registry.Acquire("1")
	session, release, committed := registry.AcquireCommitted("1")

	if routed {
		routingRelease()
		t.Fatal("Acquire(1) found a retired escrow, want routing to refuse it")
	}
	if !committed || session == nil {
		t.Fatal("AcquireCommitted(1) = not found, want the draining escrow its committed nonce owes a send to")
	}
	release()
}
