package keeper

import (
	"math/big"
	"testing"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fp"
	hashToCurve "github.com/consensys/gnark-crypto/ecc/bls12-381/hash_to_curve"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

// Commitments live under their own sub-key; GetDealerPart and
// GetEpochBLSData return the part joined, and a part written with inline
// commitments (before the split) is still read in full.
func TestDealerCommitments_StoredApartAndJoined(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	const epochID = uint64(48)
	part := makeDealerPart("a")

	require.NoError(t, k.SetDealerPart(ctx, epochID, 0, part))
	var raw types.DealerPartStorage
	require.NoError(t, k.cdc.Unmarshal(k.dealerPartsStore(ctx, epochID).Get(types.DealerPartSubKey(0)), &raw))
	require.Empty(t, raw.Commitments, "the part sub-key must not carry the commitments")
	got, err := k.GetDealerPart(ctx, epochID, 0)
	require.NoError(t, err)
	require.Equal(t, part, got)
	commitments, err := k.GetDealerCommitments(ctx, epochID, 0)
	require.NoError(t, err)
	require.Equal(t, part.Commitments, commitments)

	legacy := makeDealerPart("b")
	value, err := k.cdc.Marshal(legacy)
	require.NoError(t, err)
	k.dealerPartsStore(ctx, epochID).Set(types.DealerPartSubKey(1), value)
	commitments, err = k.GetDealerCommitments(ctx, epochID, 1)
	require.NoError(t, err)
	require.Equal(t, legacy.Commitments, commitments)

	commitments, err = k.GetDealerCommitments(ctx, epochID, 2)
	require.NoError(t, err)
	require.Nil(t, commitments)

	require.NoError(t, k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:      epochID,
		Participants: []types.BLSParticipantInfo{{Address: "a"}, {Address: "b"}, {Address: "c"}},
	}))
	full, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.Equal(t, part, full.DealerParts[0])
	require.Equal(t, legacy, full.DealerParts[1])

	require.NoError(t, k.DeleteDealerPartsForEpoch(ctx, epochID))
	commitments, err = k.GetDealerCommitments(ctx, epochID, 0)
	require.NoError(t, err)
	require.Nil(t, commitments)
}

// A part stored before the split keeps its commitments inline; the verifier
// still checks a proof against them.
func TestSubmitVerificationVector_LegacyInlineCommitments(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)
	ms := NewMsgServerImpl(k)
	const epochID, dealerScalar = uint64(51), uint64(7)
	require.NoError(t, k.SetEpochBLSData(ctx, types.EpochBLSData{
		EpochId:                     epochID,
		ITotalSlots:                 2,
		Participants:                []types.BLSParticipantInfo{{Address: "p0", SlotStartIndex: 0, SlotEndIndex: 0}, {Address: "p1", SlotStartIndex: 1, SlotEndIndex: 1}},
		DkgPhase:                    types.DKGPhase_DKG_PHASE_VERIFYING,
		VerifyingPhaseDeadlineBlock: ctx.BlockHeight() + 100,
	}))
	_, _, _, g2 := bls12381.Generators()
	var commitment bls12381.G2Affine
	commitment.ScalarMultiplication(&g2, new(big.Int).SetUint64(dealerScalar))
	c := commitment.Bytes()
	value, err := k.cdc.Marshal(&types.DealerPartStorage{DealerAddress: "p0", Commitments: [][]byte{c[:]}})
	require.NoError(t, err)
	k.dealerPartsStore(ctx, epochID).Set(types.DealerPartSubKey(0), value)

	hash := types.BuildDealerValidityProofHash(epochID, 0)
	var be [48]byte
	copy(be[16:], hash)
	var u fp.Element
	u.SetBytes(be[:])
	p := bls12381.MapToCurve1(&u)
	hashToCurve.G1Isogeny(&p.X, &p.Y)
	var msgPoint, sig bls12381.G1Affine
	msgPoint.ClearCofactor(&p)
	sig.ScalarMultiplication(&msgPoint, new(big.Int).SetUint64(dealerScalar))
	sb := sig.Bytes()

	_, err = ms.SubmitVerificationVector(ctx, &types.MsgSubmitVerificationVector{
		Creator:              "p1",
		EpochId:              epochID,
		DealerValidity:       []bool{true, true},
		DealerValidityProofs: []types.DealerValidityProof{{DealerIndex: 0, ProofSignature: sb[:]}},
	})
	require.NoError(t, err)
}
