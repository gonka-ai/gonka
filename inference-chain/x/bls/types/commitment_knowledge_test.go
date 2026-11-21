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
		chainID        = "gonka-mainnet-1"
		epochID uint64 = 462
		creator        = "gonka1dealer"
	)
	scalars := []uint64{3, 0, 11}
	commitments := make([][]byte, len(scalars))
	proofs := make([]CommitmentKnowledgeProof, len(scalars))
	for i, scalar := range scalars {
		commitment, proof, err := BuildCommitmentKnowledgeProof(chainID, epochID, creator, uint32(i), new(fr.Element).SetUint64(scalar))
		require.NoError(t, err)
		commitments[i] = commitment
		proofs[i] = proof
	}

	require.NoError(t, VerifyCommitmentKnowledgeProofs(chainID, epochID, creator, commitments, proofs))
}

func TestCommitmentKnowledgeProof_RejectsZeroConstantTerm(t *testing.T) {
	const (
		chainID        = "gonka-mainnet-1"
		epochID uint64 = 462
		creator        = "gonka1dealer"
	)
	_, _, err := BuildCommitmentKnowledgeProof(chainID, epochID, creator, 0, new(fr.Element).SetZero())
	require.Error(t, err)
	require.Contains(t, err.Error(), "constant term coefficient (index 0) must be non-zero")

	// Even if an attacker crafts a valid Schnorr proof for C_0 = 0*G2 = O directly, index 0 is rejected.
	nonce := new(fr.Element).SetUint64(19)
	infCommitment := scalarMultG2(new(fr.Element).SetZero())
	noncePoint := scalarMultG2(nonce)
	err = VerifyCommitmentKnowledgeProofs(chainID, epochID, creator, [][]byte{infCommitment}, []CommitmentKnowledgeProof{{
		NoncePoint: noncePoint,
		Response:   nonce.Marshal(),
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "commitments[0] must not be the identity")
}

func TestCommitmentKnowledgeProof_RejectsUnknownScalar(t *testing.T) {
	const (
		chainID        = "gonka-mainnet-1"
		epochID uint64 = 462
		creator        = "gonka1dealer"
	)
	commitment, proof, err := BuildCommitmentKnowledgeProof(chainID, epochID, creator, 0, new(fr.Element).SetUint64(4))
	require.NoError(t, err)

	other, _, err := BuildCommitmentKnowledgeProof(chainID, epochID, creator, 0, new(fr.Element).SetUint64(9))
	require.NoError(t, err)
	require.NotEqual(t, commitment, other)

	err = VerifyCommitmentKnowledgeProofs(chainID, epochID, creator, [][]byte{other}, []CommitmentKnowledgeProof{proof})
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestCommitmentKnowledgeProof_RejectsSwappedIndex(t *testing.T) {
	const (
		chainID        = "gonka-mainnet-1"
		epochID uint64 = 7
		creator        = "gonka1dealer"
	)
	first, proof0, err := BuildCommitmentKnowledgeProof(chainID, epochID, creator, 0, new(fr.Element).SetUint64(2))
	require.NoError(t, err)
	second, proof1, err := BuildCommitmentKnowledgeProof(chainID, epochID, creator, 1, new(fr.Element).SetUint64(5))
	require.NoError(t, err)

	err = VerifyCommitmentKnowledgeProofs(chainID, epochID, creator, [][]byte{first, second}, []CommitmentKnowledgeProof{proof1, proof0})
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestCommitmentKnowledgeProof_RejectsDifferentCreatorOrChainID(t *testing.T) {
	commitment, proof, err := BuildCommitmentKnowledgeProof("gonka-mainnet-1", 3, "gonka1dealer", 0, new(fr.Element).SetUint64(8))
	require.NoError(t, err)

	err = VerifyCommitmentKnowledgeProofs("gonka-mainnet-1", 3, "gonka1other", [][]byte{commitment}, []CommitmentKnowledgeProof{proof})
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")

	err = VerifyCommitmentKnowledgeProofs("gonka-testnet-1", 3, "gonka1dealer", [][]byte{commitment}, []CommitmentKnowledgeProof{proof})
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func validProofFixture(t *testing.T) (string, uint64, string, []byte, CommitmentKnowledgeProof) {
	t.Helper()
	const (
		chainID        = "gonka-mainnet-1"
		epochID uint64 = 5
		creator        = "gonka1dealer"
	)
	commitment, proof, err := BuildCommitmentKnowledgeProof(chainID, epochID, creator, 0, new(fr.Element).SetUint64(6))
	require.NoError(t, err)
	return chainID, epochID, creator, commitment, proof
}

func verifyOne(chainID string, epochID uint64, creator string, commitment []byte, proof CommitmentKnowledgeProof) error {
	return VerifyCommitmentKnowledgeProofs(chainID, epochID, creator, [][]byte{commitment}, []CommitmentKnowledgeProof{proof})
}

func TestCommitmentKnowledgeProof_RejectsTamperedResponse(t *testing.T) {
	chainID, epochID, creator, commitment, proof := validProofFixture(t)
	proof.Response = append([]byte(nil), proof.Response...)
	proof.Response[31] ^= 0x01
	err := verifyOne(chainID, epochID, creator, commitment, proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestCommitmentKnowledgeProof_RejectsNonCanonicalResponse(t *testing.T) {
	chainID, epochID, creator, commitment, proof := validProofFixture(t)
	// z + r verifies algebraically but is not a canonical scalar.
	z := new(big.Int).SetBytes(proof.Response)
	z.Add(z, fr.Modulus())
	proof.Response = z.FillBytes(make([]byte, 32))
	err := verifyOne(chainID, epochID, creator, commitment, proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "canonical")
}

func TestCommitmentKnowledgeProof_RejectsIdentityNonce(t *testing.T) {
	chainID, epochID, creator, commitment, proof := validProofFixture(t)
	identity := make([]byte, commitmentCompressedG2Len)
	identity[0] = 0xc0 // compressed point at infinity
	proof.NoncePoint = identity
	err := verifyOne(chainID, epochID, creator, commitment, proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "identity")
}

func TestCommitmentKnowledgeProof_RejectsPointOutsideG2(t *testing.T) {
	chainID, epochID, creator, commitment, proof := validProofFixture(t)

	bad := make([]byte, commitmentCompressedG2Len)
	bad[0] = 0x80
	rejected := 0
	for i := 1; i < 64; i++ {
		bad[commitmentCompressedG2Len-1] = byte(i)
		p := append([]byte(nil), bad...)
		require.Error(t, verifyOne(chainID, epochID, creator, p, proof))
		require.Error(t, verifyOne(chainID, epochID, creator, commitment, CommitmentKnowledgeProof{NoncePoint: p, Response: proof.Response}))
		rejected++
	}
	require.Equal(t, 63, rejected)
}

func TestCommitmentKnowledgeProof_RejectsRogueKeyCommitment(t *testing.T) {
	const (
		chainID        = "gonka-mainnet-1"
		epochID uint64 = 9
		creator        = "gonka1rogue"
	)

	victim, _, err := BuildCommitmentKnowledgeProof(chainID, epochID, "gonka1victim", 0, new(fr.Element).SetUint64(1234))
	require.NoError(t, err)

	k := new(fr.Element).SetUint64(77)
	negK := blst.P2Generator().Mult(scalarToBlst(new(fr.Element).Neg(k)), 255)
	rogue := new(blst.P2)
	rogue.FromAffine(new(blst.P2Affine).Uncompress(victim))
	rogue.AddAssign(negK)
	rogueCommitment := rogue.ToAffine().Compress()

	_, proof, err := BuildCommitmentKnowledgeProof(chainID, epochID, creator, 0, k)
	require.NoError(t, err)

	err = verifyOne(chainID, epochID, creator, rogueCommitment, proof)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verification failed")
}

func TestCommitmentKnowledgeChallenge_IsDomainSeparated(t *testing.T) {
	c := make([]byte, commitmentCompressedG2Len)
	r := make([]byte, commitmentCompressedG2Len)
	base := commitmentKnowledgeChallenge("chain-1", 1, "a", 0, c, r)
	require.False(t, base.Equal(commitmentKnowledgeChallenge("chain-2", 1, "a", 0, c, r)))
	require.False(t, base.Equal(commitmentKnowledgeChallenge("chain-1", 2, "a", 0, c, r)))
	require.False(t, base.Equal(commitmentKnowledgeChallenge("chain-1", 1, "b", 0, c, r)))
	require.False(t, base.Equal(commitmentKnowledgeChallenge("chain-1", 1, "a", 1, c, r)))
	// creator/index boundary cannot be shifted
	require.False(t, commitmentKnowledgeChallenge("chain-1", 1, "ab", 0, c, r).Equal(commitmentKnowledgeChallenge("chain-1", 1, "a", 0x62000000, c, r)))
}
