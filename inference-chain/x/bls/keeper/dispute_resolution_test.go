package keeper

import (
	"io"
	"math/big"
	"testing"

	"cosmossdk.io/math"
	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/productscience/inference/x/bls/types"
	"github.com/stretchr/testify/require"
)

func TestApplyDealerComplaintOutcomes_MissingResponseRemovesDealer(t *testing.T) {
	k := Keeper{}

	epoch := types.EpochBLSData{
		Participants: []types.BLSParticipantInfo{
			{Address: "p0"},
			{Address: "p1"},
		},
		DealerComplaints: []types.DealerComplaint{
			{
				DealerIndex:             1,
				ComplainerIndex:         0,
				DisputedSlotIndex:       0,
				DisputedCiphertextIndex: 0,
				ResponseSubmitted:       false,
			},
		},
	}

	final, err := k.applyDealerComplaintOutcomes(&epoch, []bool{true, true})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, final)

	finalWithNonCandidateDealer, err := k.applyDealerComplaintOutcomes(&epoch, []bool{false, true})
	require.NoError(t, err)
	require.Equal(t, []bool{false, false}, finalWithNonCandidateDealer)
}

func TestApplyDealerComplaintOutcomes_ValidResponseRemovesComplainer(t *testing.T) {
	k := Keeper{}
	epochID := uint64(11)
	dealerIndex := uint32(0)
	complainerIndex := uint32(1)
	disputedSlot := uint32(1)
	disputedCiphertextIndex := uint32(0)

	dealerPriv, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	complainerPriv, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)

	share := fr.NewElement(5)
	shareBytes := share.Bytes()
	seed := make([]byte, dkgOpeningSeedLen)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	r1 := newDeterministicSeedReader(seed)
	r2 := newDeterministicSeedReader(seed)
	bytes1 := make([]byte, 256)
	bytes2 := make([]byte, 256)
	_, err = io.ReadFull(r1, bytes1)
	require.NoError(t, err)
	_, err = io.ReadFull(r2, bytes2)
	require.NoError(t, err)
	require.Equal(t, bytes1, bytes2)

	ciphertext, err := encryptWithSeedForParticipant(shareBytes[:], complainerPriv.PubKey().SerializeCompressed(), seed)
	require.NoError(t, err)
	reencrypted, err := encryptWithSeedForParticipant(shareBytes[:], complainerPriv.PubKey().SerializeCompressed(), seed)
	require.NoError(t, err)
	require.Equal(t, ciphertext, reencrypted)

	commitmentForShare := mustMakeG2CommitmentForScalar(t, 5)
	shareForCheck := &fr.Element{}
	shareForCheck.SetBytes(shareBytes[:])
	shareValid, err := k.verifyShareAgainstCommitmentsBlst(shareForCheck, disputedSlot, [][]byte{commitmentForShare})
	require.NoError(t, err)
	require.True(t, shareValid)

	epoch := types.EpochBLSData{
		EpochId: epochID,
		Participants: []types.BLSParticipantInfo{
			{
				Address:            "p0",
				Secp256K1PublicKey: dealerPriv.PubKey().SerializeCompressed(),
				PercentageWeight:   math.LegacyNewDec(50),
				SlotStartIndex:     0,
				SlotEndIndex:       0,
			},
			{
				Address:            "p1",
				Secp256K1PublicKey: complainerPriv.PubKey().SerializeCompressed(),
				PercentageWeight:   math.LegacyNewDec(50),
				SlotStartIndex:     1,
				SlotEndIndex:       1,
			},
		},
		DealerParts: []*types.DealerPartStorage{
			{
				DealerAddress: "p0",
				Commitments:   [][]byte{commitmentForShare},
				ParticipantShares: []*types.EncryptedSharesForParticipant{
					{EncryptedShares: [][]byte{}},
					{EncryptedShares: [][]byte{ciphertext}},
				},
			},
			{
				DealerAddress: "p1",
				Commitments:   [][]byte{mustMakeG2CommitmentForScalar(t, 7)},
				ParticipantShares: []*types.EncryptedSharesForParticipant{
					{EncryptedShares: [][]byte{}},
					{EncryptedShares: [][]byte{}},
				},
			},
		},
		DealerComplaints: []types.DealerComplaint{
			{
				DealerIndex:             dealerIndex,
				ComplainerIndex:         complainerIndex,
				DisputedSlotIndex:       disputedSlot,
				DisputedCiphertextIndex: disputedCiphertextIndex,
				ResponseSubmitted:       true,
				ResponseShareBytes:      shareBytes[:],
				ResponseOpeningMaterial: seed,
			},
		},
	}

	responseValid, err := k.verifyDealerComplaintResponse(&epoch, &epoch.DealerComplaints[0])
	require.NoError(t, err)
	require.True(t, responseValid)

	final, err := k.applyDealerComplaintOutcomes(&epoch, []bool{true, true})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, final)
}

func TestAdjudicateComplaints_RecomputeCandidatesAfterFalseComplainerExclusion(t *testing.T) {
	k := Keeper{}

	dealerPriv, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	complainerPriv, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)
	observerPriv, err := secp256k1.GeneratePrivateKey()
	require.NoError(t, err)

	share := fr.NewElement(5)
	shareBytes := share.Bytes()
	seed := make([]byte, dkgOpeningSeedLen)
	for i := range seed {
		seed[i] = byte(i + 11)
	}

	ciphertext, err := encryptWithSeedForParticipant(shareBytes[:], complainerPriv.PubKey().SerializeCompressed(), seed)
	require.NoError(t, err)

	sharesForComplainer := make([][]byte, 50)
	for i := range sharesForComplainer {
		sharesForComplainer[i] = ciphertext
	}

	epoch := types.EpochBLSData{
		EpochId:     77,
		ITotalSlots: 100,
		Participants: []types.BLSParticipantInfo{
			{
				Address:            "p0",
				Secp256K1PublicKey: dealerPriv.PubKey().SerializeCompressed(),
				SlotStartIndex:     0,
				SlotEndIndex:       9,
			},
			{
				Address:            "p1",
				Secp256K1PublicKey: complainerPriv.PubKey().SerializeCompressed(),
				SlotStartIndex:     10,
				SlotEndIndex:       59,
			},
			{
				Address:            "p2",
				Secp256K1PublicKey: observerPriv.PubKey().SerializeCompressed(),
				SlotStartIndex:     60,
				SlotEndIndex:       99,
			},
		},
		DealerParts: []*types.DealerPartStorage{
			{
				DealerAddress: "p0",
				Commitments:   [][]byte{mustMakeG2CommitmentForScalar(t, 5)},
				ParticipantShares: []*types.EncryptedSharesForParticipant{
					{EncryptedShares: [][]byte{}},
					{EncryptedShares: sharesForComplainer},
					{EncryptedShares: [][]byte{}},
				},
			},
			{
				DealerAddress: "p1",
				Commitments:   [][]byte{mustMakeG2CommitmentForScalar(t, 7)},
			},
			{
				DealerAddress: "p2",
				Commitments:   [][]byte{},
			},
		},
		VerificationSubmissions: []*types.VerificationVectorSubmission{
			{DealerValidity: []bool{true, false, true}},
			{DealerValidity: []bool{false, true, true}},
			{DealerValidity: []bool{true, false, true}},
		},
		DealerComplaints: []types.DealerComplaint{
			{
				DealerIndex:             0,
				ComplainerIndex:         1,
				DisputedSlotIndex:       10,
				DisputedCiphertextIndex: 0,
				ResponseSubmitted:       true,
				ResponseShareBytes:      shareBytes[:],
				ResponseOpeningMaterial: seed,
			},
		},
	}

	initialCandidates, err := k.determineValidDealersWithConsensus(&epoch, nil)
	require.NoError(t, err)
	require.Equal(t, []bool{false, false, false}, initialCandidates)

	dealerFaults, falseComplainersByDealer, err := k.adjudicateDealerComplaints(&epoch)
	require.NoError(t, err)
	require.Empty(t, dealerFaults)
	require.Contains(t, falseComplainersByDealer, 0)
	require.Contains(t, falseComplainersByDealer[0], 1)

	recomputedCandidates, err := k.determineValidDealersWithConsensus(&epoch, falseComplainersByDealer)
	require.NoError(t, err)
	require.Equal(t, []bool{true, false, false}, recomputedCandidates)

	finalValidDealers := make([]bool, len(recomputedCandidates))
	copy(finalValidDealers, recomputedCandidates)
	complainerFaults := flattenFalseComplainers(falseComplainersByDealer)
	applyComplaintFaultMaps(finalValidDealers, dealerFaults, complainerFaults)
	require.Equal(t, []bool{true, false, false}, finalValidDealers)
}

func TestDetermineValidDealersWithConsensus_ExcludedVerifiersAreDealerScoped(t *testing.T) {
	k := Keeper{}

	epoch := types.EpochBLSData{
		EpochId:     88,
		ITotalSlots: 100,
		Participants: []types.BLSParticipantInfo{
			{Address: "p0", SlotStartIndex: 0, SlotEndIndex: 9},
			{Address: "p1", SlotStartIndex: 10, SlotEndIndex: 39},
			{Address: "p2", SlotStartIndex: 40, SlotEndIndex: 79},
			{Address: "p3", SlotStartIndex: 80, SlotEndIndex: 99},
		},
		DealerParts: []*types.DealerPartStorage{
			{DealerAddress: "p0", Commitments: [][]byte{mustMakeG2CommitmentForScalar(t, 5)}},
			{DealerAddress: "p1", Commitments: [][]byte{mustMakeG2CommitmentForScalar(t, 7)}},
			{DealerAddress: "p2", Commitments: [][]byte{mustMakeG2CommitmentForScalar(t, 9)}},
			{DealerAddress: "p3", Commitments: [][]byte{mustMakeG2CommitmentForScalar(t, 11)}},
		},
		VerificationSubmissions: []*types.VerificationVectorSubmission{
			{DealerValidity: []bool{true, false, false, false}},
			{DealerValidity: []bool{true, true, true, false}},
			{DealerValidity: []bool{false, false, true, false}},
			{DealerValidity: []bool{true, false, true, true}},
		},
	}

	baseline, err := k.determineValidDealersWithConsensus(&epoch, nil)
	require.NoError(t, err)
	require.Equal(t, []bool{true, false, true, false}, baseline)

	excludedByDealer := map[int]map[int]struct{}{
		0: map[int]struct{}{1: {}},
	}
	recomputed, err := k.determineValidDealersWithConsensus(&epoch, excludedByDealer)
	require.NoError(t, err)
	require.Equal(t, []bool{false, false, true, false}, recomputed)
}

func mustMakeG2CommitmentForScalar(t *testing.T, scalar uint64) []byte {
	t.Helper()

	var g2Gen bls12381.G2Affine
	_, _, _, g2Gen = bls12381.Generators()

	var scalarBig big.Int
	scalarBig.SetUint64(scalar)

	var point bls12381.G2Affine
	point.ScalarMultiplication(&g2Gen, &scalarBig)

	commitment := point.Bytes()
	return commitment[:]
}
