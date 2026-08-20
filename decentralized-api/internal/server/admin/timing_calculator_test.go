package admin

import (
	"decentralized-api/apiconfig"
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

// epochStateAt builds a synced inference-phase state at height, optionally with
// an active confirmation PoC event.
func epochStateAt(height int64, ev *types.ConfirmationPoCEvent) *chainphase.EpochState {
	ec := types.NewEpochContext(
		types.Epoch{Index: 1, PocStartBlockHeight: 100000},
		types.EpochParams{
			EpochLength:           100000,
			PocStageDuration:      10,
			PocExchangeDuration:   5,
			PocValidationDelay:    1,
			PocValidationDuration: 10,
		},
	)
	return &chainphase.EpochState{
		LatestEpoch:                ec,
		CurrentBlock:               chainphase.BlockInfo{Height: height},
		CurrentPhase:               types.InferencePhase,
		IsSynced:                   true,
		ActiveConfirmationPoCEvent: ev,
	}
}

// TestComputeTiming_ConfirmationPoC guards that an out-of-band confirmation PoC
// is not invisible to the timing helpers. Before the fix the epoch's own PoC was
// hours away, so a confirmation PoC generating right now reported plenty of
// slack and a multi-minute MLnode test was allowed to start.
func TestComputeTiming_ConfirmationPoC(t *testing.T) {
	t.Run("inside the confirmation generation window", func(t *testing.T) {
		ev := &types.ConfirmationPoCEvent{GenerationStartHeight: 500}
		got := ComputeTiming(epochStateAt(505, ev))
		if got == nil {
			t.Fatal("nil timing")
		}
		if !got.InPoCWindow {
			t.Error("InPoCWindow should be true during a confirmation PoC generation window")
		}
		if !got.ShouldBeOnline {
			t.Error("ShouldBeOnline should be true during a confirmation PoC")
		}
		if !got.ConfirmationPoCActive {
			t.Error("ConfirmationPoCActive should be true")
		}
	})

	t.Run("countdown targets the confirmation PoC when it comes first", func(t *testing.T) {
		ev := &types.ConfirmationPoCEvent{GenerationStartHeight: 600}
		got := ComputeTiming(epochStateAt(500, ev))
		if got == nil {
			t.Fatal("nil timing")
		}
		if got.BlocksUntilNextPoC != 100 {
			t.Errorf("BlocksUntilNextPoC = %d, want 100 (confirmation PoC, not the epoch's own)", got.BlocksUntilNextPoC)
		}
		if got.InPoCWindow {
			t.Error("InPoCWindow should be false before the confirmation window opens")
		}
	})

	t.Run("a completed confirmation PoC is ignored", func(t *testing.T) {
		ev := &types.ConfirmationPoCEvent{GenerationStartHeight: 100}
		got := ComputeTiming(epochStateAt(5000, ev))
		if got == nil {
			t.Fatal("nil timing")
		}
		if got.ConfirmationPoCActive {
			t.Error("a finished confirmation PoC must not drive the countdown")
		}
		if got.BlocksUntilNextPoC != 95000 {
			t.Errorf("BlocksUntilNextPoC = %d, want the epoch's own PoC", got.BlocksUntilNextPoC)
		}
	})
}

// TestPoCTestBlockReason covers the shared test-safety schedule gate, including
// the 601s boundary the old ShouldBeOnline-only check let through and the
// fail-closed behavior on an unknown schedule.
func TestPoCTestBlockReason(t *testing.T) {
	// One block = 6s, so heights are chosen to land on exact second counts.
	blocksFor := func(seconds int64) int64 { return seconds / 6 }

	t.Run("unsynced state blocks", func(t *testing.T) {
		es := epochStateAt(1, nil)
		es.IsSynced = false
		if got := pocTestBlockReason(es, 0); got == "" {
			t.Error("an unsynced tracker must block, not fail open")
		}
	})

	t.Run("nil state blocks", func(t *testing.T) {
		if got := pocTestBlockReason(nil, 0); got == "" {
			t.Error("a nil epoch state must block, not fail open")
		}
	})

	t.Run("active PoC window blocks", func(t *testing.T) {
		es := epochStateAt(100005, nil)
		es.CurrentPhase = types.PoCGeneratePhase
		if got := pocTestBlockReason(es, 0); got == "" {
			t.Error("an active PoC phase must block")
		}
	})

	t.Run("601 seconds out is blocked for a manual test", func(t *testing.T) {
		// The old gate allowed this: outside the 600s must-be-online window, but
		// a 300s test started here would still be running when the node is
		// expected online.
		es := epochStateAt(100000-blocksFor(601), nil)
		if got := pocTestBlockReason(es, apiconfig.ManualTestMinSecondsBeforePoC); got == "" {
			t.Error("601s before PoC must block a manual test (test can run 300s)")
		}
	})

	t.Run("beyond the manual floor is allowed", func(t *testing.T) {
		es := epochStateAt(100000-blocksFor(apiconfig.ManualTestMinSecondsBeforePoC+60), nil)
		if got := pocTestBlockReason(es, apiconfig.ManualTestMinSecondsBeforePoC); got != "" {
			t.Errorf("should be allowed, got %q", got)
		}
	})

	t.Run("auto-test needs a much wider margin", func(t *testing.T) {
		// Fine for a manual test, far too close for an automatic one.
		es := epochStateAt(100000-blocksFor(apiconfig.ManualTestMinSecondsBeforePoC+60), nil)
		if got := pocTestBlockReason(es, apiconfig.AutoTestMinSecondsBeforePoC); got == "" {
			t.Error("auto-test must require AutoTestMinSecondsBeforePoC of slack")
		}
	})

	// The threshold is a floor, not a ceiling: exactly the required slack is not
	// enough. These assertions previously lived on a separate shouldAutoTest
	// helper, which production no longer consults.
	t.Run("the minimum slack is exclusive", func(t *testing.T) {
		exactly := epochStateAt(100000-blocksFor(apiconfig.AutoTestMinSecondsBeforePoC), nil)
		if got := pocTestBlockReason(exactly, apiconfig.AutoTestMinSecondsBeforePoC); got != "" {
			t.Errorf("exactly the threshold should be allowed, got %q", got)
		}
		oneBlockCloser := epochStateAt(100000-blocksFor(apiconfig.AutoTestMinSecondsBeforePoC)+1, nil)
		if got := pocTestBlockReason(oneBlockCloser, apiconfig.AutoTestMinSecondsBeforePoC); got == "" {
			t.Error("one block inside the threshold must be refused")
		}
	})

	t.Run("PoC already started blocks", func(t *testing.T) {
		// Past this epoch's PoC start: the countdown points at the next epoch, but
		// the phase says PoC, so the window check must still refuse.
		es := epochStateAt(100001, nil)
		es.CurrentPhase = types.PoCGenerateWindDownPhase
		if got := pocTestBlockReason(es, 0); got == "" {
			t.Error("a wind-down PoC phase must block")
		}
	})
}
