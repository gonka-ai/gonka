package store

import (
	"context"
	"testing"
)

func storedRow(t *testing.T, registry *Store, escrowID string) DevshardRecord {
	t.Helper()
	records, err := registry.ListDevshards(context.Background())
	if err != nil {
		t.Fatalf("ListDevshards() = %v, want nil", err)
	}
	for _, record := range records {
		if record.EscrowID == escrowID {
			return record
		}
	}
	t.Fatalf("no row for escrow %s", escrowID)
	return DevshardRecord{}
}

// Test flow:
//  1. Store an inactive row and read it back unparked, as Activate's check does.
//  2. Park the row for settlement after that read, as the lifecycle can.
//  3. Call ActivateDevshardUnlessParked.
//  4. Assert it reports no activation and the row stays inactive and settlement-pending.
func TestActivatingARowParkedAfterItsCheckLeavesItParked(t *testing.T) {
	ctx := context.Background()
	registry := openTestStore(t)
	if err := registry.UpsertDevshard(ctx, DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a"}); err != nil {
		t.Fatalf("UpsertDevshard() = %v, want nil", err)
	}
	if checked := storedRow(t, registry, "1"); checked.SettlementPending || checked.SettleTxHash != "" {
		t.Fatalf("row = %+v, want it unparked at the check", checked)
	}
	if err := registry.ParkForSettlement(ctx, "1"); err != nil {
		t.Fatalf("ParkForSettlement() = %v, want nil", err)
	}

	activated, err := registry.ActivateDevshardUnlessParked(ctx, "1")

	if err != nil || activated {
		t.Fatalf("ActivateDevshardUnlessParked() = %v, %v; want false, nil", activated, err)
	}
	if row := storedRow(t, registry, "1"); row.Active || !row.SettlementPending {
		t.Errorf("row = active %v, settlement pending %v; want it still parked", row.Active, row.SettlementPending)
	}
}

// Test flow:
//  1. Store an inactive row that carries a settle transaction hash and no pending flag.
//  2. Call ActivateDevshardUnlessParked.
//  3. Assert it reports no activation and the row stays inactive.
func TestActivatingARowWithASettleHashLeavesItInactive(t *testing.T) {
	ctx := context.Background()
	registry := openTestStore(t)
	if err := registry.UpsertDevshard(ctx, DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a"}); err != nil {
		t.Fatalf("UpsertDevshard() = %v, want nil", err)
	}
	if err := registry.SetDevshardSettleTxHash(ctx, "1", broadcastSettleTxHash); err != nil {
		t.Fatalf("SetDevshardSettleTxHash() = %v, want nil", err)
	}

	activated, err := registry.ActivateDevshardUnlessParked(ctx, "1")

	if err != nil || activated {
		t.Fatalf("ActivateDevshardUnlessParked() = %v, %v; want false, nil", activated, err)
	}
	if row := storedRow(t, registry, "1"); row.Active {
		t.Error("a row carrying a settle hash was activated")
	}
}

// Test flow:
//  1. Store an inactive row and mark it gone from chain.
//  2. Call ActivateDevshardUnlessParked.
//  3. Assert it reports the activation, the row is active, and it is no longer gone from chain.
//  4. Call it for an escrow with no row and assert it reports no activation.
func TestActivatingAnUnparkedRowActivatesIt(t *testing.T) {
	ctx := context.Background()
	registry := openTestStore(t)
	if err := registry.UpsertDevshard(ctx, DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a"}); err != nil {
		t.Fatalf("UpsertDevshard() = %v, want nil", err)
	}
	if err := registry.MarkDevshardGoneFromChain(ctx, "1"); err != nil {
		t.Fatalf("MarkDevshardGoneFromChain() = %v, want nil", err)
	}

	activated, err := registry.ActivateDevshardUnlessParked(ctx, "1")

	if err != nil || !activated {
		t.Fatalf("ActivateDevshardUnlessParked() = %v, %v; want true, nil", activated, err)
	}
	if row := storedRow(t, registry, "1"); !row.Active || row.GoneFromChain {
		t.Errorf("row = active %v, gone from chain %v; want active and not gone", row.Active, row.GoneFromChain)
	}
	if activated, err := registry.ActivateDevshardUnlessParked(ctx, "2"); err != nil || activated {
		t.Errorf("ActivateDevshardUnlessParked(unknown) = %v, %v; want false, nil", activated, err)
	}
}

// Test flow:
//  1. Store an active row and read it back unparked, as register's check does.
//  2. Park the row for settlement after that read.
//  3. Call UpsertDevshardUnlessParked with an active record naming another model.
//  4. Assert it reports no write and the row stays inactive, settlement-pending, on its old model.
func TestRegisteringOverARowParkedAfterItsCheckLeavesItParked(t *testing.T) {
	ctx := context.Background()
	registry := openTestStore(t)
	record := DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a", Active: true}
	if err := registry.UpsertDevshard(ctx, record); err != nil {
		t.Fatalf("UpsertDevshard() = %v, want nil", err)
	}
	if checked := storedRow(t, registry, "1"); checked.SettlementPending || checked.SettleTxHash != "" {
		t.Fatalf("row = %+v, want it unparked at the check", checked)
	}
	if err := registry.ParkForSettlement(ctx, "1"); err != nil {
		t.Fatalf("ParkForSettlement() = %v, want nil", err)
	}
	record.Model = "model-b"

	written, err := registry.UpsertDevshardUnlessParked(ctx, record)

	if err != nil || written {
		t.Fatalf("UpsertDevshardUnlessParked() = %v, %v; want false, nil", written, err)
	}
	if row := storedRow(t, registry, "1"); row.Active || !row.SettlementPending || row.Model != "model-a" {
		t.Errorf("row = active %v, settlement pending %v, model %s; want it still parked on model-a", row.Active, row.SettlementPending, row.Model)
	}
}

// Test flow:
//  1. Call UpsertDevshardUnlessParked for an escrow with no row.
//  2. Assert it reports the write and the row is stored active.
//  3. Call it again for the same, unparked escrow with another model.
//  4. Assert it reports the write and the row carries the new model.
func TestRegisteringANewOrUnparkedRowWritesIt(t *testing.T) {
	ctx := context.Background()
	registry := openTestStore(t)
	record := DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a", Active: true}

	if written, err := registry.UpsertDevshardUnlessParked(ctx, record); err != nil || !written {
		t.Fatalf("UpsertDevshardUnlessParked(new) = %v, %v; want true, nil", written, err)
	}
	if row := storedRow(t, registry, "1"); !row.Active {
		t.Errorf("row = %+v, want it stored active", row)
	}
	record.Model = "model-b"
	if written, err := registry.UpsertDevshardUnlessParked(ctx, record); err != nil || !written {
		t.Fatalf("UpsertDevshardUnlessParked(existing) = %v, %v; want true, nil", written, err)
	}
	if row := storedRow(t, registry, "1"); row.Model != "model-b" {
		t.Errorf("row model = %s, want model-b", row.Model)
	}
}
