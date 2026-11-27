package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Store an inactive row and read it back unparked, as Activate's check does.
//  2. Park the row for settlement after that read, as the lifecycle can.
//  3. Call ActivateDevshardUnlessParked.
//  4. Assert it reports no activation and the row stays inactive and settlement-pending.
func activatingARowParkedAfterItsCheckLeavesItParked(t *testing.T, open Opener) {
	ctx := context.Background()
	registry := open()
	if err := registry.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a"}); err != nil {
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
func activatingARowWithASettleHashLeavesItInactive(t *testing.T, open Opener) {
	ctx := context.Background()
	registry := open()
	if err := registry.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a"}); err != nil {
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
func activatingAnUnparkedRowActivatesIt(t *testing.T, open Opener) {
	ctx := context.Background()
	registry := open()
	if err := registry.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a"}); err != nil {
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
func registeringOverARowParkedAfterItsCheckLeavesItParked(t *testing.T, open Opener) {
	ctx := context.Background()
	registry := open()
	record := store.DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a", Active: true}
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
func registeringANewOrUnparkedRowWritesIt(t *testing.T, open Opener) {
	ctx := context.Background()
	registry := open()
	record := store.DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a", Active: true}

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

const broadcastSettleTxHash = "settle-tx-1"

// Test flow:
//  1. Register an escrow and record the settle transaction it broadcast, which stamps the hash with its broadcast time.
//  2. Register the same escrow again, as an operator or a seed does, with a record that carries no hash.
//  3. Assert the hash and its stamp are both still there: an upsert never writes the hash, so it cannot leave a stamp naming a hash it erased, or a hash with no stamp.
func reRegisteringAnEscrowKeepsItsSettleTransaction(t *testing.T, open Opener) {
	ctx := context.Background()
	registry := open()
	record := store.DevshardRecord{EscrowID: "1", PrivateKeyEnv: "KEY_1", Model: "model-a", Active: true}
	if err := registry.UpsertDevshard(ctx, record); err != nil {
		t.Fatalf("UpsertDevshard() = %v, want nil", err)
	}
	if err := registry.SetDevshardSettleTxHash(ctx, record.EscrowID, broadcastSettleTxHash); err != nil {
		t.Fatalf("SetDevshardSettleTxHash() = %v, want nil", err)
	}

	if err := registry.UpsertDevshard(ctx, record); err != nil {
		t.Fatalf("UpsertDevshard() again = %v, want nil", err)
	}

	hash, broadcastAt, err := registry.DevshardSettleTxHash(ctx, record.EscrowID)
	if err != nil {
		t.Fatalf("DevshardSettleTxHash() = %v, want nil", err)
	}
	if hash != broadcastSettleTxHash || broadcastAt.IsZero() {
		t.Fatalf("settle tx = %q stamped %v, want %q with its broadcast stamp", hash, broadcastAt, broadcastSettleTxHash)
	}
}

// Test flow:
//  1. Upsert three devshards whose ids differ only by case and read them back.
//  2. Assert the list is in byte order and a single read returns the row as written.
//  3. Walk one row through deactivate, activate, park, settle hash and chain facts with an amount above 32 bits.
//  4. Assert each step's flags, that a zero chain fact keeps its column, and that a parked row refuses the guarded writes.
//  5. Delete the row and assert a second delete reports it missing.
func devshardRegistry(t *testing.T, open Opener) {
	gatewayStore := open()
	ctx := context.Background()
	for _, id := range []string{"b", "a", "A"} {
		record := store.DevshardRecord{EscrowID: id, PrivateKeyEnv: "KEY_" + id, Model: "qwen", Active: true, RotationRole: store.RoleRegular, RotationEpoch: 7, RoutePrefix: "/" + id}
		if err := gatewayStore.UpsertDevshard(ctx, record); err != nil {
			t.Fatalf("UpsertDevshard(%s): %v", id, err)
		}
	}
	listed, err := gatewayStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards(): %v", err)
	}
	ids := make([]string, 0, len(listed))
	for _, record := range listed {
		ids = append(ids, record.EscrowID)
	}
	if !slices.Equal(ids, []string{"A", "a", "b"}) {
		t.Fatalf("ListDevshards() ids = %v, want [A a b]", ids)
	}
	got, found, err := gatewayStore.Devshard(ctx, "a")
	want := store.DevshardRecord{EscrowID: "a", PrivateKeyEnv: "KEY_a", Model: "qwen", Active: true, RotationRole: store.RoleRegular, RotationEpoch: 7, RoutePrefix: "/a"}
	if err != nil || !found || got != want {
		t.Fatalf("Devshard(a) = %+v, %v, %v; want %+v", got, found, err, want)
	}

	if err := gatewayStore.SetDevshardChainFacts(ctx, "a", 9, 5_000_000_000); err != nil {
		t.Fatalf("SetDevshardChainFacts(): %v", err)
	}
	if err := gatewayStore.SetDevshardChainFacts(ctx, "a", 0, 0); err != nil {
		t.Fatalf("SetDevshardChainFacts(zero): %v", err)
	}
	if err := gatewayStore.MarkDevshardGoneFromChain(ctx, "a"); err != nil {
		t.Fatalf("MarkDevshardGoneFromChain(): %v", err)
	}
	got, _, _ = gatewayStore.Devshard(ctx, "a")
	if got.ChainEpoch != 9 || got.Amount != 5_000_000_000 || got.Active || !got.GoneFromChain {
		t.Fatalf("after chain facts and gone: %+v, want epoch 9, amount 5000000000, inactive and gone", got)
	}
	if err := gatewayStore.SetDevshardActive(ctx, "a", true); err != nil {
		t.Fatalf("SetDevshardActive(true): %v", err)
	}
	got, _, _ = gatewayStore.Devshard(ctx, "a")
	if !got.Active || got.GoneFromChain {
		t.Fatalf("after activate: %+v, want active and back on chain", got)
	}
	parked, err := gatewayStore.ParkForSettlementIfActive(ctx, "a")
	if err != nil || !parked {
		t.Fatalf("ParkForSettlementIfActive() = %v, %v, want true", parked, err)
	}
	activated, err := gatewayStore.ActivateDevshardUnlessParked(ctx, "a")
	if err != nil || activated {
		t.Fatalf("ActivateDevshardUnlessParked() = %v, %v, want false for a parked row", activated, err)
	}
	wrote, err := gatewayStore.UpsertDevshardUnlessParked(ctx, want)
	if err != nil || wrote {
		t.Fatalf("UpsertDevshardUnlessParked() = %v, %v, want false for a parked row", wrote, err)
	}
	if err := gatewayStore.SetDevshardSettleTxHash(ctx, "a", "ABC123"); err != nil {
		t.Fatalf("SetDevshardSettleTxHash(): %v", err)
	}
	hash, at, err := gatewayStore.DevshardSettleTxHash(ctx, "a")
	if err != nil || hash != "ABC123" || time.Since(at) > time.Minute {
		t.Fatalf("DevshardSettleTxHash() = %q, %v, %v, want ABC123 stamped now", hash, at, err)
	}

	if err := gatewayStore.DeleteDevshard(ctx, "a"); err != nil {
		t.Fatalf("DeleteDevshard(): %v", err)
	}
	if err := gatewayStore.DeleteDevshard(ctx, "a"); !errors.Is(err, store.ErrDevshardNotFound) {
		t.Fatalf("second DeleteDevshard() = %v, want store.ErrDevshardNotFound", err)
	}
}
