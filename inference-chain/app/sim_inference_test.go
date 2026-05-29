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
//   - MsgValidation (failing branch) -> inferences with ProposalDetails
//     set, populated only by startValidationVoteWithPolicy after a
//     sub-threshold value drives the inference to VOTING and the two
//     x/group proposals are submitted (proves the F-1 group-membership
//     fix in bootstrap.go).
//   - MsgValidation (revalidation vote) -> inferences in INVALIDATED status,
//     reachable only when MsgRevalidationVoteFactory drives an x/group
//     invalidate proposal to quorum and the group module executes the inner
//     MsgInvalidateInference (msg_server_invalidate_inference.go).
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
// Seed dispatch mirrors TestFullAppSimulation. Run via `make sim-smoke-test`
// or `make sim-full-test`, which pin -Seed and -GenesisTime (see
// docs/simulation.md §Reproducibility).
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

		var startProcessed, finishProcessed, validated, proposed, invalidated int
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
			if inf.ProposalDetails != nil {
				proposed++
			}
			if inf.Status == inferencetypes.InferenceStatus_INVALIDATED {
				invalidated++
			}
		}
		tb.Logf("x/inference lifecycle: total=%d startProcessed=%d finishProcessed=%d validated=%d proposed=%d invalidated=%d",
			len(infs), startProcessed, finishProcessed, validated, proposed, invalidated)

		require.Positivef(tb, startProcessed,
			"MsgStartInference never reached SetInference (no inference has AssignedTo set)")
		require.Positivef(tb, finishProcessed,
			"MsgFinishInference never reached state mutation (no inference has ExecutedBy set)")
		require.Positivef(tb, validated,
			"MsgValidation never reached its passing-branch state mutation (no VALIDATED inference)")
		// proposed > 0 is the real-x/group verification of the failing
		// MsgValidation path: ProposalDetails is set only after both
		// group.SubmitProposal calls succeed. It holds for both the smoke
		// (50x20) and full (500x200) seed-99 configs this test runs under.
		// testkeeper cannot substitute: its x/group keeper is a gomock mock,
		// so the real SubmitProposal only runs under the full sim app.
		require.Positivef(tb, proposed,
			"failing MsgValidation never created x/group proposals (no inference has ProposalDetails) — "+
				"the failing-validation factory split or the F-1 group-membership fix is not working")
		// invalidated > 0 is the x/group *execution* check for the
		// revalidation path: INVALIDATED is reachable only by the inner
		// MsgInvalidateInference the group module dispatches once a
		// revalidation vote drives an invalidate proposal past the 50%
		// PercentageDecisionPolicy. That quorum must be reached within the
		// proposal's creation block — the sim's 5000-10000 s/block time step
		// closes the 4-minute group voting window before the next block. The
		// smoke config (50x20) delivers too few revalidation votes per block
		// to reach a 3-of-5 same-block quorum; only the full config
		// (500x200, make sim-full-test) does. So the assertion is gated on
		// the full config; the smoke run logs `invalidated` for visibility
		// but does not require it.
		if cfg.NumBlocks >= 500 && cfg.BlockSize >= 200 {
			require.Positivef(tb, invalidated,
				"no inference reached INVALIDATED — the revalidation-vote factory never drove an "+
					"x/group invalidate proposal to quorum/execution")
		}
	}

	if cfg.Seed != simcli.DefaultSeedValue {
		simsx.RunWithSeed(t, cfg, NewSimApp, setupStateFactory, cfg.Seed, nil, assertInferenceLifecycle, checkInferenceInvariants)
		return
	}
	simsx.Run(t, NewSimApp, setupStateFactory, assertInferenceLifecycle, checkInferenceInvariants)
}
