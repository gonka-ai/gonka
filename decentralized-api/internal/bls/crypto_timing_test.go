package bls

import (
	"fmt"
	"testing"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/stretchr/testify/require"
)

func TestVerifyShareAgainstCommitments_TimingComparison(t *testing.T) {
	bm := &BlsManager{}

	degree := uint32(50) // t=51
	polynomial := make([]*fr.Element, degree+1)
	for i := range polynomial {
		polynomial[i] = new(fr.Element)
		_, _ = polynomial[i].SetRandom()
	}

	commitments := computeG2Commitments(polynomial)
	slotIndex := uint32(10)
	share := evaluatePolynomial(polynomial, slotIndex+1)

	// 1. Measure gnark-crypto
	startGnark := time.Now()
	okGnark, err := bm.verifyShareAgainstCommitments(share, slotIndex, commitments)
	durationGnark := time.Since(startGnark)
	require.NoError(t, err)
	require.True(t, okGnark)

	// 2. Measure blst
	startBlst := time.Now()
	okBlst, err := bm.verifyShareAgainstCommitmentsBlst(share, slotIndex, commitments)
	durationBlst := time.Since(startBlst)
	require.NoError(t, err)
	require.True(t, okBlst)

	fmt.Printf("verifyShareAgainstCommitments (gnark-crypto): %s\n", durationGnark)
	fmt.Printf("verifyShareAgainstCommitmentsBlst (blst):      %s\n", durationBlst)
	if durationBlst < durationGnark {
		fmt.Printf("blst is %.2f%% faster\n", float64(durationGnark-durationBlst)/float64(durationGnark)*100)
	}
}

func TestComputeG2Commitments_TimingComparison(t *testing.T) {
	degree := uint32(50)
	polynomial := make([]*fr.Element, degree+1)
	for i := range polynomial {
		polynomial[i] = new(fr.Element)
		_, _ = polynomial[i].SetRandom()
	}

	// 1. Measure gnark-crypto
	startGnark := time.Now()
	resGnark := computeG2Commitments(polynomial)
	durationGnark := time.Since(startGnark)

	// 2. Measure blst
	startBlst := time.Now()
	resBlst := computeG2CommitmentsBlst(polynomial)
	durationBlst := time.Since(startBlst)

	// Compare results
	require.Equal(t, len(resGnark), len(resBlst))
	for i := range resGnark {
		require.Equal(t, resGnark[i], resBlst[i], "Commitment at index %d mismatch", i)
	}

	fmt.Printf("computeG2Commitments (gnark-crypto): %s\n", durationGnark)
	fmt.Printf("computeG2CommitmentsBlst (blst):      %s\n", durationBlst)
	if durationBlst < durationGnark {
		fmt.Printf("blst is %.2f%% faster\n", float64(durationGnark-durationBlst)/float64(durationGnark)*100)
	}
}

func TestPartialSignature_TimingComparison(t *testing.T) {
	bm := &BlsManager{}

	messageHash := make([]byte, 32)
	copy(messageHash, "test message hash for api")

	numSlots := 50
	aggregatedShares := make([]fr.Element, numSlots)
	for i := range aggregatedShares {
		_, _ = aggregatedShares[i].SetRandom()
	}

	result := &VerificationResult{
		AggregatedShares: aggregatedShares,
	}

	// 1. Measure gnark-crypto (using computePartialSignature which calls hashToG1)
	startGnark := time.Now()
	sigGnark, err := bm.computePartialSignature(messageHash, result)
	durationGnark := time.Since(startGnark)
	require.NoError(t, err)

	// 2. Measure blst
	startBlst := time.Now()
	sigBlst, err := bm.computePartialSignatureBlst(messageHash, result)
	durationBlst := time.Since(startBlst)
	require.NoError(t, err)

	// Compare results
	require.Equal(t, sigGnark, sigBlst, "Partial signatures mismatch")

	fmt.Printf("computePartialSignature (gnark-crypto): %s\n", durationGnark)
	fmt.Printf("computePartialSignatureBlst (blst):      %s\n", durationBlst)
	if durationBlst < durationGnark {
		fmt.Printf("blst is %.2f%% faster\n", float64(durationGnark-durationBlst)/float64(durationGnark)*100)
	}
}
