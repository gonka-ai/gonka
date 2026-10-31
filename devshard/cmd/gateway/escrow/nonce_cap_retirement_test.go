package escrow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/scheduler"
)

// cappedRetirementSource records which escrows routing was told retired at their nonce cap.
type cappedRetirementSource struct {
	fakeSettlementSource
	atCap, plain []string
}

func (s *cappedRetirementSource) Retire(escrowID string) error {
	s.plain = append(s.plain, escrowID)
	return s.fakeSettlementSource.Retire(escrowID)
}

func (s *cappedRetirementSource) RetireAtNonceCap(escrowID string) error {
	s.atCap = append(s.atCap, escrowID)
	return s.fakeSettlementSource.Retire(escrowID)
}

// Test flow:
//  1. Build an on-mode manager over a settlement source that tells the two retirements apart, and report escrow 6 at its nonce cap.
//  2. Run one tick, then park escrow 5 the way a settled-on-chain check does.
//  3. Assert escrow 6 left routing at its nonce cap and escrow 5 by a plain retirement.
func TestOnlyTheNonceCapRetirementTellsRoutingSo(t *testing.T) {
	manager, _, _, _, _ := onManager(t, onModels(1, 0))
	source := &cappedRetirementSource{}
	manager.settlementSource = source
	manager.OnBalanceExhausted("6", scheduler.ExhaustionNonceCap)

	require.NoError(t, manager.tick(context.Background()))
	require.NoError(t, manager.park(context.Background(), "5"))

	require.Equal(t, []string{"6"}, source.atCap)
	require.Equal(t, []string{"5"}, source.plain)
}
