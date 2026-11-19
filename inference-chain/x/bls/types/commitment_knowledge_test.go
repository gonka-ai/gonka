package types

import (
	"math/big"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"
)

func TestCommitmentKnowledgeProof_RoundTrip(t *testing.T) {
	const (
		epochID uint64 = 462
		creator        = "gonka1dealer"
	)
	scalars := []uint64{3, 0, 11}
	commitments := make([][]byte, len(scalars))
	proofs := make([]CommitmentKnowledgeProof, len(scalars))
	for i, scalar := range scalars {
		commitment, proof, err := BuildCommitmentKnowledgeProof(epochID, creator, uint32(i), new(fr.Element).SetUint64(scalar))
		require.NoError(t, err)
		commitments[i] = commitment
		proofs[i] = proof
	}

	require.NoError(t, VerifyCommitmentKnowledgeProofs(epochID, creator, commitments, proofs))
}

func TestCommitmentKnowledgeProof_RejectsUnknownScalar(t *testing.T) {
	const (
		epochID uint64 = 462
		creator        = "gonka1dealer"
	)
	commitment, proof, err := BuildCommitmentKnowledgeProof(epochID, creator, 0, new(fr.Element).SetUint64(4))
	require.NoError(t, err)

	other, _, err := BuildCommitmentKnowledgeProof(epochID, creator, 0, new(fr.Element).SetUint64(9))
	require.NoError(t, err)
	require.NotEqual(t, commitment, other)

	err = VerifyCommitmentKnowledgeProofs(epochID, creator, [][]byte{other}, []CommitmentKnowledgeProof{proof})
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestCommitmentKnowledgeProof_RejectsSwappedIndex(t *testing.T) {
	const (
		epochID uint64 = 7
		creator        = "gonka1dealer"
	)
	first, proof0, err := BuildCommitmentKnowledgeProof(epochID, creator, 0, new(fr.Element).SetUint64(2))
	require.NoError(t, err)
	second, proof1, err := BuildCommitmentKnowledgeProof(epochID, creator, 1, new(fr.Element).SetUint64(5))
	require.NoError(t, err)

	err = VerifyCommitmentKnowledgeProofs(epochID, creator, [][]byte{first, second}, []CommitmentKnowledgeProof{proof1, proof0})
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestCommitmentKnowledgeProof_RejectsDifferentCreator(t *testing.T) {
	commitment, proof, err := BuildCommitmentKnowledgeProof(3, "gonka1dealer", 0, new(fr.Element).SetUint64(8))
	require.NoError(t, err)

	err = VerifyCommitmentKnowledgeProofs(3, "gonka1other", [][]byte{commitment}, []CommitmentKnowledgeProof{proof})
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func validProofFixture(t *testing.T) (uint64, string, []byte, CommitmentKnowledgeProof) {
	t.Helper()
	const epochID uint64 = 5
	const creator = "gonka1dealer"
	commitment, proof, err := BuildCommitmentKnowledgeProof(epochID, creator, 0, new(fr.Element).SetUint64(6))
	require.NoError(t, err)
	return epochID, creator, commitment, proof
}

func verifyOne(epochID uint64, creator string, commitment []byte, proof CommitmentKnowledgeProof) error {
	return VerifyCommitmentKnowledgeProofs(epochID, creator, [][]byte{commitment}, []CommitmentKnowledgeProof{proof})
}

func TestCommitmentKnowledgeProof_RejectsTamperedResponse(t *testing.T) {
	epochID, creator, commitment, proof := validProofFixture(t)
	proof.Response = append([]byte(nil), proof.Response...)
	proof.Response[31] ^= 0x01
	err := verifyOne(epochID, creator, commitment, proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestCommitmentKnowledgeProof_RejectsNonCanonicalResponse(t *testing.T) {
	epochID, creator, commitment, proof := validProofFixture(t)
	// z + r verifies algebraically but is not a canonical scalar.
	z := new(big.Int).SetBytes(proof.Response)
	z.Add(z, fr.Modulus())
	proof.Response = z.FillBytes(make([]byte, 32))
	err := verifyOne(epochID, creator, commitment, proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "canonical")
}

func TestCommitmentKnowledgeProof_RejectsIdentityNonce(t *testing.T) {
	epochID, creator, commitment, proof := validProofFixture(t)
	identity := make([]byte, commitmentCompressedG2Len)
	identity[0] = 0xc0 // compressed point at infinity
	proof.NoncePoint = identity
	err := verifyOne(epochID, creator, commitment, proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity")
}

func TestCommitmentKnowledgeProof_RejectsPointOutsideG2(t *testing.T) {
	epochID, creator, commitment, proof := validProofFixture(t)

	// Find a compressed encoding that is a curve point but not in the subgroup,
	// or fails to decode at all; either way it must be rejected.
	bad := make([]byte, commitmentCompressedG2Len)
	bad[0] = 0x80 // compressed flag, x = 0 then tweak below
	rejected := 0
	for i := 1; i < 64; i++ {
		bad[commitmentCompressedG2Len-1] = byte(i)
		p := append([]byte(nil), bad...)
		require.Error(t, verifyOne(epochID, creator, p, proof))
		require.Error(t, verifyOne(epochID, creator, commitment, CommitmentKnowledgeProof{NoncePoint: p, Response: proof.Response}))
		rejected++
	}
	require.Equal(t, 63, rejected)
}

// A rogue dealer picks C' = victim - K for a point K whose scalar k it knows,
// aiming to cancel the victim's contribution. It cannot prove knowledge of the
// scalar behind C', so the deal must be rejected.
func TestCommitmentKnowledgeProof_RejectsRogueKeyCommitment(t *testing.T) {
	const epochID uint64 = 9
	const creator = "gonka1rogue"

	victim, _, err := BuildCommitmentKnowledgeProof(epochID, "gonka1victim", 0, new(fr.Element).SetUint64(1234))
	require.NoError(t, err)

	k := new(fr.Element).SetUint64(77)
	negK := blst.P2Generator().Mult(scalarToBlst(new(fr.Element).Neg(k)), 255)
	rogue := new(blst.P2)
	rogue.FromAffine(new(blst.P2Affine).Uncompress(victim))
	rogue.AddAssign(negK)
	rogueCommitment := rogue.ToAffine().Compress()

	// Best the attacker can do: a proof built for the scalar it does know.
	_, proof, err := BuildCommitmentKnowledgeProof(epochID, creator, 0, k)
	require.NoError(t, err)

	err = verifyOne(epochID, creator, rogueCommitment, proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestCommitmentKnowledgeChallenge_IsDomainSeparated(t *testing.T) {
	c := make([]byte, commitmentCompressedG2Len)
	r := make([]byte, commitmentCompressedG2Len)
	base := commitmentKnowledgeChallenge(1, "a", 0, c, r)
	require.False(t, base.Equal(commitmentKnowledgeChallenge(2, "a", 0, c, r)))
	require.False(t, base.Equal(commitmentKnowledgeChallenge(1, "b", 0, c, r)))
	require.False(t, base.Equal(commitmentKnowledgeChallenge(1, "a", 1, c, r)))
	// creator/index boundary cannot be shifted
	require.False(t, commitmentKnowledgeChallenge(1, "ab", 0, c, r).Equal(commitmentKnowledgeChallenge(1, "a", 0x62000000, c, r)))
}
