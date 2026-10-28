package store

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// Test flow:
//  1. Register a row, record chain epoch 7 and amount 5000 for it, then re-register it the way an operator would.
//  2. Assert the re-registration kept both facts.
//  3. Record chain epoch 0 and amount 6000, and assert the zero left the chain epoch as it was.
//  4. Assert recording facts for an unknown row is ErrDevshardNotFound.
func TestTheChainFactsSurviveARegistrationAndAZeroKeepsItsColumn(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()
	row := DevshardRecord{EscrowID: "9", PrivateKeyEnv: "KEY", Model: "qwen", Active: true, RotationRole: "regular", RotationEpoch: 8}
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
	if err := testStore.SetDevshardChainFacts(ctx, "unknown", 7, 5000); !errors.Is(err, ErrDevshardNotFound) {
		t.Fatalf("SetDevshardChainFacts(unknown) = %v, want ErrDevshardNotFound", err)
	}
}

// Test flow:
//  1. Register a serving row.
//  2. Mark it gone from chain.
//  3. Assert it is inactive and gone, and that marking an unknown row is ErrDevshardNotFound.
func TestMarkingARowGoneFromChainTakesItOutOfServiceInOneWrite(t *testing.T) {
	testStore := openTestStore(t)
	ctx := context.Background()
	if err := testStore.UpsertDevshard(ctx, DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: "regular"}); err != nil {
		t.Fatalf("UpsertDevshard() = %v, want nil", err)
	}

	if err := testStore.MarkDevshardGoneFromChain(ctx, "9"); err != nil {
		t.Fatalf("MarkDevshardGoneFromChain() = %v, want nil", err)
	}

	if stored := onlyRow(t, testStore); stored.Active || !stored.GoneFromChain {
		t.Fatalf("row = %+v, want inactive and gone from chain", stored)
	}
	if err := testStore.MarkDevshardGoneFromChain(ctx, "unknown"); !errors.Is(err, ErrDevshardNotFound) {
		t.Fatalf("MarkDevshardGoneFromChain(unknown) = %v, want ErrDevshardNotFound", err)
	}
}

// Test flow:
//  1. Table-driven: each case is one statement that puts a row back into service (`ActivateDevshardUnlessParked`, `SetDevshardActive` to true, `UpsertDevshard` or `UpsertDevshardUnlessParked` with active set), plus an upsert that keeps the row inactive.
//  2. For each case, register a row, mark it gone from chain, then run the case's statement.
//  3. Assert the mark is cleared exactly when the statement reactivated the row.
func TestReactivatingARowGoneFromChainClearsTheMark(t *testing.T) {
	testCases := []struct {
		name      string
		statement func(*Store) error
		wantGone  bool
	}{
		{name: "ActivateDevshardUnlessParked", statement: func(testStore *Store) error {
			_, err := testStore.ActivateDevshardUnlessParked(context.Background(), "9")
			return err
		}},
		{name: "SetDevshardActive", statement: func(testStore *Store) error {
			return testStore.SetDevshardActive(context.Background(), "9", true)
		}},
		{name: "UpsertDevshard active", statement: func(testStore *Store) error {
			return testStore.UpsertDevshard(context.Background(), DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: "regular"})
		}},
		{name: "UpsertDevshardUnlessParked active", statement: func(testStore *Store) error {
			_, err := testStore.UpsertDevshardUnlessParked(context.Background(), DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: "regular"})
			return err
		}},
		{name: "UpsertDevshard inactive", wantGone: true, statement: func(testStore *Store) error {
			return testStore.UpsertDevshard(context.Background(), DevshardRecord{EscrowID: "9", Model: "qwen", RotationRole: "regular"})
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			testStore := openTestStore(t)
			ctx := context.Background()
			if err := testStore.UpsertDevshard(ctx, DevshardRecord{EscrowID: "9", Model: "qwen", Active: true, RotationRole: "regular"}); err != nil {
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
//  1. Inside a bubble, whose clock starts at 2000-01-01, open a store and register a row.
//  2. Record a settle hash for it.
//  3. Assert the broadcast stamp read back is the bubble's clock, not the database's.
func TestTheSettleStampFollowsTheProcessClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		testStore, err := Open(t.TempDir())
		if err != nil {
			t.Fatalf("Open() = %v, want nil", err)
		}
		ctx := context.Background()
		if err := testStore.UpsertDevshard(ctx, DevshardRecord{EscrowID: "9", Model: "qwen", Active: true}); err != nil {
			t.Fatalf("UpsertDevshard() = %v, want nil", err)
		}

		if err := testStore.SetDevshardSettleTxHash(ctx, "9", "SETTLE-TX"); err != nil {
			t.Fatalf("SetDevshardSettleTxHash() = %v, want nil", err)
		}

		hash, broadcastAt, err := testStore.DevshardSettleTxHash(ctx, "9")
		closeErr := testStore.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("DevshardSettleTxHash() = %v, Close() = %v, want nil, nil", err, closeErr)
		}
		if want := time.Now().UTC().Truncate(time.Second); hash != "SETTLE-TX" || !broadcastAt.Equal(want) {
			t.Fatalf("DevshardSettleTxHash() = %q at %v, want SETTLE-TX at %v", hash, broadcastAt, want)
		}
	})
}

func onlyRow(t *testing.T, testStore *Store) DevshardRecord {
	t.Helper()
	rows, err := testStore.ListDevshards(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListDevshards() = %d rows, %v, want 1, nil", len(rows), err)
	}
	return rows[0]
}
