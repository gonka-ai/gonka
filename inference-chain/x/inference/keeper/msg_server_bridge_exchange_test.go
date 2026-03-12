package keeper_test

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestBridgeExchange_DoubleVoteCaseBypass(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	// Setup Validator
	validatorLower := "gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w"
	validatorUpper := strings.ToUpper(validatorLower)

	// Setup Epoch
	epochIndex := uint64(1)
	_ = k.SetEffectiveEpochIndex(ctx, epochIndex)

	// Setup Epoch Group Data
	epochGroupData := types.EpochGroupData{
		EpochIndex:   epochIndex,
		ModelId:      "", // Default for main group
		EpochGroupId: 1,
		TotalWeight:  20,
	}
	k.SetEpochGroupData(ctx, epochGroupData)

	// Set active participants
	k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: []*types.ActiveParticipant{{Index: validatorLower}},
	})

	// Setup Mocks

	// 1. AccountKeeper.HasAccount for Validator (both lower and upper)
	accAddr, _ := sdk.AccAddressFromBech32(validatorLower)

	// We expect HasAccount to be called.
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, accAddr).Return(true).AnyTimes()

	// 2. GroupKeeper.GroupMembers
	// Called when checking if validator is in epoch group.
	member := &group.GroupMember{
		GroupId: 1,
		Member: &group.Member{
			Address: validatorLower,
			Weight:  "10",
		},
	}

	mocks.GroupKeeper.EXPECT().GroupMembers(ctx, gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{member},
		}, nil,
	).AnyTimes()

	// First Vote (Lowercase)
	msg1 := &types.MsgBridgeExchange{
		OriginChain:     "ethereum",
		ContractAddress: "0x123",
		OwnerAddress:    "0xabc",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		Validator:       validatorLower,
	}

	_, err := ms.BridgeExchange(ctx, msg1)
	require.NoError(t, err, "First vote should succeed")

	// Second Vote (Uppercase)
	msg2 := &types.MsgBridgeExchange{
		OriginChain:     "ethereum",
		ContractAddress: "0x123",
		OwnerAddress:    "0xabc",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		Validator:       validatorUpper, // Uppercase
	}

	// This should fail if fixed, but succeeds if vulnerable
	_, err = ms.BridgeExchange(ctx, msg2)

	// We assert that it fails (expecting the fix to prevent this)
	require.Error(t, err, "Second vote should fail as duplicate")
	if err != nil {
		require.Contains(t, err.Error(), "validator has already validated this transaction")
	}
}

func TestBridgeExchange_NonActiveValidatorRejected(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	// Setup an unauthorized Validator
	accAddr := sdk.AccAddress([]byte("unauthorized_______"))
	unauthorizedValidator := accAddr.String()

	// Setup Epoch
	epochIndex := uint64(1)
	_ = k.SetEffectiveEpochIndex(ctx, epochIndex)

	// Note: We deliberately DO NOT add this validator to the ActiveParticipants cache
	// so the permission framework should reject it immediately.

	// Mock account keeper just to avoid panics on basic address checks
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, accAddr).Return(true).AnyTimes()

	msg := &types.MsgBridgeExchange{
		OriginChain:     "ethereum",
		ContractAddress: "0x123",
		OwnerAddress:    "0xabc",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		Validator:       unauthorizedValidator,
	}

	_, err := ms.BridgeExchange(ctx, msg)

	// We assert that it fails because the permission check intercepts it
	require.Error(t, err, "Vote from non-active participant should fail at the permission level")
	if err != nil {
		require.Contains(t, err.Error(), "participant is not active")
	}
}
