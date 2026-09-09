package keeper_test

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
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

	require.ErrorIs(t, err, types.ErrBridgeAlreadyValidated)
	require.Contains(t, err.Error(), types.ErrBridgeAlreadyValidated.Error())
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

	// CheckPermission (Active | PreviousActive) gates the handler; ante uses
	// ValidateBridgeExchange which re-checks the same active set.
	require.ErrorIs(t, err, types.ErrActiveParticipantNotFound)
	require.Contains(t, err.Error(), types.ErrActiveParticipantNotFound.Error())
}

func TestBridgeExchange_EligibleVoteRecords(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	validator := testutil.Validator
	addr := sdk.MustAccAddressFromBech32(validator)
	other := sdk.MustAccAddressFromBech32(testutil.Validator2)
	epochIndex := uint64(1)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   epochIndex,
		ModelId:      "",
		EpochGroupId: 1,
		TotalWeight:  100, // majority requires 51; keep vote under threshold
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: []*types.ActiveParticipant{{Index: validator}, {Index: other.String()}},
	}))
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, addr).Return(true).AnyTimes()
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, other).Return(true).AnyTimes()
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{
				{GroupId: 1, Member: &group.Member{Address: validator, Weight: "10"}},
				{GroupId: 1, Member: &group.Member{Address: other.String(), Weight: "10"}},
			},
		}, nil,
	).AnyTimes()

	btx := &types.BridgeTransaction{
		ChainId:              "ethereum",
		ContractAddress:      "0x123",
		OwnerAddress:         "0xabc",
		Amount:               "100",
		BlockNumber:          "1000",
		ReceiptIndex:         "1",
		EpochIndex:           epochIndex,
		Status:               types.BridgeTransactionStatus_BRIDGE_PENDING,
		TotalValidationPower: 10,
	}
	k.SetBridgeTransaction(ctx, btx)
	require.NoError(t, k.AddBridgeTransactionValidator(ctx, btx, other.String()))

	msg := &types.MsgBridgeExchange{
		OriginChain:     "ethereum",
		ContractAddress: "0x123",
		OwnerAddress:    "0xabc",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		Validator:       validator,
	}
	_, err := ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)

	after, found := k.GetBridgeTransactionByContent(ctx, btx)
	require.True(t, found)
	has, err := k.HasBridgeTransactionValidator(ctx, after, addr.String())
	require.NoError(t, err)
	require.True(t, has)
}

func TestBridgeExchange_EligibleCreateStores(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	validator := testutil.Validator
	addr := sdk.MustAccAddressFromBech32(validator)
	epochIndex := uint64(1)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   epochIndex,
		ModelId:      "",
		EpochGroupId: 1,
		TotalWeight:  20,
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: []*types.ActiveParticipant{{Index: validator}},
	}))
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, addr).Return(true).AnyTimes()
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{
				{GroupId: 1, Member: &group.Member{Address: validator, Weight: "10"}},
			},
		}, nil,
	).AnyTimes()

	msg := &types.MsgBridgeExchange{
		OriginChain:     "ethereum",
		ContractAddress: "0x123",
		OwnerAddress:    "0xabc",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		Validator:       validator,
	}
	resp, err := ms.BridgeExchange(ctx, msg)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Id)

	stored, found := k.GetBridgeTransactionByContent(ctx, &types.BridgeTransaction{
		ChainId:         msg.OriginChain,
		ContractAddress: strings.ToLower(msg.ContractAddress),
		OwnerAddress:    msg.OwnerAddress,
		Amount:          msg.Amount,
		BlockNumber:     msg.BlockNumber,
		ReceiptIndex:    msg.ReceiptIndex,
	})
	require.True(t, found)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_PENDING, stored.Status)
	has, err := k.HasBridgeTransactionValidator(ctx, stored, addr.String())
	require.NoError(t, err)
	require.True(t, has)
}

func TestBridgeExchange_NotInTxEpochGroup(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	validator := testutil.Validator
	addr := sdk.MustAccAddressFromBech32(validator)
	epochIndex := uint64(1)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   epochIndex,
		ModelId:      "",
		EpochGroupId: 1,
		TotalWeight:  20,
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: []*types.ActiveParticipant{{Index: validator}},
	}))
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, addr).Return(true).AnyTimes()
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{Members: nil}, nil,
	).AnyTimes()

	btx := &types.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0x123",
		OwnerAddress:    "0xabc",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		EpochIndex:      epochIndex,
		Status:          types.BridgeTransactionStatus_BRIDGE_PENDING,
	}
	k.SetBridgeTransaction(ctx, btx)

	msg := &types.MsgBridgeExchange{
		OriginChain:     "ethereum",
		ContractAddress: "0x123",
		OwnerAddress:    "0xabc",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		Validator:       validator,
	}
	_, err := ms.BridgeExchange(ctx, msg)
	require.ErrorIs(t, err, types.ErrBridgeValidatorNotInTxEpochGroup)
	require.Contains(t, err.Error(), types.ErrBridgeValidatorNotInTxEpochGroup.Error())
}

func TestBridgeExchange_CreateNotInActiveGroup(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	validator := testutil.Validator
	addr := sdk.MustAccAddressFromBech32(validator)
	epochIndex := uint64(1)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   epochIndex,
		ModelId:      "",
		EpochGroupId: 1,
		TotalWeight:  20,
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: []*types.ActiveParticipant{{Index: validator}},
	}))
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, addr).Return(true).AnyTimes()
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{Members: nil}, nil,
	).AnyTimes()

	msg := &types.MsgBridgeExchange{
		OriginChain:     "ethereum",
		ContractAddress: "0x123",
		OwnerAddress:    "0xabc",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		Validator:       validator,
	}
	_, err := ms.BridgeExchange(ctx, msg)
	require.ErrorIs(t, err, types.ErrBridgeValidatorNotInActiveGroup)
	require.Contains(t, err.Error(), types.ErrBridgeValidatorNotInActiveGroup.Error())
}
