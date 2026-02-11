package keeper_test

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
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

	// Setup Mocks

	// 1. AccountKeeper.GetAccount for Validator (both lower and upper)
	accAddr, _ := sdk.AccAddressFromBech32(validatorLower)

	// We expect GetAccount to be called. It just checks if account exists (not nil).
	mocks.AccountKeeper.EXPECT().GetAccount(ctx, accAddr).Return(
		&authtypes.BaseAccount{Address: validatorLower},
	).AnyTimes()

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

	// Register the bridge contract address
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{
		ChainId: "ethereum",
		Address: "0x123",
	})

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

func TestBridgeExchange_CaseInsensitiveContractAddress(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	// Register bridge contract with lowercase values
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{
		ChainId: "ethereum",
		Address: "0x123",
	})

	// Setup Validator
	validatorAddr := "gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w"

	// Setup Epoch
	epochIndex := uint64(1)
	_ = k.SetEffectiveEpochIndex(ctx, epochIndex)

	// Setup Epoch Group Data
	epochGroupData := types.EpochGroupData{
		EpochIndex:   epochIndex,
		ModelId:      "",
		EpochGroupId: 1,
		TotalWeight:  20,
	}
	k.SetEpochGroupData(ctx, epochGroupData)

	// Setup Mocks
	accAddr, _ := sdk.AccAddressFromBech32(validatorAddr)

	mocks.AccountKeeper.EXPECT().GetAccount(ctx, accAddr).Return(
		&authtypes.BaseAccount{Address: validatorAddr},
	).AnyTimes()

	member := &group.GroupMember{
		GroupId: 1,
		Member: &group.Member{
			Address: validatorAddr,
			Weight:  "10",
		},
	}

	mocks.GroupKeeper.EXPECT().GroupMembers(ctx, gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{member},
		}, nil,
	).AnyTimes()

	// Submit with MIXED CASE contract address and chain — should still match
	msg := &types.MsgBridgeExchange{
		OriginChain:     "Ethereum",  // uppercase E
		ContractAddress: "0X123",     // uppercase X
		OwnerAddress:    "0xabc",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		Validator:       validatorAddr,
	}

	_, err := ms.BridgeExchange(ctx, msg)
	require.NoError(t, err, "Case-insensitive contract address should be accepted")
}

func TestBridgeExchange_UnregisteredContractAddress(t *testing.T) {
	_, ms, ctx, _ := setupKeeperWithMocks(t)

	// Submit a bridge exchange with an unregistered contract address
	msg := &types.MsgBridgeExchange{
		OriginChain:     "ethereum",
		ContractAddress: "0xUnregistered",
		OwnerAddress:    "0xabc",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		Validator:       "gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w",
	}

	_, err := ms.BridgeExchange(ctx, msg)
	require.Error(t, err, "Should reject unregistered contract address")
	require.Contains(t, err.Error(), "unregistered bridge contract address")
}
