package app

import (
	"context"
	"testing"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
)

type staleRepublishPass struct {
	listing *registry.Listing
	rows    []store.DevshardRecord
}

func registryWithRacingPass(t *testing.T, records *store.Store) (*registry.Registry, *staleRepublishPass) {
	t.Helper()
	pass := &staleRepublishPass{}
	var escrows *registry.Registry
	escrows = registry.New(registry.Deps{
		ServingSessions: func(context.Context, string) (registry.EscrowSession, error) {
			return weightlessSession{participants: []string{"validator-a"}}, nil
		},
		Now: fixedClock,
		Retiring: func(string, registry.EscrowSession) {
			if pass.listing != nil {
				return
			}
			pass.listing = escrows.Listing()
			rows, err := records.ListDevshards(context.Background())
			if err != nil {
				t.Errorf("ListDevshards(): %v", err)
			}
			pass.rows = rows
		},
	})
	t.Cleanup(func() { escrows.Close() })
	return escrows, pass
}

func (p *staleRepublishPass) publish(t *testing.T, escrows *registry.Registry) {
	t.Helper()
	if p.listing == nil {
		t.Fatal("the retire never closed the session")
	}
	defer p.listing.Close()
	add := func(ctx context.Context, record store.DevshardRecord) error {
		return addServing(ctx, p.listing, record)
	}
	ignore := func(string, error) {}
	keep := func(string, error) error { return nil }
	if _, err := publishEscrows(context.Background(), p.rows, 1, add, escrows.Retire, keep, ignore, ignore); err != nil {
		t.Fatalf("publishEscrows(): %v", err)
	}
}

// Test flow:
//  1. Register escrow 42 active; a republish pass takes its listing and reads the rows while the inactive registration's retire closes the session.
//  2. Register escrow 42 inactive, then let that pass publish the rows it read.
//  3. Assert escrow 42 does not route under its inactive row.
func TestAnInactiveRegistrationRacingARepublishPassLeavesTheEscrowUnrouted(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	escrows, pass := registryWithRacingPass(t, records)
	operator := &operations{store: records, escrows: escrows, storageDir: t.TempDir()}
	if err := operator.AddDevshard(ctx, api.AddDevshardRequest{EscrowID: "42", Model: "model-a", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Activate: true}); err != nil {
		t.Fatalf("AddDevshard(active) = %v", err)
	}

	if err := operator.AddDevshard(ctx, api.AddDevshardRequest{EscrowID: "42", Model: "model-a", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY"}); err != nil {
		t.Fatalf("AddDevshard(inactive) = %v", err)
	}
	pass.publish(t, escrows)

	if storedDevshard(t, records, "42").Active {
		t.Fatal("escrow 42's row is active after an inactive registration")
	}
	if _, routable := escrows.Routable("42"); routable {
		t.Error("escrow 42 routes under an inactive row after the pass that read it active")
	}
}

// Test flow:
//  1. Register escrow 42 active; a republish pass takes its listing and reads the rows while the deactivation's retire closes the session.
//  2. Deactivate escrow 42, then let that pass publish the rows it read.
//  3. Assert escrow 42 does not route under its inactive row.
func TestADeactivationRacingARepublishPassLeavesTheEscrowUnrouted(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	escrows, pass := registryWithRacingPass(t, records)
	operator := &operations{store: records, escrows: escrows, storageDir: t.TempDir()}
	if err := operator.AddDevshard(ctx, api.AddDevshardRequest{EscrowID: "42", Model: "model-a", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Activate: true}); err != nil {
		t.Fatalf("AddDevshard(active) = %v", err)
	}

	if err := operator.Deactivate(ctx, "42"); err != nil {
		t.Fatalf("Deactivate = %v", err)
	}
	pass.publish(t, escrows)

	if storedDevshard(t, records, "42").Active {
		t.Fatal("escrow 42's row is active after a deactivation")
	}
	if _, routable := escrows.Routable("42"); routable {
		t.Error("escrow 42 routes under an inactive row after the pass that read it active")
	}
}
