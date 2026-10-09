package keeper_test

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func scalarLE(e *fr.Element) []byte {
	b := e.Bytes()
	for i := 0; i < fr.Bytes/2; i++ {
		b[i], b[fr.Bytes-1-i] = b[fr.Bytes-1-i], b[i]
	}
	return b[:]
}

func randomScalar(t testing.TB) fr.Element {
	var e fr.Element
	_, err := e.SetRandom()
	require.NoError(t, err)
	return e
}

func g2Mul(e *fr.Element) *blst.P2 {
	return blst.P2Generator().Mult(scalarLE(e), 255)
}

func commitPolynomial(coefficients []fr.Element) [][]byte {
	commitments := make([][]byte, len(coefficients))
	for i := range coefficients {
		commitments[i] = g2Mul(&coefficients[i]).ToAffine().Compress()
	}
	return commitments
}

func proveConstantTerm(t testing.TB, epochID uint64, dealer string, commitments [][]byte, a0 *fr.Element) []byte {
	k := randomScalar(t)
	challenge := types.DealerConstantTermPoKChallenge(epochID, dealer, commitments, g2Mul(&k).ToAffine().Compress())
	var c fr.Element
	require.NoError(t, c.SetBytesCanonical(challenge[:]))
	var z fr.Element
	z.Mul(&c, a0).Add(&z, &k)
	cb, zb := c.Bytes(), z.Bytes()
	return append(cb[:], zb[:]...)
}

func dealerCommitmentsWithPoK(t testing.TB, epochID uint64, dealer string, degree int) ([][]byte, []byte) {
	coefficients := make([]fr.Element, degree+1)
	for i := range coefficients {
		coefficients[i] = randomScalar(t)
	}
	commitments := commitPolynomial(coefficients)
	return commitments, proveConstantTerm(t, epochID, dealer, commitments, &coefficients[0])
}

func TestDealerConstantTermPoK_Valid(t *testing.T) {
	commitments, proof := dealerCommitmentsWithPoK(t, 7, "dealer1", 3)
	require.NoError(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", commitments, proof))
}

func TestDealerConstantTermPoK_RejectsReplayAndTampering(t *testing.T) {
	commitments, proof := dealerCommitmentsWithPoK(t, 7, "dealer1", 3)

	require.Error(t, keeper.VerifyDealerConstantTermPoK(8, "dealer1", commitments, proof))
	require.Error(t, keeper.VerifyDealerConstantTermPoK(7, "dealer2", commitments, proof))

	other, _ := dealerCommitmentsWithPoK(t, 7, "dealer1", 3)
	tampered := append([][]byte{commitments[0]}, other[1:]...)
	require.Error(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", tampered, proof))

	require.Error(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", commitments[:3], proof))

	flipped := append([]byte(nil), proof...)
	flipped[len(flipped)-1] ^= 1
	require.Error(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", commitments, flipped))
}

func TestDealerConstantTermPoK_RejectsMalformed(t *testing.T) {
	commitments, proof := dealerCommitmentsWithPoK(t, 7, "dealer1", 3)

	require.Error(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", commitments, nil))
	require.Error(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", commitments, proof[:types.DealerConstantTermPoKLen-1]))
	require.Error(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", nil, proof))

	nonCanonical := append([]byte(nil), proof...)
	for i := 0; i < fr.Bytes; i++ {
		nonCanonical[i] = 0xff
	}
	require.Error(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", commitments, nonCanonical))

	identity := append([][]byte{new(blst.P2).ToAffine().Compress()}, commitments[1:]...)
	require.Error(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", identity, proof))
}

func TestDealerConstantTermPoK_RejectsIdentityNonceCommitment(t *testing.T) {
	a0 := randomScalar(t)
	commitments := commitPolynomial([]fr.Element{a0, randomScalar(t)})
	c := randomScalar(t)
	var z fr.Element
	z.Mul(&c, &a0)
	cb, zb := c.Bytes(), z.Bytes()
	require.Error(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", commitments, append(cb[:], zb[:]...)))
}

func TestDealerConstantTermPoK_BlocksLastDealerKeyCancellation(t *testing.T) {
	const epochID, degree = uint64(416), 3

	honestSum := new(blst.P2)
	for _, dealer := range []string{"honest1", "honest2", "honest3"} {
		commitments, proof := dealerCommitmentsWithPoK(t, epochID, dealer, degree)
		require.NoError(t, keeper.VerifyDealerConstantTermPoK(epochID, dealer, commitments, proof))
		honestSum.AddAssign(new(blst.P2Affine).Uncompress(commitments[0]))
	}

	r := randomScalar(t)
	rogueConstant := g2Mul(&r).Sub(honestSum).ToAffine()
	rogue := make([][]byte, degree+1)
	rogue[0] = rogueConstant.Compress()
	for i := 1; i <= degree; i++ {
		coefficient := randomScalar(t)
		rogue[i] = g2Mul(&coefficient).ToAffine().Compress()
	}

	groupKey := new(blst.P2)
	groupKey.FromAffine(rogueConstant)
	groupKey.AddAssign(honestSum.ToAffine())
	require.True(t, groupKey.ToAffine().Equals(g2Mul(&r).ToAffine()))

	require.Error(t, keeper.VerifyDealerConstantTermPoK(epochID, "rogue", rogue, proveConstantTerm(t, epochID, "rogue", rogue, &r)))
	guess := randomScalar(t)
	require.Error(t, keeper.VerifyDealerConstantTermPoK(epochID, "rogue", rogue, proveConstantTerm(t, epochID, "rogue", rogue, &guess)))
}

func mustHex(t testing.TB, s string) []byte {
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

func TestDealerConstantTermPoK_AcceptsDAPIProof(t *testing.T) {
	commitments := [][]byte{
		mustHex(t, "a190be857d602284393305bfe0a29e29a6982ed3f04ccaabafb7e59cdc7eda85c22bc3e8690355c7a0fb7590ae40f1b009303f04d568e289a35102b6df883d5ed620355c0eb5d02236718cdaf99fba6e19ef5cee2996268eb9a53ae1ee09bce3"),
		mustHex(t, "a528590e82f409ea8ce953f0c59d15080185dc6e3219b69fcaa3a2c8fc9d0b9e0bc1e75ec6c52638e6eaa4584005b538129c4945fe62538d2806fff056adac24f3bba8e17e42d82122affe6ad2123d68784348a79755f194fde3b3d448924032"),
		mustHex(t, "ab0f336a9b3ee493b210f64587b8c1d5fab0a86346812545a822692c40ee4eecb349a69388ba3ad13ea3c5f8d29eb36506547761152a1517a6fffeefff70c7e817bdcf94dfa8e25955a0b4ac6ffcfd9477d7230902453d78bac3bcd06dbbb457"),
	}
	proof := mustHex(t, "3f0e10eed50b30b42f201aa272033dc42c7a0043c4a3efbdb277e6895d717ec85fd84573fa028914aceb4b6b51c649b8c5dc444d368e5f0aafee2b4d05d5f662")

	for i, coefficient := range []uint64{11, 22, 33} {
		var e fr.Element
		e.SetUint64(coefficient)
		require.Equal(t, g2Mul(&e).ToAffine().Compress(), commitments[i])
	}

	require.NoError(t, keeper.VerifyDealerConstantTermPoK(416, "gonka1dealerfixture", commitments, proof))
	require.Error(t, keeper.VerifyDealerConstantTermPoK(416, "gonka1dealerfixture", [][]byte{commitments[0], commitments[2], commitments[1]}, proof))
}

func TestDealerConstantTermPoK_RejectsScalarsAtModulusBoundary(t *testing.T) {
	commitments, proof := dealerCommitmentsWithPoK(t, 7, "dealer1", 3)

	modulus := make([]byte, fr.Bytes)
	fr.Modulus().FillBytes(modulus)
	modulusMinusOne := make([]byte, fr.Bytes)
	new(big.Int).Sub(fr.Modulus(), big.NewInt(1)).FillBytes(modulusMinusOne)

	withZ := func(z []byte) []byte {
		return append(append([]byte(nil), proof[:fr.Bytes]...), z...)
	}
	withC := func(c []byte) []byte {
		return append(append([]byte(nil), c...), proof[fr.Bytes:]...)
	}

	require.ErrorContains(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", commitments, withZ(modulus)), "canonical")
	require.ErrorContains(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", commitments, withC(modulus)), "canonical")
	require.ErrorContains(t, keeper.VerifyDealerConstantTermPoK(7, "dealer1", commitments, withZ(modulusMinusOne)), "challenge mismatch")
}

func admittedPoK() []byte {
	return make([]byte, types.DealerConstantTermPoKLen)
}

func legacyRogueEpoch(t *testing.T, epochID uint64, withHonestPoK bool) (types.EpochBLSData, *blst.P2Affine, *blst.P2Affine) {
	participants := make([]types.BLSParticipantInfo, 4)
	dealerParts := make([]*types.DealerPartStorage, 4)
	submissions := make([]*types.VerificationVectorSubmission, 4)
	honestSum := new(blst.P2)
	for i := 0; i < 4; i++ {
		address := "participant" + string(rune('1'+i))
		participants[i] = types.BLSParticipantInfo{Address: address, SlotStartIndex: uint32(i * 25), SlotEndIndex: uint32(i*25 + 24)}
		submissions[i] = &types.VerificationVectorSubmission{DealerValidity: []bool{true, true, true, true}}
		if i == 3 {
			continue
		}
		commitments, pok := dealerCommitmentsWithPoK(t, epochID, address, 1)
		if !withHonestPoK {
			pok = nil
		}
		dealerParts[i] = &types.DealerPartStorage{DealerAddress: address, Commitments: commitments, ConstantTermPok: pok}
		honestSum.AddAssign(new(blst.P2Affine).Uncompress(commitments[0]))
	}

	r := randomScalar(t)
	coefficient := randomScalar(t)
	dealerParts[3] = &types.DealerPartStorage{
		DealerAddress: participants[3].Address,
		Commitments:   [][]byte{g2Mul(&r).Sub(honestSum).ToAffine().Compress(), g2Mul(&coefficient).ToAffine().Compress()},
	}

	return types.EpochBLSData{
		EpochId:                 epochID,
		ITotalSlots:             100,
		TSlotsDegree:            1,
		DkgPhase:                types.DKGPhase_DKG_PHASE_DISPUTING,
		Participants:            participants,
		DealerParts:             dealerParts,
		VerificationSubmissions: submissions,
		CandidateValidDealers:   []bool{true, true, true, true},
	}, g2Mul(&r).ToAffine(), honestSum.ToAffine()
}

func TestCompleteDKG_ExcludesDealerPartWithoutPoK(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)
	epochBLSData, attackerKey, honestKey := legacyRogueEpoch(t, 30, true)
	k.SetEpochBLSData(ctx, epochBLSData)
	k.SetActiveEpochID(ctx, 30)

	stored, err := k.GetEpochBLSData(ctx, 30)
	require.NoError(t, err)
	require.NoError(t, k.CompleteDKG(ctx, &stored))

	require.Equal(t, types.DKGPhase_DKG_PHASE_COMPLETED, stored.DkgPhase)
	require.Equal(t, []bool{true, true, true, false}, stored.ValidDealers)
	require.NotEqual(t, attackerKey.Compress(), stored.GroupPublicKey)
	require.Equal(t, honestKey.Compress(), stored.GroupPublicKey)
}

func TestCompleteDKG_FailsWhenNoDealerPartHasPoK(t *testing.T) {
	k, ctx := keepertest.BlsKeeper(t)
	epochBLSData, _, _ := legacyRogueEpoch(t, 31, false)
	k.SetEpochBLSData(ctx, epochBLSData)
	k.SetActiveEpochID(ctx, 31)

	stored, err := k.GetEpochBLSData(ctx, 31)
	require.NoError(t, err)
	require.NoError(t, k.CompleteDKG(ctx, &stored))

	require.Equal(t, types.DKGPhase_DKG_PHASE_FAILED, stored.DkgPhase)
	require.Empty(t, stored.GroupPublicKey)
}
