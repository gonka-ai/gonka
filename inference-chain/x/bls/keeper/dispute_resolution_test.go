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
