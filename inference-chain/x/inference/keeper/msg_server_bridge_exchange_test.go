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

// TestBridgeExchange_OwnerAddressCaseNormalization verifies that two validators submitting
// the same bridge transaction with different casing on OwnerAddress and ReceiptsRoot
// are matched to the same pending transaction (not treated as separate transactions).
func TestBridgeExchange_OwnerAddressCaseNormalization(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	// Two distinct validators
	validator1 := "gonka13779rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w"
	validator2 := "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2"

	// Setup Epoch
	epochIndex := uint64(1)
	_ = k.SetEffectiveEpochIndex(ctx, epochIndex)

	epochGroupData := types.EpochGroupData{
		EpochIndex:   epochIndex,
		ModelId:      "",
		EpochGroupId: 1,
		TotalWeight:  100,
	}
	k.SetEpochGroupData(ctx, epochGroupData)

	// Mock accounts for both validators
	accAddr1, _ := sdk.AccAddressFromBech32(validator1)
	accAddr2, _ := sdk.AccAddressFromBech32(validator2)

	mocks.AccountKeeper.EXPECT().GetAccount(ctx, accAddr1).Return(
		&authtypes.BaseAccount{Address: validator1},
	).AnyTimes()
	mocks.AccountKeeper.EXPECT().GetAccount(ctx, accAddr2).Return(
		&authtypes.BaseAccount{Address: validator2},
	).AnyTimes()

	member1 := &group.GroupMember{
		GroupId: 1,
		Member:  &group.Member{Address: validator1, Weight: "10"},
	}
	member2 := &group.GroupMember{
		GroupId: 1,
		Member:  &group.Member{Address: validator2, Weight: "10"},
	}

	mocks.GroupKeeper.EXPECT().GroupMembers(ctx, gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{member1, member2},
		}, nil,
	).AnyTimes()

	// Register bridge contract
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{
		ChainId: "ethereum",
		Address: "0x123",
	})

	// Mixed-case OwnerAddress and ReceiptsRoot to test normalization.
	// ReceiptsRoot is a hex-encoded Ethereum trie root — case may vary across RPC providers.
	// OwnerAddress normalization is defense-in-depth against case-variant submissions.
	ownerMixed := "Gonka13779Rkgy6ke7cdj8f097pdvx34uvrlcqq8nq2w"
	ownerLower := strings.ToLower(ownerMixed)
	receiptsRootMixed := "0xABCDef1234567890abcDEF1234567890ABCDEF1234567890abcdef1234567890"
	receiptsRootLower := strings.ToLower(receiptsRootMixed)

	// First validator submits with mixed-case OwnerAddress and ReceiptsRoot
	msg1 := &types.MsgBridgeExchange{
		OriginChain:     "ethereum",
		ContractAddress: "0x123",
		OwnerAddress:    ownerMixed,
		Amount:          "100",
		BlockNumber:     "2000",
		ReceiptIndex:    "0",
		ReceiptsRoot:    receiptsRootMixed,
		Validator:       validator1,
	}

	resp1, err := ms.BridgeExchange(ctx, msg1)
	require.NoError(t, err, "First validator vote should succeed")
	require.NotEmpty(t, resp1.Id)

	// Second validator submits with all-lowercase OwnerAddress and ReceiptsRoot.
	// This must match the same pending transaction (not create a new one).
	msg2 := &types.MsgBridgeExchange{
		OriginChain:     "ethereum",
		ContractAddress: "0x123",
		OwnerAddress:    ownerLower,
		Amount:          "100",
		BlockNumber:     "2000",
		ReceiptIndex:    "0",
		ReceiptsRoot:    receiptsRootLower,
		Validator:       validator2,
	}

	resp2, err := ms.BridgeExchange(ctx, msg2)
	require.NoError(t, err, "Second validator vote with lowercase owner/receiptsRoot should succeed")
	// Both responses must reference the same transaction ID, proving normalization works
	require.Equal(t, resp1.Id, resp2.Id,
		"Transactions with case-variant OwnerAddress/ReceiptsRoot must resolve to the same bridge transaction")
}
