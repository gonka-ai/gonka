package keeper

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

// This file pins the invariant that's actually hard to get right:
//
//   A sync-loop Set* function (SetEpochBLSData,
//   SetGroupKeyValidationState, storeThresholdSigningRequest) MUST cost
//   gas proportional to the SIZE OF WHAT THE CALLER IS ACTUALLY
//   CHANGING, not proportional to the total number of sub-key entries
//   already in state.
//
// If the caller passes a rehydrated struct with its split fields
// populated, the Set*'s sync loop re-writes every sub-key — costing
// O(N × per-entry-size) gas. That's the regression that blew Maria's
// testnet-10 MsgSubmitGroupKeyValidationSignature with N=8 participants,
// each holding multi-KB dealer parts, past the 10M gas cap.
//
// These tests seed realistic-sized sub-key state (N=16 dealer parts with
// a few KB each, etc.) and then call the Set* with the split fields
// nulled — the fix pattern every hot-path handler now uses. Gas must be
// O(base struct size), NOT O(N × per-entry-size).
//
// Each test also has a companion sanity case that shows what a buggy
// (un-nulled) caller would cost. That companion isn't asserting the
// bug is allowed; it exists to pin the cost delta so a future refactor
// that accidentally removes the Set*'s sync loop doesn't silently make
// these tests meaningless (they'd all pass trivially if the sync loop
// vanished).

const (
	// Realistic-ish sizes for mainnet v0.2.12 BLS:
	// - 16 participants ≈ upper bound of current testnet + room to grow
	// - ~2KB per dealer part (polynomial commitments + encrypted shares)
	// - ~48 bytes per partial signature per slot
	gasRegressionN                        = 16
	gasRegressionPayloadBytesPerDealerPart = 2048
	gasRegressionSignaturesPerParticipant = 20

	// Bounded-gas threshold. A write of a small (~200 byte) base struct
	// costs a few tens of thousands of gas. Anything above 500k means
	// the sync loop fired; anything above a few million is the original
	// bug Maria hit. We use 500k as a comfortable ceiling that catches
	// regressions without being sensitive to SDK gas-cost tuning.
	gasRegressionBoundedCeiling = storetypes.Gas(500_000)

	// Minimum cost of the pathological (populated-slice) path. If the
	// populated-slice path costs LESS than this, the sync loop has
	// probably been removed or the test state is too small to exercise
	// it — either way these tests need recalibration rather than
	// silently passing.
	gasRegressionPopulatedFloor = storetypes.Gas(750_000)
)

// makeDealerPart builds a realistic-sized DealerPartStorage for gas tests.
func makeDealerPart(address string) *types.DealerPartStorage {
	payload := make([]byte, gasRegressionPayloadBytesPerDealerPart/2)
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	commitments := make([][]byte, 8)
	for i := range commitments {
		commitments[i] = payload[:96] // each commitment is a G2 point (~96 bytes compressed)
	}
	participantShares := make([]*types.EncryptedSharesForParticipant, gasRegressionN)
	for i := range participantShares {
		shares := make([][]byte, 4)
		for j := range shares {
			shares[j] = payload[:64]
		}
		participantShares[i] = &types.EncryptedSharesForParticipant{EncryptedShares: shares}
	}
	return &types.DealerPartStorage{
		DealerAddress:     address,
		Commitments:       commitments,
		ParticipantShares: participantShares,
	}
}

// makeVerificationSubmission builds a VerificationVectorSubmission with N validity bits.
func makeVerificationSubmission() *types.VerificationVectorSubmission {
	validity := make([]bool, gasRegressionN)
	for i := range validity {
		validity[i] = true
	}
	return &types.VerificationVectorSubmission{DealerValidity: validity}
}

func TestSetEpochBLSData_GasBoundedWhenSplitFieldsNulled(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	const epochID = uint64(42)

	// Seed N dealer parts + N verification submissions as sub-keys, then
	// write the base struct with split fields zeroed — the steady-state
	// the hot-path handlers produce.
	participants := make([]types.BLSParticipantInfo, gasRegressionN)
	for i := range participants {
		participants[i] = types.BLSParticipantInfo{Address: string(rune('a' + i))}
	}
	base := types.EpochBLSData{
		EpochId:      epochID,
		Participants: participants,
		DkgPhase:     types.DKGPhase_DKG_PHASE_VERIFYING,
	}
	require.NoError(t, k.SetEpochBLSData(ctx, base))
	for i := 0; i < gasRegressionN; i++ {
		require.NoError(t, k.SetDealerPart(ctx, epochID, uint32(i), makeDealerPart(participants[i].Address)))
		require.NoError(t, k.SetVerificationSubmission(ctx, epochID, uint32(i), makeVerificationSubmission()))
	}

	// Rehydrate — this is what the handler does.
	rehydrated, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)

	// Mutate one base field, then null the split fields — the pattern
	// msg_server_group_validation.go:227 / msg_server_verifier.go:152-157
	// / every phase_transitions.go Set site now uses.
	rehydrated.DkgPhase = types.DKGPhase_DKG_PHASE_SIGNED
	rehydrated.DealerParts = nil
	rehydrated.VerificationSubmissions = nil
	rehydrated.DealerComplaints = nil

	metered := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start := metered.GasMeter().GasConsumed()
	require.NoError(t, k.SetEpochBLSData(metered, rehydrated))
	used := metered.GasMeter().GasConsumed() - start

	require.Less(t, used, gasRegressionBoundedCeiling,
		"SetEpochBLSData with nulled split fields must write only the small base struct; got %d gas (ceiling %d). If this fails, a caller is passing a rehydrated struct without nulling DealerParts/VerificationSubmissions/DealerComplaints first.", used, gasRegressionBoundedCeiling)

	// Sanity: the populated-slice path MUST cost more, or the sync loop
	// has regressed to a no-op and this test is silently trivial.
	rehydrated2, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	rehydrated2.DkgPhase = types.DKGPhase_DKG_PHASE_SIGNED
	metered2 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start2 := metered2.GasMeter().GasConsumed()
	require.NoError(t, k.SetEpochBLSData(metered2, rehydrated2))
	usedPopulated := metered2.GasMeter().GasConsumed() - start2

	require.Greater(t, usedPopulated, gasRegressionPopulatedFloor,
		"SetEpochBLSData's sync loop must cost real gas when called with populated split fields (got %d, expected > %d). If this is too low, either the sync loop has been removed or the per-entry payload is too small — recalibrate the test.", usedPopulated, gasRegressionPopulatedFloor)

	// Delta check: populated should be at least 10x nulled. Catches
	// cases where both paths drift toward each other (e.g., the sync
	// loop is changed to a no-op for already-existing sub-keys).
	require.Greater(t, usedPopulated, used*10,
		"populated-slice call (%d gas) must cost at least 10x the nulled-slice call (%d gas) — otherwise the sync-loop invariant has become invisible to future regressions", usedPopulated, used)
}

func TestSetGroupKeyValidationState_GasBoundedWhenPartialSignaturesNulled(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	const previousEpochID = uint64(1)
	const newEpochID = uint64(2)

	// Seed the previous epoch's participants so syncInlinePartialsToSubKeys
	// can resolve addr→index (defensive: the nulled path should never
	// invoke syncInlinePartialsToSubKeys).
	participants := make([]types.BLSParticipantInfo, gasRegressionN)
	for i := range participants {
		participants[i] = types.BLSParticipantInfo{Address: string(rune('A' + i))}
	}
	require.NoError(t, k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:      previousEpochID,
		Participants: participants,
	}))

	// Seed N sub-keyed partial signatures via the single-entry Set.
	sig := make([]byte, 48*gasRegressionSignaturesPerParticipant)
	for i := range sig {
		sig[i] = byte(i % 256)
	}
	slots := make([]uint32, gasRegressionSignaturesPerParticipant)
	for i := range slots {
		slots[i] = uint32(i)
	}
	for i := 0; i < gasRegressionN; i++ {
		require.NoError(t, k.SetGroupValidationPartialSignature(ctx, newEpochID, uint32(i), &types.PartialSignature{
			ParticipantAddress: participants[i].Address,
			SlotIndices:        slots,
			Signature:          sig,
		}))
	}

	// Write the base state separately via SetGroupKeyValidationState
	// with nil partials — the normal handler path.
	base := &types.GroupKeyValidationState{
		NewEpochId:      newEpochID,
		PreviousEpochId: previousEpochID,
		Status:          types.GroupKeyValidationStatus_GROUP_KEY_VALIDATION_STATUS_COLLECTING_SIGNATURES,
		SlotsCovered:    uint32(gasRegressionN * gasRegressionSignaturesPerParticipant),
	}
	require.NoError(t, k.SetGroupKeyValidationState(ctx, base))

	// Rehydrate — what the hot-path handler does.
	rehydrated, _, err := k.GetGroupKeyValidationState(ctx, newEpochID)
	require.NoError(t, err)

	// Hot-path pattern: modify a base field, null PartialSignatures, Set.
	rehydrated.SlotsCovered++
	rehydrated.PartialSignatures = nil

	metered := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start := metered.GasMeter().GasConsumed()
	require.NoError(t, k.SetGroupKeyValidationState(metered, rehydrated))
	used := metered.GasMeter().GasConsumed() - start

	require.Less(t, used, gasRegressionBoundedCeiling,
		"SetGroupKeyValidationState with nulled PartialSignatures must write only the small base struct; got %d gas (ceiling %d). If this fails, a caller is passing a rehydrated state without nulling PartialSignatures first — the exact regression Maria hit at testnet-10 block 5164.", used, gasRegressionBoundedCeiling)

	// Sanity check: populated-slice path costs real gas.
	rehydrated2, _, err := k.GetGroupKeyValidationState(ctx, newEpochID)
	require.NoError(t, err)
	rehydrated2.SlotsCovered++
	metered2 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start2 := metered2.GasMeter().GasConsumed()
	require.NoError(t, k.SetGroupKeyValidationState(metered2, rehydrated2))
	usedPopulated := metered2.GasMeter().GasConsumed() - start2

	require.Greater(t, usedPopulated, used*5,
		"populated PartialSignatures call (%d gas) must cost more than nulled call (%d gas) by a noticeable factor", usedPopulated, used)
}

func TestStoreThresholdSigningRequest_GasBoundedWhenPartialSignaturesNulled(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	requestID := []byte("gas-regression-request")

	// Seed N sub-keyed partial signatures.
	sigPayload := make([]byte, 48*gasRegressionSignaturesPerParticipant)
	for i := 0; i < gasRegressionN; i++ {
		addr := "submitter-" + string(rune('A'+i))
		require.NoError(t, k.SetThresholdPartialSignature(ctx, requestID, &types.PartialSignature{
			ParticipantAddress: addr,
			Signature:          sigPayload,
		}))
	}

	// Persist the base request with nil partials — steady state.
	base := &types.ThresholdSigningRequest{
		RequestId: requestID,
		Status:    types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
	}
	require.NoError(t, k.storeThresholdSigningRequest(ctx, base))

	// Rehydrate via GetSigningStatus (what the hot paths do).
	rehydrated, err := k.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)

	// Hot-path pattern (checkThresholdAndAggregate,
	// finalizeFailedThresholdSigningRequest, CancelThresholdSignature,
	// AddPartialSignature): null PartialSignatures before Set.
	rehydrated.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED
	rehydrated.PartialSignatures = nil

	metered := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start := metered.GasMeter().GasConsumed()
	require.NoError(t, k.storeThresholdSigningRequest(metered, rehydrated))
	used := metered.GasMeter().GasConsumed() - start

	require.Less(t, used, gasRegressionBoundedCeiling,
		"storeThresholdSigningRequest with nulled PartialSignatures must write only the small base struct; got %d gas (ceiling %d).", used, gasRegressionBoundedCeiling)

	// Sanity check: populated path costs real gas.
	rehydrated2, err := k.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	rehydrated2.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED
	metered2 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start2 := metered2.GasMeter().GasConsumed()
	require.NoError(t, k.storeThresholdSigningRequest(metered2, rehydrated2))
	usedPopulated := metered2.GasMeter().GasConsumed() - start2

	require.Greater(t, usedPopulated, used*3,
		"populated PartialSignatures call (%d gas) must cost more than nulled call (%d gas); if this fails, the sync loop is no longer exercised", usedPopulated, used)
}
