package keeper

import (
	"math/big"
	"testing"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/require"
)

func makeValidCompressedG1Signature(t *testing.T) []byte {
	t.Helper()

	_, _, g1Gen, _ := bls12381.Generators()
	var scalar big.Int
	scalar.SetInt64(7)

	var sig bls12381.G1Affine
	sig.ScalarMultiplication(&g1Gen, &scalar)
	sigBytes := sig.Bytes()
	return sigBytes[:]
}

func TestAggregateBLSPartialSignatures_RejectsDuplicateSlots(t *testing.T) {
	k := Keeper{}
	sig := makeValidCompressedG1Signature(t)

	partials := []types.PartialSignature{
		{SlotIndices: []uint32{1}, Signature: sig},
		{SlotIndices: []uint32{1}, Signature: sig}, // duplicate slot across payloads
	}

	_, err := k.aggregateBLSPartialSignatures(partials)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate slot index")
}

func TestAggregateBLSPartialSignaturesBlst_RejectsDuplicateSlots(t *testing.T) {
	k := Keeper{}
	sig := makeValidCompressedG1Signature(t)

	partials := []types.PartialSignature{
		{SlotIndices: []uint32{3}, Signature: sig},
		{SlotIndices: []uint32{3}, Signature: sig}, // duplicate slot across payloads
	}

	_, err := k.aggregateBLSPartialSignaturesBlst(partials)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate slot index")
}
