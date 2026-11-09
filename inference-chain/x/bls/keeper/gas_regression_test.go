package keeper

import (
	"bytes"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

// Gas-regression tests for the four sync-loop Set* functions. Each test
// seeds realistic N=16 sub-key state, then calls Set* with split fields
// nulled and asserts gas stays bounded (< 500k). A companion case calls
// the same Set* with fields populated and asserts the cost is notably
// higher, so a future refactor that removes the sync loop entirely
// doesn't silently make these tests pass trivially.

const (
	gasRegressionN                         = 16
	gasRegressionPayloadBytesPerDealerPart = 2048
	gasRegressionSignaturesPerParticipant  = 20
	gasRegressionBoundedCeiling            = storetypes.Gas(500_000)
	gasRegressionPopulatedFloor            = storetypes.Gas(750_000)
)

func makeDealerPart(address string) *types.DealerPartStorage {
	payload := make([]byte, gasRegressionPayloadBytesPerDealerPart/2)
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	commitments := make([][]byte, 8)
	for i := range commitments {
		commitments[i] = payload[:96]
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

func makeVerificationSubmission() *types.VerificationVectorSubmission {
	validity := make([]bool, gasRegressionN)
	for i := range validity {
		validity[i] = true
	}
	return &types.VerificationVectorSubmission{DealerValidity: validity}
}

func TestRequestThresholdSignature_SkipsDealerPartReads(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	const epochID = uint64(9)
	participants := make([]types.BLSParticipantInfo, gasRegressionN)
	for i := range participants {
		participants[i] = types.BLSParticipantInfo{Address: string(rune('a' + i))}
	}
	require.NoError(t, k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:        epochID,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1, 2, 3},
		Participants:   participants,
	}))
	for i := 0; i < gasRegressionN; i++ {
		require.NoError(t, k.SetDealerPart(ctx, epochID, uint32(i), makeDealerPart(participants[i].Address)))
	}

	fullCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	fullStart := fullCtx.GasMeter().GasConsumed()
	_, err := k.GetEpochBLSData(fullCtx, epochID)
	require.NoError(t, err)
	fullUsed := fullCtx.GasMeter().GasConsumed() - fullStart

	signCtx := ctx.WithBlockHeight(10).WithGasMeter(storetypes.NewInfiniteGasMeter())
	signStart := signCtx.GasMeter().GasConsumed()
	err = k.RequestThresholdSignature(signCtx, types.SigningData{
		CurrentEpochId: epochID,
		ChainId:        bytes.Repeat([]byte{1}, 32),
		RequestId:      bytes.Repeat([]byte{2}, 32),
		Data:           [][]byte{bytes.Repeat([]byte{3}, 32)},
	})
	require.NoError(t, err)
	signUsed := signCtx.GasMeter().GasConsumed() - signStart

	require.Less(t, signUsed, fullUsed/2,
		"signing request %d must stay under half of the full epoch load %d", signUsed, fullUsed)
}

func TestSubmitDealerPart_GasIndependentOfEarlierDealers(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	ctx = ctx.WithBlockHeight(10)
	participants := make([]types.BLSParticipantInfo, gasRegressionN)
	for i := range participants {
		participants[i] = types.BLSParticipantInfo{
			Address:        string(rune('a' + i)),
			SlotStartIndex: uint32(i),
			SlotEndIndex:   uint32(i),
		}
	}

	const emptyEpoch = uint64(20)
	require.NoError(t, k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:                   emptyEpoch,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 100,
		TSlotsDegree:              1,
		Participants:              participants,
	}))
	emptyUsed := submitDealerGas(t, k, ctx, dealerPartMsg(participants[0].Address, emptyEpoch, len(participants)))

	const seededEpoch = uint64(21)
	require.NoError(t, k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:                   seededEpoch,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 100,
		TSlotsDegree:              1,
		Participants:              participants,
	}))
	for i := 0; i < gasRegressionN-1; i++ {
		require.NoError(t, k.SetDealerPart(ctx, seededEpoch, uint32(i), makeDealerPart(participants[i].Address)))
	}

	fullCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	fullStart := fullCtx.GasMeter().GasConsumed()
	_, err := k.GetEpochBLSData(fullCtx, seededEpoch)
	require.NoError(t, err)
	fullUsed := fullCtx.GasMeter().GasConsumed() - fullStart

	seededUsed := submitDealerGas(t, k, ctx, dealerPartMsg(participants[gasRegressionN-1].Address, seededEpoch, len(participants)))

	require.Less(t, seededUsed, emptyUsed*2,
		"submit with %d earlier parts used %d, empty epoch used %d", gasRegressionN-1, seededUsed, emptyUsed)
	require.Less(t, seededUsed, fullUsed/2,
		"submit %d must stay under half of the full epoch load %d", seededUsed, fullUsed)
}

func dealerPartMsg(creator string, epochID uint64, participants int) *types.MsgSubmitDealerPart {
	cipher := make([]byte, types.MinEncryptedShareCiphertextLen)
	shares := make([]types.EncryptedSharesForParticipant, participants)
	for i := range shares {
		shares[i] = types.EncryptedSharesForParticipant{EncryptedShares: [][]byte{cipher}}
	}
	return &types.MsgSubmitDealerPart{
		Creator:                        creator,
		EpochId:                        epochID,
		Commitments:                    [][]byte{[]byte("c0"), []byte("c1")},
		EncryptedSharesForParticipants: shares,
	}
}

func submitDealerGas(t *testing.T, k Keeper, ctx sdk.Context, msg *types.MsgSubmitDealerPart) storetypes.Gas {
	t.Helper()
	metered := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start := metered.GasMeter().GasConsumed()
	_, err := NewMsgServerImpl(k).SubmitDealerPart(metered, msg)
	require.NoError(t, err)
	return metered.GasMeter().GasConsumed() - start
}

func TestSetEpochBLSData_GasBoundedWhenSplitFieldsNulled(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	const epochID = uint64(42)

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

	rehydrated, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	rehydrated.DkgPhase = types.DKGPhase_DKG_PHASE_SIGNED
	rehydrated.DealerParts = nil
	rehydrated.VerificationSubmissions = nil
	rehydrated.DealerComplaints = nil

	metered := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start := metered.GasMeter().GasConsumed()
	require.NoError(t, k.SetEpochBLSData(metered, rehydrated))
	used := metered.GasMeter().GasConsumed() - start

	require.Less(t, used, gasRegressionBoundedCeiling,
		"nulled-fields call must write only the base struct; got %d, ceiling %d", used, gasRegressionBoundedCeiling)

	// Sanity: populated-fields path must cost notably more, else the sync
	// loop has been removed and this test is silently trivial.
	rehydrated2, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	rehydrated2.DkgPhase = types.DKGPhase_DKG_PHASE_SIGNED
	metered2 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start2 := metered2.GasMeter().GasConsumed()
	require.NoError(t, k.SetEpochBLSData(metered2, rehydrated2))
	usedPopulated := metered2.GasMeter().GasConsumed() - start2

	require.Greater(t, usedPopulated, gasRegressionPopulatedFloor,
		"populated-fields call must exercise the sync loop; got %d, expected > %d", usedPopulated, gasRegressionPopulatedFloor)
	require.Greater(t, usedPopulated, used*10,
		"populated (%d) must cost >=10x nulled (%d) so the invariant remains visible", usedPopulated, used)
}

func TestSetGroupKeyValidationState_GasBoundedWhenPartialSignaturesNulled(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	const previousEpochID = uint64(1)
	const newEpochID = uint64(2)

	// Previous epoch needs participants so syncInlinePartialsToSubKeys can
	// resolve addr→index if it fires (it shouldn't on the nulled path).
	participants := make([]types.BLSParticipantInfo, gasRegressionN)
	for i := range participants {
		participants[i] = types.BLSParticipantInfo{Address: string(rune('A' + i))}
	}
	require.NoError(t, k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:      previousEpochID,
		Participants: participants,
	}))

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

	base := &types.GroupKeyValidationState{
		NewEpochId:      newEpochID,
		PreviousEpochId: previousEpochID,
		Status:          types.GroupKeyValidationStatus_GROUP_KEY_VALIDATION_STATUS_COLLECTING_SIGNATURES,
		SlotsCovered:    uint32(gasRegressionN * gasRegressionSignaturesPerParticipant),
	}
	require.NoError(t, k.SetGroupKeyValidationState(ctx, base))

	rehydrated, _, err := k.GetGroupKeyValidationState(ctx, newEpochID)
	require.NoError(t, err)
	rehydrated.SlotsCovered++
	rehydrated.PartialSignatures = nil

	metered := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start := metered.GasMeter().GasConsumed()
	require.NoError(t, k.SetGroupKeyValidationState(metered, rehydrated))
	used := metered.GasMeter().GasConsumed() - start

	require.Less(t, used, gasRegressionBoundedCeiling,
		"nulled-partials call must write only the base struct; got %d, ceiling %d", used, gasRegressionBoundedCeiling)

	rehydrated2, _, err := k.GetGroupKeyValidationState(ctx, newEpochID)
	require.NoError(t, err)
	rehydrated2.SlotsCovered++
	metered2 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start2 := metered2.GasMeter().GasConsumed()
	require.NoError(t, k.SetGroupKeyValidationState(metered2, rehydrated2))
	usedPopulated := metered2.GasMeter().GasConsumed() - start2

	require.Greater(t, usedPopulated, used*5,
		"populated (%d) must cost notably more than nulled (%d)", usedPopulated, used)
}

func TestStoreThresholdSigningRequest_GasBoundedWhenPartialSignaturesNulled(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	requestID := []byte("gas-regression-request")

	sigPayload := make([]byte, 48*gasRegressionSignaturesPerParticipant)
	for i := 0; i < gasRegressionN; i++ {
		addr := "submitter-" + string(rune('A'+i))
		require.NoError(t, k.SetThresholdPartialSignature(ctx, requestID, &types.PartialSignature{
			ParticipantAddress: addr,
			Signature:          sigPayload,
		}))
	}

	base := &types.ThresholdSigningRequest{
		RequestId: requestID,
		Status:    types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
	}
	require.NoError(t, k.storeThresholdSigningRequest(ctx, base))

	rehydrated, err := k.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	rehydrated.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED
	rehydrated.PartialSignatures = nil

	metered := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start := metered.GasMeter().GasConsumed()
	require.NoError(t, k.storeThresholdSigningRequest(metered, rehydrated))
	used := metered.GasMeter().GasConsumed() - start

	require.Less(t, used, gasRegressionBoundedCeiling,
		"nulled-partials call must write only the base struct; got %d, ceiling %d", used, gasRegressionBoundedCeiling)

	rehydrated2, err := k.GetSigningStatus(ctx, requestID)
	require.NoError(t, err)
	rehydrated2.Status = types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED
	metered2 := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	start2 := metered2.GasMeter().GasConsumed()
	require.NoError(t, k.storeThresholdSigningRequest(metered2, rehydrated2))
	usedPopulated := metered2.GasMeter().GasConsumed() - start2

	require.Greater(t, usedPopulated, used*3,
		"populated (%d) must cost notably more than nulled (%d)", usedPopulated, used)
}
