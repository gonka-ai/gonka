package keeper_test

import (
	"context"
	"math/big"
	"testing"

	bls12381 "github.com/consensys/gnark-crypto/ecc/bls12-381"
	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/keeper"
	"github.com/productscience/inference/x/bls/types"
)

func buildValidTestDealerCommitmentsAndProofs(t testing.TB, chainID string, epochID uint64, dealerAddr string, coeffs []fr.Element) ([][]byte, []types.CommitmentKnowledgeProof) {
	t.Helper()
	commitments := make([][]byte, len(coeffs))
	proofs := make([]types.CommitmentKnowledgeProof, len(coeffs))
	for i := range coeffs {
		commitment, proof, err := types.BuildCommitmentKnowledgeProof(chainID, epochID, dealerAddr, uint32(i), &coeffs[i])
		require.NoError(t, err)
		commitments[i] = commitment
		proofs[i] = proof
	}
	return commitments, proofs
}

func setupMsgServerDealer(t testing.TB) (keeper.Keeper, types.MsgServer, context.Context) {
	k, ctx := keepertest.BlsKeeper(t)
	return k, keeper.NewMsgServerImpl(k), ctx
}

func TestSubmitDealerPart_Success(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Setup test data
	epochID := uint64(1)
	dealerAddr := "dealer1"
	participant1Addr := "participant1"
	participant2Addr := "participant2"

	// Create epoch BLS data with participants
	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		ITotalSlots:               3,
		TSlotsDegree:              1,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 100, // Future deadline
		Participants: []types.BLSParticipantInfo{
			{
				Address:            dealerAddr,
				Secp256K1PublicKey: []byte("pubkey1"),
				PercentageWeight:   math.LegacyNewDec(33),
				SlotStartIndex:     0,
				SlotEndIndex:       0,
			},
			{
				Address:            participant1Addr,
				Secp256K1PublicKey: []byte("pubkey2"),
				PercentageWeight:   math.LegacyNewDec(33),
				SlotStartIndex:     1,
				SlotEndIndex:       1,
			},
			{
				Address:            participant2Addr,
				Secp256K1PublicKey: []byte("pubkey3"),
				PercentageWeight:   math.LegacyNewDec(34),
				SlotStartIndex:     2,
				SlotEndIndex:       2,
			},
		},
		DealerParts: []*types.DealerPartStorage{
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	coeffs := []fr.Element{fr.NewElement(7), fr.NewElement(3)}
	commitments, proofs := buildValidTestDealerCommitmentsAndProofs(t, ctx.ChainID(), epochID, dealerAddr, coeffs)

	// Create test message
	msg := &types.MsgSubmitDealerPart{
		Creator:          dealerAddr,
		EpochId:          epochID,
		Commitments:      commitments,
		CommitmentProofs: proofs,
		EncryptedSharesForParticipants: []types.EncryptedSharesForParticipant{
			{EncryptedShares: [][]byte{dummyEncryptedShare(1)}},
			{EncryptedShares: [][]byte{dummyEncryptedShare(2)}},
			{EncryptedShares: [][]byte{dummyEncryptedShare(3)}},
		},
	}

	// Execute
	resp, err := ms.SubmitDealerPart(goCtx, msg)

	// Verify
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Check that dealer part was stored
	updatedEpochBLSData, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)

	// Dealer should be at index 0
	dealerPart := updatedEpochBLSData.DealerParts[0]
	require.NotNil(t, dealerPart)
	assert.Equal(t, dealerAddr, dealerPart.DealerAddress)
	assert.Equal(t, msg.Commitments, dealerPart.Commitments)
	assert.Len(t, dealerPart.ParticipantShares, 3)

	// Verify participant shares were stored correctly
	for i, expectedShare := range msg.EncryptedSharesForParticipants {
		assert.Equal(t, expectedShare.EncryptedShares, dealerPart.ParticipantShares[i].EncryptedShares)
	}
}

func TestSubmitDealerPart_EpochNotFound(t *testing.T) {
	_, ms, goCtx := setupMsgServerDealer(t)

	msg := &types.MsgSubmitDealerPart{
		Creator: "dealer1",
		EpochId: 999, // Non-existent epoch
	}

	_, err := ms.SubmitDealerPart(goCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "epoch BLS data not found")
}

func TestSubmitDealerPart_WrongPhase(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	epochID := uint64(1)
	epochBLSData := types.EpochBLSData{
		EpochId:  epochID,
		DkgPhase: types.DKGPhase_DKG_PHASE_VERIFYING, // Wrong phase
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	msg := &types.MsgSubmitDealerPart{
		Creator: "dealer1",
		EpochId: epochID,
	}

	_, err := ms.SubmitDealerPart(goCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in dealing phase")
}

func TestSubmitDealerPart_DeadlinePassed(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	epochID := uint64(1)
	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() - 1, // Past deadline
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	msg := &types.MsgSubmitDealerPart{
		Creator: "dealer1",
		EpochId: epochID,
	}

	_, err := ms.SubmitDealerPart(goCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dealing phase deadline has passed")
}

func TestSubmitDealerPart_NotParticipant(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	epochID := uint64(1)
	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		ITotalSlots:               100,
		TSlotsDegree:              33,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 100,
		Participants: []types.BLSParticipantInfo{
			{
				Address:            "other_participant",
				Secp256K1PublicKey: []byte("pubkey1"),
				PercentageWeight:   math.LegacyNewDec(100),
				SlotStartIndex:     0,
				SlotEndIndex:       99,
			},
		},
		DealerParts: []*types.DealerPartStorage{
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	msg := &types.MsgSubmitDealerPart{
		Creator: "not_a_participant",
		EpochId: epochID,
	}

	_, err := ms.SubmitDealerPart(goCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a participant")
}

func TestSubmitDealerPart_AlreadySubmitted(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	epochID := uint64(1)
	dealerAddr := "dealer1"

	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		ITotalSlots:               100,
		TSlotsDegree:              33,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 100,
		Participants: []types.BLSParticipantInfo{
			{
				Address:            dealerAddr,
				Secp256K1PublicKey: []byte("pubkey1"),
				PercentageWeight:   math.LegacyNewDec(100),
				SlotStartIndex:     0,
				SlotEndIndex:       99,
			},
		},
		DealerParts: []*types.DealerPartStorage{
			{DealerAddress: dealerAddr, Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}}, // Already submitted
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	msg := &types.MsgSubmitDealerPart{
		Creator: dealerAddr,
		EpochId: epochID,
	}

	_, err := ms.SubmitDealerPart(goCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already submitted dealer part")
}

func TestSubmitDealerPart_WrongSharesLength(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	epochID := uint64(1)
	dealerAddr := "dealer1"

	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		ITotalSlots:               100,
		TSlotsDegree:              33,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 100,
		Participants: []types.BLSParticipantInfo{
			{
				Address:            dealerAddr,
				Secp256K1PublicKey: []byte("pubkey1"),
				PercentageWeight:   math.LegacyNewDec(50),
				SlotStartIndex:     0,
				SlotEndIndex:       49,
			},
			{
				Address:            "participant2",
				Secp256K1PublicKey: []byte("pubkey2"),
				PercentageWeight:   math.LegacyNewDec(50),
				SlotStartIndex:     50,
				SlotEndIndex:       99,
			},
		},
		DealerParts: []*types.DealerPartStorage{
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	msg := &types.MsgSubmitDealerPart{
		Creator: dealerAddr,
		EpochId: epochID,
		EncryptedSharesForParticipants: []types.EncryptedSharesForParticipant{
			// Only one share, but there are 2 participants
			{EncryptedShares: [][]byte{[]byte("share1")}},
		},
	}

	_, err := ms.SubmitDealerPart(goCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected encrypted shares for 2 participants, got 1")
}

func TestSubmitDealerPart_InvalidEncryptedSharesShape(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	epochID := uint64(1)
	dealerAddr := "dealer1"

	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		ITotalSlots:               2,
		TSlotsDegree:              1,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 100,
		Participants: []types.BLSParticipantInfo{
			{
				Address:            dealerAddr,
				Secp256K1PublicKey: []byte("pubkey1"),
				PercentageWeight:   math.LegacyNewDec(50),
				SlotStartIndex:     0,
				SlotEndIndex:       0,
			},
			{
				Address:            "participant2",
				Secp256K1PublicKey: []byte("pubkey2"),
				PercentageWeight:   math.LegacyNewDec(50),
				SlotStartIndex:     1,
				SlotEndIndex:       1,
			},
		},
		DealerParts: []*types.DealerPartStorage{
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	msg := &types.MsgSubmitDealerPart{
		Creator: dealerAddr,
		EpochId: epochID,
		Commitments: [][]byte{
			[]byte("commitment1"),
			[]byte("commitment2"),
		},
		EncryptedSharesForParticipants: []types.EncryptedSharesForParticipant{
			{EncryptedShares: [][]byte{[]byte("share1"), []byte("extra-share")}}, // expected 1, got 2
			{EncryptedShares: [][]byte{[]byte("share2")}},
		},
	}

	_, err := ms.SubmitDealerPart(goCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid encrypted shares for participant index 0")
}

func TestSubmitDealerPart_UndersizedCiphertextRejected(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	epochID := uint64(1)
	dealerAddr := "dealer1"

	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		ITotalSlots:               1,
		TSlotsDegree:              1,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 100,
		Participants: []types.BLSParticipantInfo{
			{
				Address:            dealerAddr,
				Secp256K1PublicKey: []byte("pubkey1"),
				PercentageWeight:   math.LegacyNewDec(100),
				SlotStartIndex:     0,
				SlotEndIndex:       0,
			},
		},
		DealerParts: []*types.DealerPartStorage{
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	short := make([]byte, 98)
	short[0] = 0x04
	msg := &types.MsgSubmitDealerPart{
		Creator: dealerAddr,
		EpochId: epochID,
		Commitments: [][]byte{
			[]byte("commitment1"),
			[]byte("commitment2"),
		},
		EncryptedSharesForParticipants: []types.EncryptedSharesForParticipant{
			{EncryptedShares: [][]byte{short}},
		},
	}

	_, err := ms.SubmitDealerPart(goCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "below ECIES minimum")
}

func TestSubmitDealerPart_WrongCommitmentsLength(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	epochID := uint64(1)
	dealerAddr := "dealer1"

	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		ITotalSlots:               100,
		TSlotsDegree:              2, // Expect exactly 3 commitments
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 100,
		Participants: []types.BLSParticipantInfo{
			{
				Address:            dealerAddr,
				Secp256K1PublicKey: []byte("pubkey1"),
				PercentageWeight:   math.LegacyNewDec(50),
				SlotStartIndex:     0,
				SlotEndIndex:       49,
			},
			{
				Address:            "participant2",
				Secp256K1PublicKey: []byte("pubkey2"),
				PercentageWeight:   math.LegacyNewDec(50),
				SlotStartIndex:     50,
				SlotEndIndex:       99,
			},
		},
		DealerParts: []*types.DealerPartStorage{
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	msg := &types.MsgSubmitDealerPart{
		Creator: dealerAddr,
		EpochId: epochID,
		Commitments: [][]byte{
			[]byte("commitment1"),
			[]byte("commitment2"), // Too few; expected 3
		},
		EncryptedSharesForParticipants: []types.EncryptedSharesForParticipant{
			{EncryptedShares: [][]byte{[]byte("share1")}},
			{EncryptedShares: [][]byte{[]byte("share2")}},
		},
	}

	_, err := ms.SubmitDealerPart(goCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected 3 commitments")
}

func TestSubmitDealerPart_EventEmission(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx)

	epochID := uint64(1)
	dealerAddr := "dealer1"

	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		ITotalSlots:               1,
		TSlotsDegree:              1,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 100,
		Participants: []types.BLSParticipantInfo{
			{
				Address:            dealerAddr,
				Secp256K1PublicKey: []byte("pubkey1"),
				PercentageWeight:   math.LegacyNewDec(100),
				SlotStartIndex:     0,
				SlotEndIndex:       0,
			},
		},
		DealerParts: []*types.DealerPartStorage{
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	coeffs := []fr.Element{fr.NewElement(11), fr.NewElement(5)}
	commitments, proofs := buildValidTestDealerCommitmentsAndProofs(t, ctx.ChainID(), epochID, dealerAddr, coeffs)

	msg := &types.MsgSubmitDealerPart{
		Creator:          dealerAddr,
		EpochId:          epochID,
		Commitments:      commitments,
		CommitmentProofs: proofs,
		EncryptedSharesForParticipants: []types.EncryptedSharesForParticipant{
			{EncryptedShares: [][]byte{dummyEncryptedShare(1)}},
		},
	}

	// Execute
	_, err := ms.SubmitDealerPart(goCtx, msg)
	require.NoError(t, err)

	// Check that event was emitted
	events := ctx.EventManager().Events()
	var dealerSubmittedEvent sdk.Event
	found := false
	for _, event := range events {
		if event.Type == "inference.bls.EventDealerPartSubmitted" {
			dealerSubmittedEvent = event
			found = true
			break
		}
	}

	require.True(t, found, "EventDealerPartSubmitted should be emitted")

	// Verify event attributes
	epochAttr := false
	dealerAttr := false
	for _, attr := range dealerSubmittedEvent.Attributes {
		if attr.Key == "epoch_id" {
			assert.Equal(t, "\"1\"", attr.Value)
			epochAttr = true
		}
		if attr.Key == "dealer_address" {
			assert.Equal(t, "\""+dealerAddr+"\"", attr.Value)
			dealerAttr = true
		}
	}
	assert.True(t, epochAttr, "Event should contain epoch_id")
	assert.True(t, dealerAttr, "Event should contain dealer_address")
}

func TestSubmitDealerPart_RogueKeyOrInvalidCommitmentProofsRejected(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx).WithChainID("gonka-mainnet")
	goCtx = ctx

	epochID := uint64(462)
	honestAddr := "honest_dealer"
	rogueAddr := "rogue_dealer"

	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		ITotalSlots:               4,
		TSlotsDegree:              1,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 10,
		Participants: []types.BLSParticipantInfo{
			{Address: honestAddr, PercentageWeight: math.LegacyNewDec(50), SlotStartIndex: 0, SlotEndIndex: 1},
			{Address: rogueAddr, PercentageWeight: math.LegacyNewDec(50), SlotStartIndex: 2, SlotEndIndex: 3},
		},
		DealerParts: []*types.DealerPartStorage{{}, {}},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	honestCoeffs := []fr.Element{fr.NewElement(42), fr.NewElement(9)}
	honestCommitments, honestProofs := buildValidTestDealerCommitmentsAndProofs(t, ctx.ChainID(), epochID, honestAddr, honestCoeffs)

	attackerKnownCoeffs := []fr.Element{fr.NewElement(99), fr.NewElement(5)}
	attackerCommitments, attackerProofs := buildValidTestDealerCommitmentsAndProofs(t, ctx.ChainID(), epochID, rogueAddr, attackerKnownCoeffs)

	_, _, _, g2Gen := bls12381.Generators()
	var targetGPK, honestC0, negHonestC0, rogueC0, honestC1, negHonestC1, rogueC1 bls12381.G2Affine
	targetGPK.ScalarMultiplication(&g2Gen, attackerKnownCoeffs[0].BigInt(new(big.Int)))
	require.NoError(t, honestC0.Unmarshal(honestCommitments[0]))
	negHonestC0.Neg(&honestC0)
	rogueC0.Add(&targetGPK, &negHonestC0)
	c0Bz := rogueC0.Bytes()

	require.NoError(t, honestC1.Unmarshal(honestCommitments[1]))
	negHonestC1.Neg(&honestC1)
	rogueC1 = negHonestC1
	c1Bz := rogueC1.Bytes()

	shares := []types.EncryptedSharesForParticipant{
		{EncryptedShares: [][]byte{dummyEncryptedShare(1), dummyEncryptedShare(2)}},
		{EncryptedShares: [][]byte{dummyEncryptedShare(3), dummyEncryptedShare(4)}},
	}

	// 1. Missing CommitmentProofs rejected
	_, err := ms.SubmitDealerPart(goCtx, &types.MsgSubmitDealerPart{
		Creator:                        rogueAddr,
		EpochId:                        epochID,
		Commitments:                    attackerCommitments,
		EncryptedSharesForParticipants: shares,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commitment_proofs count 0 does not match commitments count 2")

	// 2. Rogue C_{d,0} cancellation rejected (both forged proof and replayed honest proof)
	for _, proofs := range [][]types.CommitmentKnowledgeProof{attackerProofs, honestProofs} {
		_, err := ms.SubmitDealerPart(goCtx, &types.MsgSubmitDealerPart{
			Creator:                        rogueAddr,
			EpochId:                        epochID,
			Commitments:                    [][]byte{c0Bz[:], attackerCommitments[1]},
			CommitmentProofs:               proofs,
			EncryptedSharesForParticipants: shares,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid commitment knowledge proof")
	}

	// 3. Valid known C_{d,0} with rogue higher coefficient C_{d,1} is ALSO rejected!
	_, err = ms.SubmitDealerPart(goCtx, &types.MsgSubmitDealerPart{
		Creator:                        rogueAddr,
		EpochId:                        epochID,
		Commitments:                    [][]byte{attackerCommitments[0], c1Bz[:]},
		CommitmentProofs:               attackerProofs,
		EncryptedSharesForParticipants: shares,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid commitment knowledge proof: commitment_proofs[1]")
}

func TestSubmitDealerPart_ZeroSlotShareRootTrapRejected(t *testing.T) {
	k, ms, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx).WithChainID("gonka-mainnet")
	goCtx = ctx

	epochID := uint64(462)
	trapDealer := "dealer5"

	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		ITotalSlots:               35,
		TSlotsDegree:              2,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 10,
		Participants: []types.BLSParticipantInfo{
			{Address: trapDealer, PercentageWeight: math.LegacyNewDec(50), SlotStartIndex: 0, SlotEndIndex: 28},
			{Address: "victim", PercentageWeight: math.LegacyNewDec(50), SlotStartIndex: 29, SlotEndIndex: 34},
		},
		DealerParts: []*types.DealerPartStorage{{}, {}},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	// Exact Block-7122278 trap construction: f(x) = (x - 30)(x + 7) = -210 - 23x + x^2,
	// where every scalar coefficient is known (so all 3 Schnorr PoKs are 100% valid!),
	// yet f(x) evaluates to 0 at slot 29 (x = 30).
	var c0, c1, c2 fr.Element
	c0.SetUint64(210)
	c0.Neg(&c0)
	c1.SetUint64(23)
	c1.Neg(&c1)
	c2.SetOne()

	commitments, validProofs := buildValidTestDealerCommitmentsAndProofs(t, ctx.ChainID(), epochID, trapDealer, []fr.Element{c0, c1, c2})

	victimShares := make([][]byte, 6)
	for i := range victimShares {
		victimShares[i] = dummyEncryptedShare(byte(40 + i))
	}
	selfShares := make([][]byte, 29)
	for i := range selfShares {
		selfShares[i] = dummyEncryptedShare(byte(1 + i))
	}

	msg := &types.MsgSubmitDealerPart{
		Creator:          trapDealer,
		EpochId:          epochID,
		Commitments:      commitments,
		CommitmentProofs: validProofs,
		EncryptedSharesForParticipants: []types.EncryptedSharesForParticipant{
			{EncryptedShares: selfShares},
			{EncryptedShares: victimShares},
		},
	}

	_, err := ms.SubmitDealerPart(goCtx, msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dealer commitment polynomial evaluates to identity at slot 29")
}

func TestDealerPartAttempts_CappedAndClearedAfterDealingPhase(t *testing.T) {
	k, _, goCtx := setupMsgServerDealer(t)
	ctx := sdk.UnwrapSDKContext(goCtx).WithChainID("gonka-mainnet")

	const epochID = uint64(500)
	const dealerAddr = "gonka1dealer"
	epochBLSData := types.EpochBLSData{
		EpochId:                   epochID,
		ITotalSlots:               4,
		TSlotsDegree:              1,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 5,
		Participants: []types.BLSParticipantInfo{
			{Address: dealerAddr, PercentageWeight: math.LegacyNewDec(100), SlotStartIndex: 0, SlotEndIndex: 3},
		},
	}
	require.NoError(t, k.SetEpochBLSData(ctx, epochBLSData))

	msg := &types.MsgSubmitDealerPart{Creator: dealerAddr, EpochId: epochID}

	// Non-participant / wrong epoch do not consume dealerAddr's attempts.
	require.Error(t, k.CheckAndRecordDealerPartAttempt(ctx, &types.MsgSubmitDealerPart{Creator: "outsider", EpochId: epochID}, true))
	require.Error(t, k.CheckAndRecordDealerPartAttempt(ctx, &types.MsgSubmitDealerPart{Creator: dealerAddr, EpochId: epochID + 1}, true))
	attempts, err := k.GetDealerPartAttempts(ctx, epochID, 0)
	require.NoError(t, err)
	require.Equal(t, uint32(0), attempts)

	// Default cap is 3 attempts.
	for i := uint32(1); i <= types.DefaultMaxDealerAttemptsPerEpoch; i++ {
		require.NoError(t, k.CheckAndRecordDealerPartAttempt(ctx, msg, true))
		attempts, err = k.GetDealerPartAttempts(ctx, epochID, 0)
		require.NoError(t, err)
		require.Equal(t, i, attempts)
	}
	err = k.CheckAndRecordDealerPartAttempt(ctx, msg, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max dealer part attempts")

	// Governance raising the cap immediately unlocks another attempt.
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.MaxDealerAttemptsPerEpoch = 4
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.CheckAndRecordDealerPartAttempt(ctx, msg, true))
	require.Error(t, k.CheckAndRecordDealerPartAttempt(ctx, msg, true))

	// Unset (0) fails closed rather than silently defaulting.
	params.MaxDealerAttemptsPerEpoch = 0
	require.NoError(t, k.SetParams(ctx, params))
	err = k.CheckAndRecordDealerPartAttempt(ctx, msg, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_dealer_attempts_per_epoch is not set")

	params.MaxDealerAttemptsPerEpoch = types.DefaultMaxDealerAttemptsPerEpoch
	require.NoError(t, k.SetParams(ctx, params))

	// Transitioning out of DEALING clears per-participant attempt counters.
	rehydrated, err := k.GetEpochBLSData(ctx, epochID)
	require.NoError(t, err)
	require.NoError(t, k.TransitionToVerifyingPhase(ctx, &rehydrated))
	attempts, err = k.GetDealerPartAttempts(ctx, epochID, 0)
	require.NoError(t, err)
	require.Equal(t, uint32(0), attempts)
}
