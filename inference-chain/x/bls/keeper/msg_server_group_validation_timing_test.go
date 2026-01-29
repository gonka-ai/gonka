package keeper

import (
	"fmt"
	"math/big"
	"testing"
	"time"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/consensys/gnark-crypto/ecc"
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

func TestSubmitGroupKeyValidationSignature_Timing(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped with -short")
	}

	k, ctx := setupTimingKeeper(t)
	ctx = ctx.WithChainID("timing-test-chain")
	goCtx := sdk.WrapSDKContext(ctx)

	totalSlots := uint32(100)
	numParticipants := 100
	commitmentCount := int(totalSlots/2) + 1
	participants := buildTimingParticipants(totalSlots, numParticipants)
	var start time.Time

	var newEpochSk fr.Element
	newEpochSk.SetUint64(9)
	_, _, _, g2Gen := bls12381.Generators()
	newGroupKey := g2BytesFromScalar(g2Gen, newEpochSk)

	t.Logf("setup: slots=%d participants=%d dealers=%d commitments_per_dealer=%d", totalSlots, numParticipants, numParticipants, commitmentCount)

	start = time.Now()
	dealerCoeffs, totalCoeffs := buildDealerCoefficients(numParticipants, commitmentCount)
	t.Logf("build dealer coefficients: %s", time.Since(start))

	start = time.Now()
	dealerParts := make([]*types.DealerPartStorage, numParticipants)
	validDealers := make([]bool, numParticipants)
	for i := range dealerParts {
		commitments := make([][]byte, commitmentCount)
		for j := 0; j < commitmentCount; j++ {
			commitments[j] = g2BytesFromScalar(g2Gen, dealerCoeffs[i][j])
		}
		dealerParts[i] = &types.DealerPartStorage{
			DealerAddress:     participants[i].Address,
			Commitments:       commitments,
			ParticipantShares: []*types.EncryptedSharesForParticipant{},
		}
		validDealers[i] = true
	}
	t.Logf("build dealer commitments: %s", time.Since(start))

	prevGroupKey := g2BytesFromScalar(g2Gen, totalCoeffs[0])

	// Sanity: MSM and naive evaluation match for a real slot/dealer.
	naiveEval, _, err := evaluateCommitmentPolynomialWithTimings(dealerParts[0].Commitments, 0)
	require.NoError(t, err)
	msmEval, _, err := evaluateCommitmentPolynomialWithMSM(dealerParts[0].Commitments, 0)
	require.NoError(t, err)
	require.Equal(t, naiveEval.Bytes(), msmEval.Bytes())

	previousEpoch := types.EpochBLSData{
		EpochId:        1,
		ITotalSlots:    totalSlots,
		TSlotsDegree:   uint32(commitmentCount - 1),
		Participants:   participants,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: prevGroupKey,
		DealerParts:    dealerParts,
		ValidDealers:   validDealers,
	}

	// Precompute slot public keys for the optimized path
	start = time.Now()
	slotPKs, err := k.PrecomputeSlotPublicKeys(&previousEpoch)
	require.NoError(t, err)
	previousEpoch.SlotPublicKeys = slotPKs
	t.Logf("precompute slot public keys: %s", time.Since(start))

	newEpoch := types.EpochBLSData{
		EpochId:        2,
		ITotalSlots:    totalSlots,
		TSlotsDegree:   totalSlots / 2,
		DkgPhase:       types.DKGPhase_DKG_PHASE_COMPLETED,
		GroupPublicKey: newGroupKey,
	}

	k.SetEpochBLSData(ctx, previousEpoch)
	k.SetEpochBLSData(ctx, newEpoch)

	ms := msgServer{Keeper: k}

	start = time.Now()
	messageHash, err := ms.computeValidationMessageHash(ctx, newEpoch.GroupPublicKey, previousEpoch.EpochId, newEpoch.EpochId)
	require.NoError(t, err)
	t.Logf("computeValidationMessageHash: %s", time.Since(start))

	start = time.Now()
	messageG1, err := k.hashToG1(messageHash)
	require.NoError(t, err)
	t.Logf("hashToG1: %s", time.Since(start))

	start = time.Now()
	slotScalars := computeSlotScalars(totalCoeffs, totalSlots)
	t.Logf("compute slot scalars: %s", time.Since(start))

	start = time.Now()
	slotSignatures := make([][]byte, totalSlots)
	for slotIndex := uint32(0); slotIndex < totalSlots; slotIndex++ {
		slotSignatures[slotIndex] = g1SignatureFromScalar(messageG1, slotScalars[slotIndex])
	}
	t.Logf("compute slot signatures: %s", time.Since(start))

	start = time.Now()
	slotLists := make([][]uint32, numParticipants)
	signaturePayloads := make([][]byte, numParticipants)
	for i, participant := range participants {
		slots := make([]uint32, 0, int(participant.SlotEndIndex-participant.SlotStartIndex+1))
		for slot := participant.SlotStartIndex; slot <= participant.SlotEndIndex; slot++ {
			slots = append(slots, slot)
		}
		slotLists[i] = slots

		payload := make([]byte, 0, len(slots)*48)
		for _, slot := range slots {
			payload = append(payload, slotSignatures[slot]...)
		}
		signaturePayloads[i] = payload
	}
	t.Logf("build slot lists + payloads: %s", time.Since(start))

	start = time.Now()
	ok, firstStats := verifyBLSPartialSignatureWithTimings(k, signaturePayloads[0], messageHash, &previousEpoch, slotLists[0])
	require.True(t, ok)
	t.Logf("verifyBLSPartialSignature (first): %s", time.Since(start))
	logVerifyStats(t, "first", firstStats)

	start = time.Now()
	ok, singleStats := verifyBLSPartialSignatureWithTimings(k, signaturePayloads[0][:48], messageHash, &previousEpoch, slotLists[0][:1])
	require.True(t, ok)
	t.Logf("verifyBLSPartialSignature (single slot): %s", time.Since(start))
	logVerifyStats(t, "single_slot", singleStats)

	start = time.Now()
	ok, singleMsmStats := verifyBLSPartialSignatureWithTimingsMSM(k, signaturePayloads[0][:48], messageHash, &previousEpoch, slotLists[0][:1])
	require.True(t, ok)
	t.Logf("verifyBLSPartialSignature (single slot, msm): %s", time.Since(start))
	logVerifyStatsMSM(t, "single_slot_msm", singleMsmStats)

	singleSlotMsg := &types.MsgSubmitGroupKeyValidationSignature{
		Creator:          participants[0].Address,
		NewEpochId:       newEpoch.EpochId,
		SlotIndices:      slotLists[0][:1],
		PartialSignature: signaturePayloads[0][:48],
	}
	start = time.Now()
	_, err = ms.SubmitGroupKeyValidationSignature(goCtx, singleSlotMsg)
	require.NoError(t, err)
	t.Logf("SubmitGroupKeyValidationSignature (single slot): %s", time.Since(start))

	requiredSlots := previousEpoch.ITotalSlots/2 + 1
	slotsCovered := uint32(1)
	perCallDurations := make([]time.Duration, 0, numParticipants)
	totalStart := time.Now()
	for i := 1; i < numParticipants; i++ {
		if slotsCovered+uint32(len(slotLists[i])) >= requiredSlots {
			break
		}

		msg := &types.MsgSubmitGroupKeyValidationSignature{
			Creator:          participants[i].Address,
			NewEpochId:       newEpoch.EpochId,
			SlotIndices:      slotLists[i],
			PartialSignature: signaturePayloads[i],
		}
		callStart := time.Now()
		_, err := ms.SubmitGroupKeyValidationSignature(goCtx, msg)
		require.NoError(t, err)
		perCallDurations = append(perCallDurations, time.Since(callStart))

		slotsCovered += uint32(len(slotLists[i]))
	}
	totalDuration := time.Since(totalStart)

	var maxDuration time.Duration
	var sumDuration time.Duration
	for _, d := range perCallDurations {
		sumDuration += d
		if d > maxDuration {
			maxDuration = d
		}
	}
	avgDuration := time.Duration(0)
	if len(perCallDurations) > 0 {
		avgDuration = sumDuration / time.Duration(len(perCallDurations))
	}

	t.Logf(
		"SubmitGroupKeyValidationSignature (partial): total=%s avg=%s max=%s submissions=%d slots=%d required=%d",
		totalDuration,
		avgDuration,
		maxDuration,
		len(perCallDurations),
		slotsCovered,
		requiredSlots,
	)

	// Collect enough signatures to reach the threshold and verify aggregation/final signature
	for i := len(perCallDurations) + 1; i < numParticipants; i++ {
		msg := &types.MsgSubmitGroupKeyValidationSignature{
			Creator:          participants[i].Address,
			NewEpochId:       newEpoch.EpochId,
			SlotIndices:      slotLists[i],
			PartialSignature: signaturePayloads[i],
		}
		_, err := ms.SubmitGroupKeyValidationSignature(goCtx, msg)
		require.NoError(t, err)
		slotsCovered += uint32(len(slotLists[i]))

		if slotsCovered >= requiredSlots {
			break
		}
	}

	// Verify that the new epoch is now in SIGNED phase
	storedNewEpoch, err := k.GetEpochBLSData(ctx, newEpoch.EpochId)
	require.NoError(t, err)
	require.Equal(t, types.DKGPhase_DKG_PHASE_SIGNED, storedNewEpoch.DkgPhase)
	require.NotEmpty(t, storedNewEpoch.ValidationSignature)
	t.Logf("Final aggregated signature verified successfully! Epoch transitioned to SIGNED phase.")
}

func TestPrecomputeSlotPublicKeys_TimingComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped with -short")
	}

	k, _ := setupTimingKeeper(t)

	// Test with 100 slots
	totalSlots := uint32(100)
	numParticipants := 100
	commitmentCount := int(totalSlots/2) + 1 // t = 51
	participants := buildTimingParticipants(totalSlots, numParticipants)
	_, _, _, g2Gen := bls12381.Generators()

	t.Logf("Timing comparison: slots=%d participants=%d commitments_per_dealer=%d", totalSlots, numParticipants, commitmentCount)

	dealerCoeffs, totalCoeffs := buildDealerCoefficients(numParticipants, commitmentCount)
	dealerParts := make([]*types.DealerPartStorage, numParticipants)
	validDealers := make([]bool, numParticipants)
	for i := range dealerParts {
		commitments := make([][]byte, commitmentCount)
		for j := 0; j < commitmentCount; j++ {
			commitments[j] = g2BytesFromScalar(g2Gen, dealerCoeffs[i][j])
		}
		dealerParts[i] = &types.DealerPartStorage{
			DealerAddress: participants[i].Address,
			Commitments:   commitments,
		}
		validDealers[i] = true
	}

	prevGroupKey := g2BytesFromScalar(g2Gen, totalCoeffs[0])

	epochData := types.EpochBLSData{
		EpochId:        1,
		ITotalSlots:    totalSlots,
		TSlotsDegree:   uint32(commitmentCount - 1),
		Participants:   participants,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: prevGroupKey,
		DealerParts:    dealerParts,
		ValidDealers:   validDealers,
	}

	// 1. Measure PrecomputeSlotPublicKeys (gnark-crypto)
	startGnark := time.Now()
	resGnark, err := k.PrecomputeSlotPublicKeys(&epochData)
	durationGnark := time.Since(startGnark)
	require.NoError(t, err)

	// 2. Measure PrecomputeSlotPublicKeysBlst (blst)
	startBlst := time.Now()
	resBlst, err := k.PrecomputeSlotPublicKeysBlst(&epochData)
	durationBlst := time.Since(startBlst)
	require.NoError(t, err)

	// 3. Compare results
	require.Equal(t, len(resGnark), len(resBlst), "Result lengths must match")
	for i := range resGnark {
		require.Equal(t, resGnark[i], resBlst[i], "Result at slot %d must match exactly", i)
	}

	t.Logf("PrecomputeSlotPublicKeys (gnark-crypto): %s", durationGnark)
	t.Logf("PrecomputeSlotPublicKeysBlst (blst):      %s", durationBlst)
	if durationBlst < durationGnark {
		improvement := float64(durationGnark-durationBlst) / float64(durationGnark) * 100
		t.Logf("blst is %.2f%% faster", improvement)
	} else {
		t.Logf("gnark-crypto is faster (unexpected for large MSM)")
	}
}

func TestVerifyBLSPartialSignature_TimingComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped with -short")
	}

	k, _ := setupTimingKeeper(t)

	totalSlots := uint32(100)
	numParticipants := 100
	commitmentCount := int(totalSlots/2) + 1
	participants := buildTimingParticipants(totalSlots, numParticipants)
	_, _, _, g2Gen := bls12381.Generators()

	dealerCoeffs, totalCoeffs := buildDealerCoefficients(numParticipants, commitmentCount)
	dealerParts := make([]*types.DealerPartStorage, numParticipants)
	validDealers := make([]bool, numParticipants)
	for i := range dealerParts {
		commitments := make([][]byte, commitmentCount)
		for j := 0; j < commitmentCount; j++ {
			commitments[j] = g2BytesFromScalar(g2Gen, dealerCoeffs[i][j])
		}
		dealerParts[i] = &types.DealerPartStorage{
			DealerAddress: participants[i].Address,
			Commitments:   commitments,
		}
		validDealers[i] = true
	}

	prevGroupKey := g2BytesFromScalar(g2Gen, totalCoeffs[0])

	epochData := types.EpochBLSData{
		EpochId:        1,
		ITotalSlots:    totalSlots,
		TSlotsDegree:   uint32(commitmentCount - 1),
		Participants:   participants,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: prevGroupKey,
		DealerParts:    dealerParts,
		ValidDealers:   validDealers,
	}

	// Precompute slot public keys
	slotPKs, err := k.PrecomputeSlotPublicKeys(&epochData)
	require.NoError(t, err)
	epochData.SlotPublicKeys = slotPKs

	// Prepare a signature for some slots (32-byte message hash)
	messageHash := make([]byte, 32)
	copy(messageHash, "test message")
	messageG1, err := k.hashToG1(messageHash)
	require.NoError(t, err)

	// Use participant 0's slots
	p0 := participants[0]
	numSlots := int(p0.SlotEndIndex - p0.SlotStartIndex + 1)
	slotIndices := make([]uint32, numSlots)
	for i := 0; i < numSlots; i++ {
		slotIndices[i] = p0.SlotStartIndex + uint32(i)
	}

	// For each slot, compute a valid signature
	slotScalars := computeSlotScalars(totalCoeffs, totalSlots)
	signaturePayload := make([]byte, 0, numSlots*48)
	for _, slotIdx := range slotIndices {
		sig := g1SignatureFromScalar(messageG1, slotScalars[slotIdx])
		signaturePayload = append(signaturePayload, sig...)
	}

	// 1. Measure verifyBLSPartialSignature (gnark-crypto)
	startGnark := time.Now()
	okGnark := k.verifyBLSPartialSignature(signaturePayload, messageHash, &epochData, slotIndices)
	durationGnark := time.Since(startGnark)
	require.True(t, okGnark)

	// 2. Measure verifyBLSPartialSignatureBlst (blst)
	startBlst := time.Now()
	okBlst := k.verifyBLSPartialSignatureBlst(signaturePayload, messageHash, &epochData, slotIndices)
	durationBlst := time.Since(startBlst)
	require.True(t, okBlst)

	t.Logf("verifyBLSPartialSignature (gnark-crypto): %s", durationGnark)
	t.Logf("verifyBLSPartialSignatureBlst (blst):      %s", durationBlst)
	if durationBlst < durationGnark {
		improvement := float64(durationGnark-durationBlst) / float64(durationGnark) * 100
		t.Logf("blst is %.2f%% faster", improvement)
	}
}

func TestAggregateBLSPartialSignatures_Timing(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped with -short")
	}

	k, _ := setupTimingKeeper(t)

	totalSlots := uint32(100)
	numParticipants := 100
	commitmentCount := int(totalSlots/2) + 1

	// Setup mock data for coefficients
	_, totalCoeffs := buildDealerCoefficients(numParticipants, commitmentCount)

	// Prepare message hash and G1 point
	messageHash := make([]byte, 32)
	copy(messageHash, "aggregation test message")
	messageG1, err := k.hashToG1(messageHash)
	require.NoError(t, err)

	// Prepare 51 slots (threshold) for aggregation
	requiredSlots := uint32(51)
	slotScalars := computeSlotScalars(totalCoeffs, totalSlots)

	partialSignatures := make([]types.PartialSignature, 0, requiredSlots)
	for i := uint32(0); i < requiredSlots; i++ {
		sig := g1SignatureFromScalar(messageG1, slotScalars[i])
		partialSignatures = append(partialSignatures, types.PartialSignature{
			Signature:   sig,
			SlotIndices: []uint32{i},
		})
	}

	t.Logf("Timing aggregation of %d partial signatures (each for 1 slot)", len(partialSignatures))

	// Measure gnark-crypto aggregation time
	start := time.Now()
	aggregatedSig, err := k.aggregateBLSPartialSignatures(partialSignatures)
	duration := time.Since(start)
	require.NoError(t, err)
	require.NotNil(t, aggregatedSig)

	t.Logf("aggregateBLSPartialSignatures (gnark-crypto): %s", duration)

	// Measure blst aggregation time
	startBlst := time.Now()
	aggregatedSigBlst, err := k.aggregateBLSPartialSignaturesBlst(partialSignatures)
	durationBlst := time.Since(startBlst)
	require.NoError(t, err)
	require.NotNil(t, aggregatedSigBlst)

	// Compare results
	require.Equal(t, aggregatedSig, aggregatedSigBlst, "Aggregated signatures must match exactly")

	t.Logf("aggregateBLSPartialSignaturesBlst (blst):      %s", durationBlst)
	if durationBlst < duration {
		improvement := float64(duration-durationBlst) / float64(duration) * 100
		t.Logf("blst is %.2f%% faster", improvement)
	}
}

func TestVerifyFinalSignature_TimingComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped with -short")
	}

	k, _ := setupTimingKeeper(t)

	// Prepare data
	messageHash := make([]byte, 32)
	copy(messageHash, "final signature test")

	_, _, _, g2Gen := bls12381.Generators()
	var sk fr.Element
	sk.SetRandom()

	var pk bls12381.G2Affine
	pk.ScalarMultiplication(&g2Gen, sk.BigInt(new(big.Int)))
	pkBytes := pk.Bytes()

	messageG1, _ := k.hashToG1(messageHash)
	var sig bls12381.G1Affine
	sig.ScalarMultiplication(&messageG1, sk.BigInt(new(big.Int)))
	sigArr := sig.Bytes()
	sigBytes := sigArr[:]

	// 1. Measure gnark
	startGnark := time.Now()
	okGnark := k.verifyFinalSignature(sigBytes, messageHash, pkBytes[:])
	durationGnark := time.Since(startGnark)
	require.True(t, okGnark)

	// 2. Measure blst
	startBlst := time.Now()
	okBlst := k.verifyFinalSignatureBlst(sigBytes, messageHash, pkBytes[:])
	durationBlst := time.Since(startBlst)
	require.True(t, okBlst)

	t.Logf("verifyFinalSignature (gnark-crypto): %s", durationGnark)
	t.Logf("verifyFinalSignatureBlst (blst):      %s", durationBlst)
}

func TestComputeParticipantPublicKey_TimingComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped with -short")
	}

	k, _ := setupTimingKeeper(t)

	// Setup mock EpochBLSData with 100 slots
	totalSlots := uint32(100)
	slotPKs := make([][]byte, totalSlots)
	_, _, _, g2Gen := bls12381.Generators()
	for i := uint32(0); i < totalSlots; i++ {
		var sk fr.Element
		sk.SetUint64(uint64(i + 1))
		var pk bls12381.G2Affine
		pk.ScalarMultiplication(&g2Gen, sk.BigInt(new(big.Int)))
		pkArr := pk.Bytes()
		slotPKs[i] = pkArr[:]
	}

	epochData := types.EpochBLSData{
		SlotPublicKeys: slotPKs,
	}

	// Use all 100 slots for the participant
	slotIndices := make([]uint32, totalSlots)
	for i := uint32(0); i < totalSlots; i++ {
		slotIndices[i] = i
	}

	// 1. Measure gnark
	startGnark := time.Now()
	resGnark, err := k.computeParticipantPublicKey(&epochData, slotIndices)
	durationGnark := time.Since(startGnark)
	require.NoError(t, err)

	// 2. Measure blst
	startBlst := time.Now()
	resBlst, err := k.computeParticipantPublicKeyBlst(&epochData, slotIndices)
	durationBlst := time.Since(startBlst)
	require.NoError(t, err)

	// Compare results
	require.Equal(t, resGnark, resBlst, "Participant public keys must match")

	t.Logf("computeParticipantPublicKey (gnark-crypto): %s", durationGnark)
	t.Logf("computeParticipantPublicKeyBlst (blst):      %s", durationBlst)
}

func TestDecompressG2To256_TimingComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped with -short")
	}

	k, _ := setupTimingKeeper(t)

	_, _, _, g2Gen := bls12381.Generators()
	var sk fr.Element
	sk.SetRandom()
	var pk bls12381.G2Affine
	pk.ScalarMultiplication(&g2Gen, sk.BigInt(new(big.Int)))
	pkBytes := pk.Bytes()

	// 1. Measure gnark
	startGnark := time.Now()
	resGnark, err := k.DecompressG2To256(pkBytes[:])
	durationGnark := time.Since(startGnark)
	require.NoError(t, err)

	// 2. Measure blst
	startBlst := time.Now()
	resBlst, err := k.DecompressG2To256Blst(pkBytes[:])
	durationBlst := time.Since(startBlst)
	require.NoError(t, err)

	// 3. Compare results
	require.Equal(t, resGnark, resBlst, "Decompressed G2 formats must match exactly")

	t.Logf("DecompressG2To256 (gnark-crypto): %s", durationGnark)
	t.Logf("DecompressG2To256Blst (blst):      %s", durationBlst)
}

func TestDecompressG1To128_TimingComparison(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test skipped with -short")
	}

	k, _ := setupTimingKeeper(t)

	_, _, g1Gen, _ := bls12381.Generators()
	var sk fr.Element
	sk.SetRandom()
	var sig bls12381.G1Affine
	sig.ScalarMultiplication(&g1Gen, sk.BigInt(new(big.Int)))
	sigBytes := sig.Bytes()

	// 1. Measure gnark
	startGnark := time.Now()
	resGnark, err := k.DecompressG1To128(sigBytes[:])
	durationGnark := time.Since(startGnark)
	require.NoError(t, err)

	// 2. Measure blst
	startBlst := time.Now()
	resBlst, err := k.DecompressG1To128Blst(sigBytes[:])
	durationBlst := time.Since(startBlst)
	require.NoError(t, err)

	// 3. Compare results
	require.Equal(t, resGnark, resBlst, "Decompressed G1 formats must match exactly")

	t.Logf("DecompressG1To128 (gnark-crypto): %s", durationGnark)
	t.Logf("DecompressG1To128Blst (blst):      %s", durationBlst)
}

func setupTimingKeeper(t testing.TB) (Keeper, sdk.Context) {
	storeKey := storetypes.NewKVStoreKey(types.StoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)

	k := NewKeeper(
		cdc,
		runtime.NewKVStoreService(storeKey),
		log.NewNopLogger(),
		authority.String(),
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))

	return k, ctx
}

func buildTimingParticipants(totalSlots uint32, numParticipants int) []types.BLSParticipantInfo {
	participants := make([]types.BLSParticipantInfo, numParticipants)
	slotsPerParticipant := totalSlots / uint32(numParticipants)

	for i := 0; i < numParticipants; i++ {
		startIndex := uint32(i) * slotsPerParticipant
		endIndex := startIndex + slotsPerParticipant - 1
		if i == numParticipants-1 {
			endIndex = totalSlots - 1
		}

		participants[i] = types.BLSParticipantInfo{
			Address:            fmt.Sprintf("participant%02d", i+1),
			PercentageWeight:   math.LegacyNewDec(1),
			Secp256K1PublicKey: []byte(fmt.Sprintf("pubkey%02d", i+1)),
			SlotStartIndex:     startIndex,
			SlotEndIndex:       endIndex,
		}
	}

	return participants
}

func g1SignatureFromScalar(message bls12381.G1Affine, sk fr.Element) []byte {
	var sig bls12381.G1Affine
	sig.ScalarMultiplication(&message, sk.BigInt(new(big.Int)))
	bytes := sig.Bytes()
	return bytes[:]
}

func g2BytesFromScalar(g2Gen bls12381.G2Affine, sk fr.Element) []byte {
	var g2 bls12381.G2Affine
	g2.ScalarMultiplication(&g2Gen, sk.BigInt(new(big.Int)))
	bytes := g2.Bytes()
	return bytes[:]
}

func buildDealerCoefficients(numDealers, commitmentCount int) ([][]fr.Element, []fr.Element) {
	dealerCoeffs := make([][]fr.Element, numDealers)
	totalCoeffs := make([]fr.Element, commitmentCount)

	for dealerIdx := 0; dealerIdx < numDealers; dealerIdx++ {
		coeffs := make([]fr.Element, commitmentCount)
		for coeffIdx := 0; coeffIdx < commitmentCount; coeffIdx++ {
			var coeff fr.Element
			coeff.SetUint64(uint64(dealerIdx*commitmentCount + coeffIdx + 1))
			coeffs[coeffIdx] = coeff
			totalCoeffs[coeffIdx].Add(&totalCoeffs[coeffIdx], &coeff)
		}
		dealerCoeffs[dealerIdx] = coeffs
	}

	return dealerCoeffs, totalCoeffs
}

func computeSlotScalars(totalCoeffs []fr.Element, totalSlots uint32) []fr.Element {
	scalars := make([]fr.Element, totalSlots)
	for slot := uint32(0); slot < totalSlots; slot++ {
		var x fr.Element
		x.SetUint64(uint64(slot + 1))
		scalars[slot] = evalPolynomial(totalCoeffs, x)
	}
	return scalars
}

func evalPolynomial(coeffs []fr.Element, x fr.Element) fr.Element {
	var result fr.Element
	result.SetZero()
	var power fr.Element
	power.SetOne()

	for i := range coeffs {
		var term fr.Element
		term.Mul(&coeffs[i], &power)
		result.Add(&result, &term)
		power.Mul(&power, &x)
	}

	return result
}

type verifyTimingStats struct {
	HashToG1            time.Duration
	DealerLoopTotal     time.Duration
	CommitmentEvalTime  time.Duration
	CommitmentUnmarshal time.Duration
	CommitmentScalarMul time.Duration
	CommitmentAdd       time.Duration
	CommitmentPowerMul  time.Duration
	PairingTime         time.Duration
}

type verifyTimingStatsMSM struct {
	HashToG1            time.Duration
	DealerLoopTotal     time.Duration
	CommitmentEvalTime  time.Duration
	CommitmentUnmarshal time.Duration
	CommitmentMultiExp  time.Duration
	PairingTime         time.Duration
}

func verifyBLSPartialSignatureWithTimings(k Keeper, signature []byte, messageHash []byte, epochBLSData *types.EpochBLSData, slotIndices []uint32) (bool, verifyTimingStats) {
	var stats verifyTimingStats

	if len(signature)%48 != 0 {
		return false, stats
	}
	sigCount := len(signature) / 48
	if sigCount != len(slotIndices) {
		return false, stats
	}

	hashStart := time.Now()
	messageG1, err := k.hashToG1(messageHash)
	stats.HashToG1 = time.Since(hashStart)
	if err != nil {
		return false, stats
	}

	_, _, _, g2Gen := bls12381.Generators()

	for i, slotIndex := range slotIndices {
		start := i * 48
		end := start + 48
		sigBytes := signature[start:end]

		var g1Signature bls12381.G1Affine
		if err := g1Signature.Unmarshal(sigBytes); err != nil {
			return false, stats
		}

		var slotPubKey bls12381.G2Affine
		slotPubKey.SetInfinity()

		dealerLoopStart := time.Now()
		for dealerIdx, isValid := range epochBLSData.ValidDealers {
			if !isValid || dealerIdx >= len(epochBLSData.DealerParts) {
				continue
			}
			dealerPart := epochBLSData.DealerParts[dealerIdx]
			if dealerPart == nil || len(dealerPart.Commitments) == 0 {
				continue
			}
			evalStart := time.Now()
			eval, evalStats, err := evaluateCommitmentPolynomialWithTimings(dealerPart.Commitments, slotIndex)
			stats.CommitmentEvalTime += time.Since(evalStart)
			stats.CommitmentUnmarshal += evalStats.Unmarshal
			stats.CommitmentScalarMul += evalStats.ScalarMul
			stats.CommitmentAdd += evalStats.Add
			stats.CommitmentPowerMul += evalStats.PowerMul
			if err != nil {
				return false, stats
			}
			slotPubKey.Add(&slotPubKey, &eval)
		}
		stats.DealerLoopTotal += time.Since(dealerLoopStart)

		pairStart := time.Now()
		p1, err := bls12381.Pair([]bls12381.G1Affine{g1Signature}, []bls12381.G2Affine{g2Gen})
		if err != nil {
			return false, stats
		}
		p2, err := bls12381.Pair([]bls12381.G1Affine{messageG1}, []bls12381.G2Affine{slotPubKey})
		if err != nil {
			return false, stats
		}
		stats.PairingTime += time.Since(pairStart)
		if !p1.Equal(&p2) {
			return false, stats
		}
	}

	return true, stats
}

func logVerifyStats(t *testing.T, label string, stats verifyTimingStats) {
	t.Logf(
		"verifyBLSPartialSignature breakdown (%s): hashToG1=%s dealerLoop=%s commitmentEval=%s pairing=%s",
		label,
		stats.HashToG1,
		stats.DealerLoopTotal,
		stats.CommitmentEvalTime,
		stats.PairingTime,
	)
	t.Logf(
		"commitment breakdown (%s): unmarshal=%s scalarMul=%s add=%s powerMul=%s",
		label,
		stats.CommitmentUnmarshal,
		stats.CommitmentScalarMul,
		stats.CommitmentAdd,
		stats.CommitmentPowerMul,
	)
}

func logVerifyStatsMSM(t *testing.T, label string, stats verifyTimingStatsMSM) {
	t.Logf(
		"verifyBLSPartialSignature breakdown (%s): hashToG1=%s dealerLoop=%s commitmentEval=%s pairing=%s",
		label,
		stats.HashToG1,
		stats.DealerLoopTotal,
		stats.CommitmentEvalTime,
		stats.PairingTime,
	)
	t.Logf(
		"commitment breakdown (%s): unmarshal=%s multiExp=%s",
		label,
		stats.CommitmentUnmarshal,
		stats.CommitmentMultiExp,
	)
}

type commitmentEvalStats struct {
	Unmarshal time.Duration
	ScalarMul time.Duration
	Add       time.Duration
	PowerMul  time.Duration
}

func evaluateCommitmentPolynomialWithTimings(commitments [][]byte, slotIndex uint32) (bls12381.G2Affine, commitmentEvalStats, error) {
	var result bls12381.G2Affine
	result.SetInfinity()

	var stats commitmentEvalStats

	var x fr.Element
	x.SetUint64(uint64(slotIndex + 1))
	var power fr.Element
	power.SetOne()

	for i, commitmentBytes := range commitments {
		if len(commitmentBytes) != 96 {
			return result, stats, fmt.Errorf("invalid commitment %d length: expected 96, got %d", i, len(commitmentBytes))
		}

		var commitment bls12381.G2Affine
		unmarshalStart := time.Now()
		if err := commitment.Unmarshal(commitmentBytes); err != nil {
			return result, stats, fmt.Errorf("failed to unmarshal commitment %d: %w", i, err)
		}
		stats.Unmarshal += time.Since(unmarshalStart)

		var term bls12381.G2Affine
		mulStart := time.Now()
		term.ScalarMultiplication(&commitment, power.BigInt(new(big.Int)))
		stats.ScalarMul += time.Since(mulStart)

		addStart := time.Now()
		result.Add(&result, &term)
		stats.Add += time.Since(addStart)

		powerStart := time.Now()
		power.Mul(&power, &x)
		stats.PowerMul += time.Since(powerStart)
	}

	return result, stats, nil
}

type commitmentEvalStatsMSM struct {
	Unmarshal time.Duration
	MultiExp  time.Duration
}

func evaluateCommitmentPolynomialWithMSM(commitments [][]byte, slotIndex uint32) (bls12381.G2Affine, commitmentEvalStatsMSM, error) {
	var result bls12381.G2Affine
	result.SetInfinity()

	var stats commitmentEvalStatsMSM

	scalars := make([]fr.Element, len(commitments))
	points := make([]bls12381.G2Affine, len(commitments))

	var x fr.Element
	x.SetUint64(uint64(slotIndex + 1))
	var power fr.Element
	power.SetOne()

	for i, commitmentBytes := range commitments {
		if len(commitmentBytes) != 96 {
			return result, stats, fmt.Errorf("invalid commitment %d length: expected 96, got %d", i, len(commitmentBytes))
		}
		unmarshalStart := time.Now()
		if err := points[i].Unmarshal(commitmentBytes); err != nil {
			return result, stats, fmt.Errorf("failed to unmarshal commitment %d: %w", i, err)
		}
		stats.Unmarshal += time.Since(unmarshalStart)

		scalars[i] = power
		power.Mul(&power, &x)
	}

	multiExpStart := time.Now()
	if _, err := result.MultiExp(points, scalars, ecc.MultiExpConfig{}); err != nil {
		return result, stats, err
	}
	stats.MultiExp += time.Since(multiExpStart)

	return result, stats, nil
}

func verifyBLSPartialSignatureWithTimingsMSM(k Keeper, signature []byte, messageHash []byte, epochBLSData *types.EpochBLSData, slotIndices []uint32) (bool, verifyTimingStatsMSM) {
	var stats verifyTimingStatsMSM

	if len(signature)%48 != 0 {
		return false, stats
	}
	sigCount := len(signature) / 48
	if sigCount != len(slotIndices) {
		return false, stats
	}

	hashStart := time.Now()
	messageG1, err := k.hashToG1(messageHash)
	stats.HashToG1 = time.Since(hashStart)
	if err != nil {
		return false, stats
	}

	_, _, _, g2Gen := bls12381.Generators()

	for i, slotIndex := range slotIndices {
		start := i * 48
		end := start + 48
		sigBytes := signature[start:end]

		var g1Signature bls12381.G1Affine
		if err := g1Signature.Unmarshal(sigBytes); err != nil {
			return false, stats
		}

		var slotPubKey bls12381.G2Affine
		slotPubKey.SetInfinity()

		dealerLoopStart := time.Now()
		for dealerIdx, isValid := range epochBLSData.ValidDealers {
			if !isValid || dealerIdx >= len(epochBLSData.DealerParts) {
				continue
			}
			dealerPart := epochBLSData.DealerParts[dealerIdx]
			if dealerPart == nil || len(dealerPart.Commitments) == 0 {
				continue
			}
			evalStart := time.Now()
			eval, evalStats, err := evaluateCommitmentPolynomialWithMSM(dealerPart.Commitments, slotIndex)
			stats.CommitmentEvalTime += time.Since(evalStart)
			stats.CommitmentUnmarshal += evalStats.Unmarshal
			stats.CommitmentMultiExp += evalStats.MultiExp
			if err != nil {
				return false, stats
			}
			slotPubKey.Add(&slotPubKey, &eval)
		}
		stats.DealerLoopTotal += time.Since(dealerLoopStart)

		pairStart := time.Now()
		p1, err := bls12381.Pair([]bls12381.G1Affine{g1Signature}, []bls12381.G2Affine{g2Gen})
		if err != nil {
			return false, stats
		}
		p2, err := bls12381.Pair([]bls12381.G1Affine{messageG1}, []bls12381.G2Affine{slotPubKey})
		if err != nil {
			return false, stats
		}
		stats.PairingTime += time.Since(pairStart)
		if !p1.Equal(&p2) {
			return false, stats
		}
	}

	return true, stats
}
