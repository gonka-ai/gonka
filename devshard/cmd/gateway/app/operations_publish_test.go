package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
)

func parkingRegistry(t *testing.T, records *store.Store) *registry.Registry {
	t.Helper()
	var escrows *registry.Registry
	escrows = registry.New(registry.Deps{
		ServingSessions: func(ctx context.Context, escrowID string) (registry.EscrowSession, error) {
			if err := records.ParkForSettlement(ctx, escrowID); err != nil {
				return nil, err
			}
			if err := escrows.Retire(escrowID); err != nil {
				return nil, err
			}
			return weightlessSession{participants: []string{"validator-a"}}, nil
		},
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	t.Cleanup(func() { escrows.Close() })
	return escrows
}

// Test flow:
//  1. Store an inactive unparked row for escrow 42 and a registry whose session open parks and retires the escrow.
//  2. Call operator.Activate for escrow 42.
//  3. Assert the row is parked and the escrow is not routable.
func TestAParkLandingWhileActivateOpensTheSessionKeepsTheEscrowUnroutable(t *testing.T) {
	ctx := t.Context()
	records := openedStore(t)
	if err := records.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "42", PrivateKeyEnv: "DEVSHARD_HELD_KEY", Model: "model-a"}); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	escrows := parkingRegistry(t, records)
	operator := &operations{store: records, escrows: escrows}

	if err := operator.Activate(ctx, "42"); err != nil {
		t.Fatalf("Activate(42) = %v, want nil", err)
	}

	if row := storedDevshard(t, records, "42"); !row.SettlementPending {
		t.Errorf("row = %+v, want it parked", row)
	}
	if _, routable := escrows.Routable("42"); routable {
		t.Error("Routable(42) = true after a park during its session open, want false")
	}
}

// Test flow:
//  1. Set the key variable and a registry whose session open parks and retires the escrow.
//  2. Import escrow 42 with Activate set.
//  3. Assert the row is parked and the escrow is not routable.
func TestAParkLandingWhileAnImportOpensTheSessionKeepsTheEscrowUnroutable(t *testing.T) {
	ctx := t.Context()
	t.Setenv("DEVSHARD_IMPORT_KEY", "0x01")
	records := openedStore(t)
	escrows := parkingRegistry(t, records)
	operator := &operations{store: records, escrows: escrows, storageDir: t.TempDir()}

	if err := operator.ImportDevshard(ctx, api.ImportDevshardRequest{EscrowID: "42", SourcePath: sessionSource(t), PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a", Activate: true}); err != nil {
		t.Fatalf("ImportDevshard(42) = %v, want nil", err)
	}

	if row := storedDevshard(t, records, "42"); !row.SettlementPending {
		t.Errorf("row = %+v, want it parked", row)
	}
	if _, routable := escrows.Routable("42"); routable {
		t.Error("Routable(42) = true after a park during its session open, want false")
	}
}

// Test flow:
//  1. Store an inactive row for escrow 42 and park it for settlement.
//  2. Call operator.Activate for escrow 42.
//  3. Assert it returns ErrDevshardNotActivatable.
//  4. Assert the row is still inactive and parked and the escrow is not routable.
func TestActivatingAParkedRowIsRefusedAndNotPublished(t *testing.T) {
	ctx := t.Context()
	records := openedStore(t)
	if err := records.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "42", PrivateKeyEnv: "DEVSHARD_HELD_KEY", Model: "model-a"}); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	if err := records.ParkForSettlement(ctx, "42"); err != nil {
		t.Fatalf("ParkForSettlement(): %v", err)
	}
	escrows := servingRegistry(t)
	operator := &operations{store: records, escrows: escrows}

	err := operator.Activate(ctx, "42")

	if !errors.Is(err, api.ErrDevshardNotActivatable) {
		t.Fatalf("Activate(parked) = %v, want ErrDevshardNotActivatable", err)
	}
	if row := storedDevshard(t, records, "42"); row.Active || !row.SettlementPending {
		t.Errorf("row = %+v, want it inactive and parked", row)
	}
	if _, routable := escrows.Routable("42"); routable {
		t.Error("Routable(42) = true for a parked escrow, want false")
	}
}

// Test flow:
//  1. Open an empty store.
//  2. Call operator.Activate for escrow 42, which has no row.
//  3. Assert it returns ErrUnknownDevshard.
func TestActivatingAnEscrowWithoutARowReportsItUnknown(t *testing.T) {
	operator := &operations{store: openedStore(t), escrows: servingRegistry(t)}

	if err := operator.Activate(t.Context(), "42"); !errors.Is(err, api.ErrUnknownDevshard) {
		t.Fatalf("Activate(42) = %v, want ErrUnknownDevshard", err)
	}
}
