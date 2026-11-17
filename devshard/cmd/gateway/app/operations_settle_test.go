package app

import (
	"context"
	"errors"
	"testing"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
)

type settleObserver struct {
	escrows          *registry.Registry
	failure          error
	routableAtSettle bool
}

func (s *settleObserver) CreateEscrow(context.Context, escrow.ModelConfig) (chain.CreateEscrowResult, error) {
	return chain.CreateEscrowResult{}, nil
}

func (s *settleObserver) Settle(_ context.Context, escrowID string, _ bool) (chain.SettleEscrowResult, error) {
	_, s.routableAtSettle = s.escrows.RoutableSession(escrowID)
	return chain.SettleEscrowResult{}, s.failure
}

type rowDeletingSettler struct {
	records *store.Store
}

func (s *rowDeletingSettler) CreateEscrow(context.Context, escrow.ModelConfig) (chain.CreateEscrowResult, error) {
	return chain.CreateEscrowResult{}, nil
}

func (s *rowDeletingSettler) Settle(ctx context.Context, escrowID string, _ bool) (chain.SettleEscrowResult, error) {
	if _, err := findDevshard(ctx, s.records, escrowID); err != nil {
		return chain.SettleEscrowResult{}, err
	}
	return chain.SettleEscrowResult{}, s.records.DeleteDevshard(ctx, escrowID)
}

func settleableEscrow(t *testing.T) (*store.Store, *registry.Registry) {
	t.Helper()
	records := openedStore(t)
	if err := records.UpsertDevshard(context.Background(), store.DevshardRecord{EscrowID: "42", PrivateKeyEnv: "DEVSHARD_IMPORT_KEY", Model: "model-a", Active: true}); err != nil {
		t.Fatalf("UpsertDevshard(): %v", err)
	}
	escrows := servingRegistry(t)
	if err := escrows.Add(context.Background(), "42", "model-a"); err != nil {
		t.Fatalf("Add(42) = %v", err)
	}
	return records, escrows
}

// Test flow:
//  1. For a settlement that succeeds and one the chain refuses, store and publish escrow 42 and build an operator wrapping a settle observer.
//  2. Call operator.Settle for that escrow.
//  3. Assert the returned error matches the case's failure.
//  4. Assert the escrow was not routable at the moment settlement began.
//  5. Assert the escrow is not routable after Settle returns.
func TestSettleStopsRoutingBeforeTheChainSettlementAndLeavesItRetiredWhenItFails(t *testing.T) {
	testCases := []struct {
		name    string
		failure error
	}{
		{name: "a settlement that succeeds"},
		{name: "a settlement the chain refused", failure: errors.New("broadcast refused")},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			records, escrows := settleableEscrow(t)
			settler := &settleObserver{escrows: escrows, failure: testCase.failure}
			operator := &operations{store: records, escrows: escrows, manager: settler}

			_, err := operator.Settle(context.Background(), "42", false)

			if !errors.Is(err, testCase.failure) {
				t.Fatalf("Settle() = %v, want %v", err, testCase.failure)
			}
			if settler.routableAtSettle {
				t.Error("the escrow was still routable when the settlement began: a nonce can land after the payload was built")
			}
			if _, routable := escrows.RoutableSession("42"); routable {
				t.Error("the escrow is routable after the settle: it claims funds against nonces routing can still spend")
			}
		})
	}
}

// Test flow:
//  1. Store and publish escrow 42 behind a settler that deletes the row, as a successful chain settlement does.
//  2. Settle escrow 42 twice, the second as a settle queued behind the first would run.
//  3. Assert the first settle succeeds and the second returns ErrUnknownDevshard.
func TestASettleAfterTheEscrowWasSettledAnswersUnknown(t *testing.T) {
	ctx := context.Background()
	records, escrows := settleableEscrow(t)
	operator := &operations{store: records, escrows: escrows, manager: &rowDeletingSettler{records: records}}

	_, first := operator.Settle(ctx, "42", false)
	_, second := operator.Settle(ctx, "42", false)

	if first != nil {
		t.Fatalf("first Settle = %v", first)
	}
	if !errors.Is(second, api.ErrUnknownDevshard) {
		t.Errorf("second Settle = %v, want ErrUnknownDevshard", second)
	}
}

// Test flow:
//  1. Open a store holding no row for escrow 42, as after a settle the call waited behind.
//  2. Call operator.Deactivate for escrow 42.
//  3. Assert it returns ErrUnknownDevshard.
func TestADeactivateOfAGoneRowAnswersUnknown(t *testing.T) {
	operator := &operations{store: openedStore(t), escrows: servingRegistry(t)}

	if err := operator.Deactivate(context.Background(), "42"); !errors.Is(err, api.ErrUnknownDevshard) {
		t.Fatalf("Deactivate(42) = %v, want ErrUnknownDevshard", err)
	}
}

// Test flow:
//  1. Store and publish escrow 42 behind a settler that answers ErrUnknownEscrow, as when the rotation lifecycle settled and deleted the row after the settle's re-read.
//  2. Settle escrow 42.
//  3. Assert it returns ErrUnknownDevshard.
func TestASettleWhoseRowTheRotationDeletedMeanwhileAnswersUnknown(t *testing.T) {
	records, escrows := settleableEscrow(t)
	operator := &operations{store: records, escrows: escrows, manager: &settleObserver{escrows: escrows, failure: escrow.ErrUnknownEscrow}}

	if _, err := operator.Settle(context.Background(), "42", false); !errors.Is(err, api.ErrUnknownDevshard) {
		t.Fatalf("Settle(42) = %v, want ErrUnknownDevshard", err)
	}
}
