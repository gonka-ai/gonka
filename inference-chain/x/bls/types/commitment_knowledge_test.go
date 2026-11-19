package types

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/stretchr/testify/require"
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
