package engine

import (
	"fmt"
	"testing"

	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

// Test flow:
//  1. Observe a pick declined because the pinned escrow is short of balance, then one declined because no escrow can pay.
//  2. Assert only the second marks the race out of funds: a pinned escrow running short says nothing about the model's escrows, and its first failure keeps the reason.
func TestOnlyAnUnpinnedBalanceRefusalMarksTheRaceOutOfFunds(t *testing.T) {
	pinnedShort := &raceCoordinator{}
	pinnedShort.observePick(scheduler.Assignment{}, fmt.Errorf("%w: %w: %w", scheduler.ErrPinnedEscrowShort, scheduler.ErrNoEscrowCapacity, types.ErrInsufficientBalance))
	if pinnedShort.balanceExhausted {
		t.Fatal("a pinned escrow running short marked the race out of funds")
	}

	outOfFunds := &raceCoordinator{}
	outOfFunds.observePick(scheduler.Assignment{}, fmt.Errorf("%w: %w", scheduler.ErrNoEscrowCapacity, types.ErrInsufficientBalance))
	if !outOfFunds.balanceExhausted {
		t.Fatal("a model with no escrow able to pay did not mark the race out of funds")
	}
}
