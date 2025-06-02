package keeper_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/bls/keeper"
	"github.com/productscience/inference/x/bls/types"
)

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
		ITotalSlots:               100,
		TSlotsDegree:              33,
		DkgPhase:                  types.DKGPhase_DKG_PHASE_DEALING,
		DealingPhaseDeadlineBlock: ctx.BlockHeight() + 100, // Future deadline
		Participants: []types.BLSParticipantInfo{
			{
				Address:            dealerAddr,
				Secp256K1PublicKey: []byte("pubkey1"),
				PercentageWeight:   math.LegacyNewDec(33),
				SlotStartIndex:     0,
				SlotEndIndex:       32,
			},
			{
				Address:            participant1Addr,
				Secp256K1PublicKey: []byte("pubkey2"),
				PercentageWeight:   math.LegacyNewDec(33),
				SlotStartIndex:     33,
				SlotEndIndex:       65,
			},
			{
				Address:            participant2Addr,
				Secp256K1PublicKey: []byte("pubkey3"),
				PercentageWeight:   math.LegacyNewDec(34),
				SlotStartIndex:     66,
				SlotEndIndex:       99,
			},
		},
		DealerParts: []*types.DealerPartStorage{
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	// Create test message
	msg := &types.MsgSubmitDealerPart{
		Creator: dealerAddr,
		EpochId: epochID,
		Commitments: [][]byte{
			[]byte("commitment1"),
			[]byte("commitment2"),
		},
		EncryptedSharesForParticipants: []types.EncryptedSharesForParticipant{
			{EncryptedShares: [][]byte{[]byte("share1_for_dealer")}},
			{EncryptedShares: [][]byte{[]byte("share1_for_p1")}},
			{EncryptedShares: [][]byte{[]byte("share1_for_p2")}},
		},
	}

	// Execute
	resp, err := ms.SubmitDealerPart(goCtx, msg)

	// Verify
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Check that dealer part was stored
	updatedEpochBLSData, found := k.GetEpochBLSData(ctx, epochID)
	require.True(t, found)

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
	assert.Contains(t, err.Error(), "epoch 999 not found")
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

func TestSubmitDealerPart_EventEmission(t *testing.T) {
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
			{DealerAddress: "", Commitments: [][]byte{}, ParticipantShares: []*types.EncryptedSharesForParticipant{}},
		},
	}
	k.SetEpochBLSData(ctx, epochBLSData)

	msg := &types.MsgSubmitDealerPart{
		Creator: dealerAddr,
		EpochId: epochID,
		EncryptedSharesForParticipants: []types.EncryptedSharesForParticipant{
			{EncryptedShares: [][]byte{[]byte("share1")}},
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
