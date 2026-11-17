package store

import (
	"context"
	"testing"
	"time"
)

// Test flow:
//  1. Save a `Commitment` with every field populated, including a nanosecond-precision `CreatedAt`.
//  2. Load it back and assert every field matches, checking `CreatedAt` separately for exact round-trip precision.
//  3. Delete the commitment and assert `LoadCommitments` returns no rows.
func TestCommitmentSaveLoadDeleteRoundTrip(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	commitment := Commitment{
		TxHash:        "tx-1",
		Model:         "model-a",
		Role:          "temp",
		PrivateKeyEnv: "GATEWAY_KEY_ESCROW_1",
		Epoch:         7,
		BlockHeight:   1000,
		CreatedAt:     time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC),
	}
	if err := testStore.SaveCommitment(ctx, commitment); err != nil {
		t.Fatalf("SaveCommitment(): %v", err)
	}

	loaded, err := testStore.LoadCommitments(ctx)
	if err != nil {
		t.Fatalf("LoadCommitments(): %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("LoadCommitments() = %+v, want exactly 1 row", loaded)
	}
	got := loaded[0]
	want := commitment
	got.CreatedAt, want.CreatedAt = time.Time{}, time.Time{}
	if got != want {
		t.Fatalf("LoadCommitments()[0] = %+v, want %+v", loaded[0], commitment)
	}
	if !loaded[0].CreatedAt.Equal(commitment.CreatedAt) || loaded[0].CreatedAt.UnixNano() != commitment.CreatedAt.UnixNano() {
		t.Fatalf("CreatedAt = %v, want exact round trip of %v", loaded[0].CreatedAt, commitment.CreatedAt)
	}

	if err := testStore.DeleteCommitment(ctx, commitment.TxHash); err != nil {
		t.Fatalf("DeleteCommitment(): %v", err)
	}
	loaded, err = testStore.LoadCommitments(ctx)
	if err != nil {
		t.Fatalf("LoadCommitments() after delete: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("LoadCommitments() after delete = %+v, want empty", loaded)
	}
}

// Test flow:
//  1. Save a `Commitment`, then save a second one with the same `TxHash` but a different model, epoch, and block height.
//  2. Assert `LoadCommitments` returns exactly one row, holding the second save's fields.
func TestSaveCommitmentUpsertReplacesNotDuplicates(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	first := Commitment{
		TxHash:      "tx-2",
		Model:       "model-a",
		Role:        "temp",
		Epoch:       1,
		BlockHeight: 10,
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := testStore.SaveCommitment(ctx, first); err != nil {
		t.Fatalf("SaveCommitment() first: %v", err)
	}

	second := first
	second.Model = "model-b"
	second.Epoch = 2
	second.BlockHeight = 20
	if err := testStore.SaveCommitment(ctx, second); err != nil {
		t.Fatalf("SaveCommitment() second: %v", err)
	}

	loaded, err := testStore.LoadCommitments(ctx)
	if err != nil {
		t.Fatalf("LoadCommitments(): %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("LoadCommitments() = %+v, want exactly 1 row (upsert must replace)", loaded)
	}
	if loaded[0].Model != "model-b" || loaded[0].Epoch != 2 || loaded[0].BlockHeight != 20 {
		t.Fatalf("LoadCommitments()[0] = %+v, want replaced fields from %+v", loaded[0], second)
	}
}

// Test flow:
//  1. Save three commitments whose hashes and timestamps disagree on sort order: one that sorts first by hash but last by time, and two that tie on time and must fall back to hash.
//  2. Assert `LoadCommitments` returns them ordered by time then hash, proving the compound `ORDER BY` rather than luck.
func TestLoadCommitmentsDeterministicOrder(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []Commitment{
		{TxHash: "tx-z", CreatedAt: base.Add(time.Second)},
		{TxHash: "tx-b", CreatedAt: base},
		{TxHash: "tx-a", CreatedAt: base},
	}
	for _, row := range rows {
		if err := testStore.SaveCommitment(ctx, row); err != nil {
			t.Fatalf("SaveCommitment(%s): %v", row.TxHash, err)
		}
	}

	loaded, err := testStore.LoadCommitments(ctx)
	if err != nil {
		t.Fatalf("LoadCommitments(): %v", err)
	}
	wantOrder := []string{"tx-a", "tx-b", "tx-z"}
	if len(loaded) != len(wantOrder) {
		t.Fatalf("LoadCommitments() = %d rows, want %d", len(loaded), len(wantOrder))
	}
	for i, txHash := range wantOrder {
		if loaded[i].TxHash != txHash {
			t.Fatalf("LoadCommitments()[%d].TxHash = %q, want %q (full order %+v)", i, loaded[i].TxHash, txHash, loaded)
		}
	}
}

// Test flow:
//  1. Delete a commitment that was never saved.
//  2. Assert `DeleteCommitment` returns no error, an idempotent no-op.
func TestDeleteCommitmentAbsentIsNoOp(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()
	if err := testStore.DeleteCommitment(ctx, "ghost-tx"); err != nil {
		t.Fatalf("DeleteCommitment(absent) = %v, want nil (idempotent no-op)", err)
	}
}

// Test flow:
//  1. Save a `Commitment` with no `CreatedAt` set.
//  2. Assert `LoadCommitments` loads the row without error and its `CreatedAt` comes back zero, rather than failing the whole load the way a raw `time.Parse` on an empty string would.
func TestLoadCommitmentsAcceptsARowWithNoCreatedAt(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()

	if err := testStore.SaveCommitment(ctx, Commitment{TxHash: "tx-zero", Model: "model-a", Role: "regular"}); err != nil {
		t.Fatalf("SaveCommitment(): %v", err)
	}

	loaded, err := testStore.LoadCommitments(ctx)
	if err != nil {
		t.Fatalf("LoadCommitments() = %v, want a zero created_at to load", err)
	}
	if len(loaded) != 1 || !loaded[0].CreatedAt.IsZero() {
		t.Fatalf("loaded = %+v, want one row with a zero CreatedAt", loaded)
	}
}
