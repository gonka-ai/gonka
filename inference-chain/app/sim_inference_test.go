//go:build sims

package app_test

import (
	"testing"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	simtypes "github.com/cosmos/cosmos-sdk/types/simulation"
	simcli "github.com/cosmos/cosmos-sdk/x/simulation/client/cli"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/app"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// TestFullSimulation_x_Inference_Integrated runs the smoke simulation and
// asserts — by inspecting keeper state after the run — that the Phase 2
// first-wave x/inference operations actually mutate chain state, not just
// get delivered as txs.
//
// Why state inspection rather than the simsx report: the `+++ DONE` report
// exposes a single combined `inference_completed` counter for all five
// inference ops and itemises only *skips*, so it cannot show per-op success.
// The keeper's Inferences collection is the ground truth.
//
// Asserted (one signal per op, each reachable only through that op's
// state-mutating path):
//   - StartInference  -> inferences with AssignedTo set (StartProcessed).
//   - FinishInference -> inferences with ExecutedBy set (FinishedProcessed,
//     types/inference.go).
//   - MsgValidation   -> inferences in VALIDATED status, set only by the
//     passing branch at msg_server_validation.go.
//
// Not asserted, by design:
//   - MsgSubmitNewParticipant is idempotent; sim accounts are already
//     genesis participants (BuildSimGenesisParticipants), so it takes the
//     no-op update path and leaves no distinguishing state.
//   - MsgClaimRewards runs against EffectiveEpochIndex=0, where the handler
//     returns a graceful response without mutating state
//     (msg_server_claim_rewards.go).
//
// Both ops are still exercised by the run — they have no state
// fingerprint to assert on.
//
// Seed dispatch mirrors TestFullAppSimulation. Run via `make sim-smoke-test`,
// which pins -Seed and -GenesisTime (see docs/simulation.md §Reproducibility).
func TestFullSimulation_x_Inference_Integrated(t *testing.T) {
	if !simcli.FlagEnabledValue {
		t.Skip("pass -Enabled=true to run this; e.g. via make sim-smoke-test")
	}
	cfg := simcli.NewConfigFromFlags()
	cfg.ChainID = simsx.SimAppChainID

	assertInferenceLifecycle := func(tb testing.TB, ti simsx.TestInstance[*app.App], _ []simtypes.Account) {
		tb.Helper()
		bApp := ti.App
		ctx := bApp.NewContextLegacy(true, cmtproto.Header{Height: bApp.LastBlockHeight()})

		infs, err := bApp.InferenceKeeper.GetAllInference(ctx)
		require.NoError(tb, err)

		var startProcessed, finishProcessed, validated int
		for _, inf := range infs {
			if inf.AssignedTo != "" {
				startProcessed++
			}
			if inf.ExecutedBy != "" {
				finishProcessed++
			}
			if inf.Status == inferencetypes.InferenceStatus_VALIDATED {
				validated++
			}
		}
		tb.Logf("x/inference lifecycle: total=%d startProcessed=%d finishProcessed=%d validated=%d",
			len(infs), startProcessed, finishProcessed, validated)

		require.Positivef(tb, startProcessed,
			"MsgStartInference never reached SetInference (no inference has AssignedTo set)")
		require.Positivef(tb, finishProcessed,
			"MsgFinishInference never reached state mutation (no inference has ExecutedBy set)")
		require.Positivef(tb, validated,
			"MsgValidation never reached its passing-branch state mutation (no VALIDATED inference)")
	}

	if cfg.Seed != simcli.DefaultSeedValue {
		simsx.RunWithSeed(t, cfg, NewSimApp, setupStateFactory, cfg.Seed, nil, assertInferenceLifecycle)
		return
	}
	simsx.Run(t, NewSimApp, setupStateFactory, assertInferenceLifecycle)
}
