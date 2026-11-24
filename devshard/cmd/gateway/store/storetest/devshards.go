package storetest

import (
	"context"
	"errors"
	"testing"

	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Upsert a `store.DevshardRecord` and assert `ListDevshards` returns exactly it.
//  2. Deactivate it and mark it settlement-pending, and assert the listed record reflects both.
//  3. Upsert the same escrow again with a different model and active state, and assert the upsert replaces the record's fields.
//  4. Delete the escrow and assert `ListDevshards` returns nothing.
func devshardCRUDLifecycle(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()

	record := store.DevshardRecord{
		EscrowID:      "escrow-7",
		PrivateKeyEnv: "GATEWAY_KEY_ESCROW_7",
		Model:         "model-a",
		Active:        true,
		RotationRole:  "regular",
		RotationEpoch: 12,
	}
	if err := testStore.UpsertDevshard(ctx, record); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}

	listed, err := testStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards(): %v", err)
	}
	if len(listed) != 1 || listed[0] != record {
		t.Fatalf("ListDevshards() = %+v, want exactly the upserted record", listed)
	}

	if err := testStore.SetDevshardActive(ctx, "escrow-7", false); err != nil {
		t.Fatalf("SetDevshardActive(): %v", err)
	}
	if err := testStore.SetDevshardSettlementPending(ctx, "escrow-7", true); err != nil {
		t.Fatalf("SetDevshardSettlementPending(): %v", err)
	}
	listed, err = testStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards() after updates: %v", err)
	}
	if listed[0].Active || !listed[0].SettlementPending {
		t.Fatalf("after updates got %+v, want inactive + settlement pending", listed[0])
	}

	record.Model = "model-b"
	record.Active = false
	if err := testStore.UpsertDevshard(ctx, record); err != nil {
		t.Fatalf("UpsertDevshard() replace: %v", err)
	}
	listed, _ = testStore.ListDevshards(ctx)
	if listed[0].Model != "model-b" {
		t.Fatalf("after replace got model %q, want model-b", listed[0].Model)
	}

	if err := testStore.DeleteDevshard(ctx, "escrow-7"); err != nil {
		t.Fatalf("DeleteDevshard(): %v", err)
	}
	listed, _ = testStore.ListDevshards(ctx)
	if len(listed) != 0 {
		t.Fatalf("after delete list = %+v, want empty", listed)
	}
}

// Test flow:
//  1. Upsert a devshard and mark it settlement-pending.
//  2. Re-import (upsert again) the same escrow with a different model and active state, the way a re-import would build the record with `SettlementPending` at its zero value.
//  3. Assert the settlement-pending flag survived the re-import while every other field was replaced.
func upsertDevshardKeepsAQueuedSettlementWhileReplacingEveryOtherField(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()

	parked := store.DevshardRecord{EscrowID: "escrow-9", PrivateKeyEnv: "GATEWAY_KEY_9", Model: "model-a", RotationRole: "regular"}
	if err := testStore.UpsertDevshard(ctx, parked); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	if err := testStore.SetDevshardSettlementPending(ctx, "escrow-9", true); err != nil {
		t.Fatalf("SetDevshardSettlementPending(): %v", err)
	}

	reimported := parked
	reimported.Model = "model-b"
	reimported.Active = true
	if err := testStore.UpsertDevshard(ctx, reimported); err != nil {
		t.Fatalf("UpsertDevshard() re-import: %v", err)
	}

	listed, err := testStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards(): %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListDevshards() = %+v, want exactly one row", listed)
	}
	if !listed[0].SettlementPending {
		t.Fatal("the re-import cleared the queued settlement; nothing will ever settle that escrow")
	}
	if listed[0].Model != "model-b" || !listed[0].Active {
		t.Fatalf("re-imported row = %+v, want model-b and active (every other field replaced)", listed[0])
	}
}

// Test flow:
//  1. Register two escrows, the second parked.
//  2. Read the second by id and assert the row matches what the list returns for it.
//  3. Read an id that was never registered and assert it is reported missing without an error.
func devshardReadsOneRowAsTheListDoes(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()
	for _, escrowID := range []string{"1", "2"} {
		if err := testStore.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: escrowID, PrivateKeyEnv: "KEY_" + escrowID, Model: "model", Active: true}); err != nil {
			t.Fatalf("UpsertDevshard(%s): %v", escrowID, err)
		}
	}
	if err := testStore.ParkForSettlement(ctx, "2"); err != nil {
		t.Fatalf("ParkForSettlement(2): %v", err)
	}

	listed, err := testStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards(): %v", err)
	}
	record, found, err := testStore.Devshard(ctx, "2")
	if err != nil || !found {
		t.Fatalf("Devshard(2) = found %v, err %v", found, err)
	}
	if record != listed[1] {
		t.Fatalf("Devshard(2) = %+v, want the listed row %+v", record, listed[1])
	}

	if _, found, err := testStore.Devshard(ctx, "ghost"); err != nil || found {
		t.Fatalf("Devshard(ghost) = found %v, err %v, want missing and no error", found, err)
	}
}

// Test flow:
//  1. Call `SetDevshardActive`, `SetDevshardSettlementPending`, and `DeleteDevshard` on an escrow id that was never registered.
//  2. Assert each returns `store.ErrDevshardNotFound`.
func missingDevshardReturnsSentinel(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()
	if err := testStore.SetDevshardActive(ctx, "ghost", true); !errors.Is(err, store.ErrDevshardNotFound) {
		t.Fatalf("SetDevshardActive(ghost) = %v, want store.ErrDevshardNotFound", err)
	}
	if err := testStore.SetDevshardSettlementPending(ctx, "ghost", true); !errors.Is(err, store.ErrDevshardNotFound) {
		t.Fatalf("SetDevshardSettlementPending(ghost) = %v, want store.ErrDevshardNotFound", err)
	}
	if err := testStore.DeleteDevshard(ctx, "ghost"); !errors.Is(err, store.ErrDevshardNotFound) {
		t.Fatalf("DeleteDevshard(ghost) = %v, want store.ErrDevshardNotFound", err)
	}
}

// Test flow:
//  1. Upsert an active devshard.
//  2. Park it for settlement via `ParkForSettlement`.
//  3. Assert the listed record is both inactive and settlement-pending, set together as one statement so a crash between the two can never leave the escrow out of service and never settled.
func parkingForSettlementSetsBothFieldsAtOnce(t *testing.T, open Opener) {
	gatewayStore := open()
	ctx := context.Background()
	if err := gatewayStore.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "7", Model: "qwen", Active: true}); err != nil {
		t.Fatalf("UpsertDevshard: %v", err)
	}

	if err := gatewayStore.ParkForSettlement(ctx, "7"); err != nil {
		t.Fatalf("ParkForSettlement: %v", err)
	}

	records, err := gatewayStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Active {
		t.Fatal("a parked escrow is still routable")
	}
	if !records[0].SettlementPending {
		t.Fatal("a parked escrow is not pending, so nothing will ever settle it")
	}
}

// Test flow:
//  1. Call `ParkForSettlement` on an escrow id that was never registered.
//  2. Assert it returns `store.ErrDevshardNotFound`.
func parkingAnUnknownEscrowIsReported(t *testing.T, open Opener) {
	gatewayStore := open()

	err := gatewayStore.ParkForSettlement(context.Background(), "404")

	if !errors.Is(err, store.ErrDevshardNotFound) {
		t.Fatalf("ParkForSettlement = %v, want store.ErrDevshardNotFound", err)
	}
}

// Test flow:
//  1. Upsert an active devshard.
//  2. Call `ParkForSettlementIfActive`.
//  3. Assert it reports that it parked the escrow, and the listed record is inactive and settlement-pending.
func parkingIfActiveParksAServingEscrow(t *testing.T, open Opener) {
	gatewayStore := open()
	ctx := context.Background()
	if err := gatewayStore.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "7", Model: "qwen", Active: true}); err != nil {
		t.Fatalf("UpsertDevshard: %v", err)
	}

	parked, err := gatewayStore.ParkForSettlementIfActive(ctx, "7")

	if err != nil {
		t.Fatalf("ParkForSettlementIfActive: %v", err)
	}
	if !parked {
		t.Fatal("parking a serving escrow did not report that it parked it")
	}
	records, err := gatewayStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Active || !records[0].SettlementPending {
		t.Fatalf("record = %+v, want inactive and settlement-pending", records[0])
	}
}

// Test flow:
//  1. Call `ParkForSettlementIfActive` on an escrow id that was never registered.
//  2. Assert it returns no error and reports that nothing was parked.
func parkingIfActiveReportsNoParkForAnUnknownEscrow(t *testing.T, open Opener) {
	gatewayStore := open()

	parked, err := gatewayStore.ParkForSettlementIfActive(context.Background(), "404")

	if err != nil || parked {
		t.Fatalf("ParkForSettlementIfActive = %v, %v; want false, nil", parked, err)
	}
}

// Test flow:
//  1. Upsert an inactive devshard.
//  2. Call `ParkForSettlementIfActive`.
//  3. Assert it reports no park happened and the record's settlement-pending flag stays clear.
func parkingIfActiveLeavesAnEscrowAlreadyOutOfServiceUntouched(t *testing.T, open Opener) {
	gatewayStore := open()
	ctx := context.Background()
	if err := gatewayStore.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "7", Model: "qwen", Active: false}); err != nil {
		t.Fatalf("UpsertDevshard: %v", err)
	}

	parked, err := gatewayStore.ParkForSettlementIfActive(ctx, "7")

	if err != nil {
		t.Fatalf("ParkForSettlementIfActive: %v", err)
	}
	if parked {
		t.Fatal("parking an escrow already out of service reported a park")
	}
	records, err := gatewayStore.ListDevshards(ctx)
	if err != nil {
		t.Fatalf("ListDevshards: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].SettlementPending {
		t.Fatal("an escrow taken out of service without a settlement was queued for one")
	}
}

// Test flow:
//  1. Register a row, record chain epoch 7 and amount 5000 for it, then re-register it the way an operator would.
//  2. Assert the re-registration kept both facts.
//  3. Record chain epoch 0 and amount 6000, and assert the zero left the chain epoch as it was.
//  4. Assert recording facts for an unknown row is store.ErrDevshardNotFound.
func theChainFactsSurviveARegistrationAndAZeroKeepsItsColumn(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()
	row := store.DevshardRecord{EscrowID: "9", PrivateKeyEnv: "KEY", Model: "qwen", Active: true, RotationRole: "regular", RotationEpoch: 8}
	if err := testStore.UpsertDevshard(ctx, row); err != nil {
		t.Fatalf("UpsertDevshard() = %v, want nil", err)
	}
	if err := testStore.SetDevshardChainFacts(ctx, "9", 7, 5000); err != nil {
		t.Fatalf("SetDevshardChainFacts(7, 5000) = %v, want nil", err)
	}
	if err := testStore.UpsertDevshard(ctx, row); err != nil {
		t.Fatalf("UpsertDevshard() again = %v, want nil", err)
	}
	if stored := onlyRow(t, testStore); stored.ChainEpoch != 7 || stored.Amount != 5000 {
		t.Fatalf("row after re-registration = %+v, want chain epoch 7 and amount 5000", stored)
	}

	if err := testStore.SetDevshardChainFacts(ctx, "9", 0, 6000); err != nil {
		t.Fatalf("SetDevshardChainFacts(0, 6000) = %v, want nil", err)
	}
	if stored := onlyRow(t, testStore); stored.ChainEpoch != 7 || stored.Amount != 6000 {
		t.Fatalf("row = %+v, want chain epoch 7 kept and amount 6000", stored)
	}
	if err := testStore.SetDevshardChainFacts(ctx, "unknown", 7, 5000); !errors.Is(err, store.ErrDevshardNotFound) {
		t.Fatalf("SetDevshardChainFacts(unknown) = %v, want store.ErrDevshardNotFound", err)
	}
}

// Test flow:
//  1. Register a serving row.
//  2. Mark it gone from chain.
//  3. Assert it is inactive and gone, and that marking an unknown row is store.ErrDevshardNotFound.
func markingARowGoneFromChainTakesItOutOfServiceInOneWrite(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()
	if err := testStore.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: "regular"}); err != nil {
		t.Fatalf("UpsertDevshard() = %v, want nil", err)
	}

	if err := testStore.MarkDevshardGoneFromChain(ctx, "9"); err != nil {
		t.Fatalf("MarkDevshardGoneFromChain() = %v, want nil", err)
	}

	if stored := onlyRow(t, testStore); stored.Active || !stored.GoneFromChain {
		t.Fatalf("row = %+v, want inactive and gone from chain", stored)
	}
	if err := testStore.MarkDevshardGoneFromChain(ctx, "unknown"); !errors.Is(err, store.ErrDevshardNotFound) {
		t.Fatalf("MarkDevshardGoneFromChain(unknown) = %v, want store.ErrDevshardNotFound", err)
	}
}

// Test flow:
//  1. Table-driven: each case is one statement that puts a row back into service (`ActivateDevshardUnlessParked`, `SetDevshardActive` to true, `UpsertDevshard` or `UpsertDevshardUnlessParked` with active set), plus an upsert that keeps the row inactive.
//  2. For each case, register a row, mark it gone from chain, then run the case's statement.
//  3. Assert the mark is cleared exactly when the statement reactivated the row.
func reactivatingARowGoneFromChainClearsTheMark(t *testing.T, open Opener) {
	testCases := []struct {
		name      string
		statement func(store.Store) error
		wantGone  bool
	}{
		{name: "ActivateDevshardUnlessParked", statement: func(testStore store.Store) error {
			_, err := testStore.ActivateDevshardUnlessParked(context.Background(), "9")
			return err
		}},
		{name: "SetDevshardActive", statement: func(testStore store.Store) error {
			return testStore.SetDevshardActive(context.Background(), "9", true)
		}},
		{name: "UpsertDevshard active", statement: func(testStore store.Store) error {
			return testStore.UpsertDevshard(context.Background(), store.DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: "regular"})
		}},
		{name: "UpsertDevshardUnlessParked active", statement: func(testStore store.Store) error {
			_, err := testStore.UpsertDevshardUnlessParked(context.Background(), store.DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: "regular"})
			return err
		}},
		{name: "UpsertDevshard inactive", wantGone: true, statement: func(testStore store.Store) error {
			return testStore.UpsertDevshard(context.Background(), store.DevshardRecord{EscrowID: "9", Model: "qwen", RotationRole: "regular"})
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testStore := open()
			ctx := context.Background()
			if err := testStore.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: "regular"}); err != nil {
				t.Fatalf("UpsertDevshard() = %v, want nil", err)
			}
			if err := testStore.MarkDevshardGoneFromChain(ctx, "9"); err != nil {
				t.Fatalf("MarkDevshardGoneFromChain() = %v, want nil", err)
			}

			if err := testCase.statement(testStore); err != nil {
				t.Fatalf("%s = %v, want nil", testCase.name, err)
			}

			if stored := onlyRow(t, testStore); stored.GoneFromChain != testCase.wantGone {
				t.Fatalf("%s: row = %+v, want gone_from_chain %v", testCase.name, stored, testCase.wantGone)
			}
		})
	}
}

// Test flow:
//  1. Register a row and record a settle hash for it.
//  2. Assert the hash reads back with a broadcast stamp.
//  3. Clear the hash and assert the hash and the stamp read back empty.
func theSettleStampIsRecordedAndClearedWithTheHash(t *testing.T, open Opener) {
	testStore := open()
	ctx := context.Background()
	if err := testStore.UpsertDevshard(ctx, store.DevshardRecord{EscrowID: "9", Model: "qwen", Active: true}); err != nil {
		t.Fatalf("UpsertDevshard() = %v, want nil", err)
	}
	if err := testStore.SetDevshardSettleTxHash(ctx, "9", "SETTLE-TX"); err != nil {
		t.Fatalf("SetDevshardSettleTxHash() = %v, want nil", err)
	}
	hash, broadcastAt, err := testStore.DevshardSettleTxHash(ctx, "9")
	if err != nil || hash != "SETTLE-TX" || broadcastAt.IsZero() {
		t.Fatalf("DevshardSettleTxHash() = %q at %v, %v; want SETTLE-TX with a stamp", hash, broadcastAt, err)
	}
	if err := testStore.SetDevshardSettleTxHash(ctx, "9", ""); err != nil {
		t.Fatalf("SetDevshardSettleTxHash(clear) = %v, want nil", err)
	}
	hash, broadcastAt, err = testStore.DevshardSettleTxHash(ctx, "9")
	if err != nil || hash != "" || !broadcastAt.IsZero() {
		t.Fatalf("DevshardSettleTxHash() after clearing = %q at %v, %v; want nothing", hash, broadcastAt, err)
	}
}

func onlyRow(t *testing.T, testStore store.Store) store.DevshardRecord {
	t.Helper()
	rows, err := testStore.ListDevshards(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListDevshards() = %d rows, %v, want 1, nil", len(rows), err)
	}
	return rows[0]
}

func storedRow(t *testing.T, registry store.Store, escrowID string) store.DevshardRecord {
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
	return store.DevshardRecord{}
}
