package admin

import (
	"decentralized-api/chainphase"
	"testing"

	"github.com/productscience/inference/x/inference/types"
)

// TestComputeTiming_UsesImminentPoCStart guards the fix that the countdown
// targets this epoch's PoC while we are still before it (pre-PoC inference
// phase), and only the following epoch's PoC once we are past it.
func TestComputeTiming_UsesImminentPoCStart(t *testing.T) {
	mk := func(height int64) *chainphase.EpochState {
		ec := types.NewEpochContext(
			types.Epoch{Index: 1, PocStartBlockHeight: 1000},
			types.EpochParams{EpochLength: 100},
		)
		return &chainphase.EpochState{
			LatestEpoch:  ec,
			CurrentBlock: chainphase.BlockInfo{Height: height},
			CurrentPhase: types.InferencePhase,
			IsSynced:     true,
		}
	}
	// Before this epoch's PoC (1000): count to 1000, not the next epoch.
	if got := ComputeTiming(mk(900)); got == nil || got.BlocksUntilNextPoC != 100 {
		t.Fatalf("pre-PoC: BlocksUntilNextPoC = %v, want 100", got)
	}
	// Past this epoch's PoC: count to the next epoch's PoC (1100).
	if got := ComputeTiming(mk(1050)); got == nil || got.BlocksUntilNextPoC != 50 {
		t.Fatalf("post-PoC: BlocksUntilNextPoC = %v, want 50", got)
	}
}
