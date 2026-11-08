package store

import (
	"context"
	"slices"
	"testing"
)

// Test flow:
//  1. Open a store, add three suspicious hosts, then remove one.
//  2. Close and reopen the store from the same directory.
//  3. Assert `ListSuspiciousHosts` returns only the two hosts still pinned, sorted.
func TestSuspiciousHostsSurviveAReopen(t *testing.T) {
	storageDir := t.TempDir()
	first, err := Open(storageDir)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	ctx := context.Background()
	for _, participantKey := range []string{"validator-b", "validator-a", "validator-c"} {
		if err := first.AddSuspiciousHost(ctx, participantKey); err != nil {
			t.Fatalf("AddSuspiciousHost(%q): %v", participantKey, err)
		}
	}
	if err := first.RemoveSuspiciousHost(ctx, "validator-c"); err != nil {
		t.Fatalf("RemoveSuspiciousHost(): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	second, err := Open(storageDir)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})

	pinned, err := second.ListSuspiciousHosts(ctx)
	if err != nil {
		t.Fatalf("ListSuspiciousHosts(): %v", err)
	}
	if want := []string{"validator-a", "validator-b"}; !slices.Equal(pinned, want) {
		t.Fatalf("ListSuspiciousHosts() = %v, want %v", pinned, want)
	}
}

// Test flow:
//  1. Open a test store.
//  2. Add the same host twice and remove a host that was never pinned.
//  3. Assert `ListSuspiciousHosts` returns the host once, unaffected by the repeat or the unknown removal.
func TestSuspiciousHostsToleratesRepeatedPinsAndUnknownUnpins(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	if err := testStore.AddSuspiciousHost(ctx, "validator-a"); err != nil {
		t.Fatalf("first AddSuspiciousHost(): %v", err)
	}
	if err := testStore.AddSuspiciousHost(ctx, "validator-a"); err != nil {
		t.Fatalf("repeated AddSuspiciousHost(): %v", err)
	}
	if err := testStore.RemoveSuspiciousHost(ctx, "never-pinned"); err != nil {
		t.Fatalf("RemoveSuspiciousHost() for an unpinned host: %v", err)
	}

	pinned, err := testStore.ListSuspiciousHosts(ctx)
	if err != nil {
		t.Fatalf("ListSuspiciousHosts(): %v", err)
	}
	if want := []string{"validator-a"}; !slices.Equal(pinned, want) {
		t.Fatalf("ListSuspiciousHosts() = %v, want %v", pinned, want)
	}
}
