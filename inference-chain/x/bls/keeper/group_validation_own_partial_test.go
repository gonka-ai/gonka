package keeper

import (
	"fmt"
	"testing"

	storetypes "cosmossdk.io/store/types"
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

const (
	gkvParticipants = 16
	gkvSlotsEach    = 4
	gkvDegree       = 40 // threshold 41: the 11th signer aggregates
)

type gkvSigner struct {
	msg *types.MsgSubmitGroupKeyValidationSignature
}

// setupGroupKeyValidation stores a signed previous epoch whose slot keys come
// from one degree-40 polynomial, a completed new epoch, and every previous
// participant's valid validation signature.
func setupGroupKeyValidation(t *testing.T) (msgServer, sdk.Context, []gkvSigner) {
	t.Helper()
	k, ctx := setupTimingKeeper(t)
	ctx = ctx.WithChainID("gkv-own-partial")
	_, _, _, g2Gen := bls12381.Generators()

	coeffs := make([]fr.Element, gkvDegree+1)
	for i := range coeffs {
		coeffs[i].SetUint64(uint64(7 + i))
	}
	totalSlots := uint32(gkvParticipants * gkvSlotsEach)
	scalars := computeSlotScalars(coeffs, totalSlots)

	prev := types.EpochBLSData{
		EpochId:        1,
		ITotalSlots:    totalSlots,
		TSlotsDegree:   gkvDegree,
		DkgPhase:       types.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: g2BytesFromScalar(g2Gen, coeffs[0]),
		Participants:   buildTimingParticipants(totalSlots, gkvParticipants),
	}
	for _, sk := range scalars {
		prev.SlotPublicKeys = append(prev.SlotPublicKeys, g2BytesFromScalar(g2Gen, sk))
	}
	var newSk fr.Element
	newSk.SetUint64(9)
	next := types.EpochBLSData{
		EpochId:        2,
		ITotalSlots:    totalSlots,
		TSlotsDegree:   gkvDegree,
		DkgPhase:       types.DKGPhase_DKG_PHASE_COMPLETED,
		GroupPublicKey: g2BytesFromScalar(g2Gen, newSk),
	}
	require.NoError(t, k.SetEpochBLSData(ctx, prev))
	require.NoError(t, k.SetEpochBLSData(ctx, next))

	ms := msgServer{Keeper: k}
	hash, err := ms.computeValidationMessageHash(ctx, next.GroupPublicKey, prev.EpochId, next.EpochId)
	require.NoError(t, err)
	msgG1, err := k.hashToG1(hash)
	require.NoError(t, err)

	signers := make([]gkvSigner, gkvParticipants)
	for i, p := range prev.Participants {
		msg := &types.MsgSubmitGroupKeyValidationSignature{Creator: p.Address, NewEpochId: next.EpochId}
		for slot := p.SlotStartIndex; slot <= p.SlotEndIndex; slot++ {
			msg.SlotIndices = append(msg.SlotIndices, slot)
			msg.PartialSignature = append(msg.PartialSignature, g1SignatureFromScalar(msgG1, scalars[slot])...)
		}
		signers[i] = gkvSigner{msg: msg}
	}
	return ms, ctx, signers
}

func submitGKVMeasured(t *testing.T, ms msgServer, ctx sdk.Context, msg *types.MsgSubmitGroupKeyValidationSignature) (storetypes.Gas, error) {
	t.Helper()
	sub := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err := ms.SubmitGroupKeyValidationSignature(sub, msg)
	return sub.GasMeter().GasConsumed(), err
}

// Store gas of each signer until the 11th reaches the threshold.
func TestSubmitGroupKeyValidationSignature_GasPerSigner(t *testing.T) {
	ms, ctx, signers := setupGroupKeyValidation(t)
	var total storetypes.Gas
	for i := 0; i < 11; i++ {
		gas, err := submitGKVMeasured(t, ms, ctx, signers[i].msg)
		require.NoError(t, err)
		total += gas
		t.Logf("signer %2d: %d gas", i+1, gas)
	}
	t.Logf("collection: %d gas", total)

	data, err := ms.GetEpochBLSData(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, types.DKGPhase_DKG_PHASE_SIGNED, data.DkgPhase)
	require.NotEmpty(t, data.ValidationSignature)
}

// A participant's resubmission of a slot it already signed is filtered,
// and a submission with no new slot is rejected.
func TestSubmitGroupKeyValidationSignature_ResubmittedSlots(t *testing.T) {
	ms, ctx, signers := setupGroupKeyValidation(t)
	first := signers[0].msg
	half := &types.MsgSubmitGroupKeyValidationSignature{
		Creator:          first.Creator,
		NewEpochId:       first.NewEpochId,
		SlotIndices:      first.SlotIndices[:2],
		PartialSignature: first.PartialSignature[:2*48],
	}
	_, err := ms.SubmitGroupKeyValidationSignature(ctx, half)
	require.NoError(t, err)
	_, err = ms.SubmitGroupKeyValidationSignature(ctx, signers[1].msg)
	require.NoError(t, err)

	_, err = ms.SubmitGroupKeyValidationSignature(ctx, half)
	require.ErrorContains(t, err, "no new slots")

	// The full set adds only the two slots not yet signed.
	_, err = ms.SubmitGroupKeyValidationSignature(ctx, first)
	require.NoError(t, err)
	state, found, err := ms.GetGroupKeyValidationState(ctx, 2)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, uint32(2*gkvSlotsEach), state.SlotsCovered)
	own, err := ms.GetGroupValidationPartialSignature(ctx, 2, 0)
	require.NoError(t, err)
	require.Equal(t, first.SlotIndices, own.SlotIndices)
	require.Equal(t, first.PartialSignature, own.Signature)

	// The rest still reach the threshold and aggregate.
	for i := 2; i < 11; i++ {
		_, err = ms.SubmitGroupKeyValidationSignature(ctx, signers[i].msg)
		require.NoError(t, err, fmt.Sprintf("signer %d", i+1))
	}
	data, err := ms.GetEpochBLSData(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, types.DKGPhase_DKG_PHASE_SIGNED, data.DkgPhase)
}
