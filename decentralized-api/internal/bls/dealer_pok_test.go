package bls

import (
	"math/big"
	"testing"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/require"
)

func recomputeNonceCommitment(t *testing.T, constantTerm []byte, c, z *fr.Element) []byte {
	var c0 bls12381.G2Affine
	_, err := c0.SetBytes(constantTerm)
	require.NoError(t, err)
	_, _, _, g2 := bls12381.Generators()

	var zG, cC0 bls12381.G2Affine
	zG.ScalarMultiplication(&g2, z.BigInt(new(big.Int)))
	cC0.ScalarMultiplication(&c0, c.BigInt(new(big.Int)))
	var r bls12381.G2Affine
	r.Sub(&zG, &cC0)
	b := r.Bytes()
	return b[:]
}

func TestProveConstantTermKnowledge(t *testing.T) {
	polynomial, err := generateRandomPolynomial(3)
	require.NoError(t, err)
	commitments := computeG2CommitmentsBlst(polynomial)

	proof, err := proveConstantTermKnowledge(42, "gonka1dealer", commitments, polynomial[0])
	require.NoError(t, err)
	require.Len(t, proof, types.DealerConstantTermPoKLen)

	var c, z fr.Element
	require.NoError(t, c.SetBytesCanonical(proof[:fr.Bytes]))
	require.NoError(t, z.SetBytesCanonical(proof[fr.Bytes:]))

	expected := types.DealerConstantTermPoKChallenge(42, "gonka1dealer", commitments, recomputeNonceCommitment(t, commitments[0], &c, &z))
	require.Equal(t, proof[:fr.Bytes], expected[:])

	other := types.DealerConstantTermPoKChallenge(43, "gonka1dealer", commitments, recomputeNonceCommitment(t, commitments[0], &c, &z))
	require.NotEqual(t, proof[:fr.Bytes], other[:])
}

func TestProveConstantTermKnowledge_FreshNonce(t *testing.T) {
	polynomial, err := generateRandomPolynomial(1)
	require.NoError(t, err)
	commitments := computeG2CommitmentsBlst(polynomial)

	first, err := proveConstantTermKnowledge(1, "gonka1dealer", commitments, polynomial[0])
	require.NoError(t, err)
	second, err := proveConstantTermKnowledge(1, "gonka1dealer", commitments, polynomial[0])
	require.NoError(t, err)
	require.NotEqual(t, first, second)
}
