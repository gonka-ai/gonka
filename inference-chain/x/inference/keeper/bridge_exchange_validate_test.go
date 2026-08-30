package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func setupBridgeValidate(t *testing.T) (keeper.Keeper, sdk.Context, *keepertest.InferenceMocks, string, sdk.AccAddress) {
	t.Helper()
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	validator := testutil.Validator
	addr := sdk.MustAccAddressFromBech32(validator)
	epochIndex := uint64(335)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{
		Index:               epochIndex,
		PocStartBlockHeight: 1,
	}))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   epochIndex,
		ModelId:      "",
		EpochGroupId: 1,
		TotalWeight:  100,
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: []*types.ActiveParticipant{{Index: validator}},
	}))
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, addr).Return(true).AnyTimes()

	return k, ctx, &mocks, validator, addr
}

func TestValidateBridgeExchange_NotInTxEpochGroup(t *testing.T) {
	k, ctx, mocks, validator, _ := setupBridgeValidate(t)

	btx := &types.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
		EpochIndex:      335,
		Status:          types.BridgeTransactionStatus_BRIDGE_PENDING,
	}
	k.SetBridgeTransaction(ctx, btx)

	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{{
				GroupId: 1,
				Member:  &group.Member{Address: testutil.Validator2, Weight: "10"},
			}},
		}, nil,
	).AnyTimes()

	msg := &types.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
	}
	_, err := k.ValidateBridgeExchange(ctx, msg)
	require.ErrorIs(t, err, types.ErrBridgeValidatorNotInTxEpochGroup)
}

func TestValidateBridgeExchange_AlreadyValidated(t *testing.T) {
	k, ctx, mocks, validator, addr := setupBridgeValidate(t)

	btx := &types.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
		EpochIndex:      335,
		Status:          types.BridgeTransactionStatus_BRIDGE_PENDING,
	}
	k.SetBridgeTransaction(ctx, btx)
	require.NoError(t, k.AddBridgeTransactionValidator(ctx, btx, addr.String()))

	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{{
				GroupId: 1,
				Member:  &group.Member{Address: validator, Weight: "10"},
			}},
		}, nil,
	).AnyTimes()

	msg := &types.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
	}
	_, err := k.ValidateBridgeExchange(ctx, msg)
	require.ErrorIs(t, err, types.ErrBridgeAlreadyValidated)
}

func TestValidateBridgeExchange_CreateNotInActiveGroup(t *testing.T) {
	k, ctx, mocks, validator, _ := setupBridgeValidate(t)

	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{{
				GroupId: 1,
				Member:  &group.Member{Address: testutil.Validator2, Weight: "10"},
			}},
		}, nil,
	).AnyTimes()

	msg := &types.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "2000",
		ReceiptIndex:    "2",
		ReceiptsRoot:    "0xroot",
	}
	_, err := k.ValidateBridgeExchange(ctx, msg)
	require.ErrorIs(t, err, types.ErrBridgeValidatorNotInActiveGroup)
}

func TestValidateBridgeExchange_ContentMismatch(t *testing.T) {
	k, ctx, _, validator, _ := setupBridgeValidate(t)

	// Seed under the content key for proposed fields, then overwrite the
	// stored value with mismatched owner so GetBridgeTransactionByContent
	// finds a row that fails bridgeTransactionsEqual (defense-in-depth path).
	proposed := &types.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
		EpochIndex:      335,
		Status:          types.BridgeTransactionStatus_BRIDGE_PENDING,
	}
	k.SetBridgeTransaction(ctx, proposed)

	iter, err := k.BridgeTransactionsMap.Iterate(ctx, nil)
	require.NoError(t, err)
	keys, err := iter.Keys()
	iter.Close()
	require.NoError(t, err)
	require.Len(t, keys, 1)

	stored, err := k.BridgeTransactionsMap.Get(ctx, keys[0])
	require.NoError(t, err)
	stored.OwnerAddress = "gonka1different"
	require.NoError(t, k.BridgeTransactionsMap.Set(ctx, keys[0], stored))

	msg := &types.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
	}
	_, err = k.ValidateBridgeExchange(ctx, msg)
	require.ErrorIs(t, err, types.ErrBridgeContentMismatch)
}

func TestValidateBridgeExchange_CreateSuccess(t *testing.T) {
	k, ctx, mocks, validator, addr := setupBridgeValidate(t)

	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{{
				GroupId: 1,
				Member:  &group.Member{Address: validator, Weight: "25"},
			}},
		}, nil,
	).AnyTimes()

	msg := &types.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xAbC",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "2000",
		ReceiptIndex:    "2",
		ReceiptsRoot:    "0xroot",
	}
	validated, err := k.ValidateBridgeExchange(ctx, msg)
	require.NoError(t, err)
	require.True(t, validated.IsCreate)
	require.Nil(t, validated.ExistingTx)
	require.True(t, validated.ValidatorAddress.Equals(addr))
	require.Equal(t, int64(25), validated.ValidatorPower)
	require.Equal(t, int64(100), validated.TotalEpochPower)
	require.Equal(t, uint64(335), validated.EpochIndex)
	require.Equal(t, "0xabc", validated.ProposedTx.ContractAddress)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_PENDING, validated.ProposedTx.Status)
	require.Empty(t, validated.ProposedTx.Id)
}

func TestValidateBridgeExchange_VoteSuccess(t *testing.T) {
	k, ctx, mocks, validator, addr := setupBridgeValidate(t)

	btx := &types.BridgeTransaction{
		ChainId:              "ethereum",
		ContractAddress:      "0xabc",
		OwnerAddress:         "gonka1owner",
		Amount:               "100",
		BlockNumber:          "1000",
		ReceiptIndex:         "1",
		ReceiptsRoot:         "0xroot",
		EpochIndex:           335,
		Status:               types.BridgeTransactionStatus_BRIDGE_PENDING,
		TotalValidationPower: 10,
	}
	k.SetBridgeTransaction(ctx, btx)

	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{{
				GroupId: 1,
				Member:  &group.Member{Address: validator, Weight: "40"},
			}},
		}, nil,
	).AnyTimes()

	msg := &types.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
	}
	validated, err := k.ValidateBridgeExchange(ctx, msg)
	require.NoError(t, err)
	require.False(t, validated.IsCreate)
	require.NotNil(t, validated.ExistingTx)
	require.True(t, validated.ValidatorAddress.Equals(addr))
	require.Equal(t, int64(40), validated.ValidatorPower)
	require.Equal(t, int64(100), validated.TotalEpochPower)
	require.Equal(t, uint64(335), validated.EpochIndex)
}

func TestValidateBridgeExchange_DoesNotMutate(t *testing.T) {
	k, ctx, mocks, validator, addr := setupBridgeValidate(t)

	btx := &types.BridgeTransaction{
		ChainId:              "ethereum",
		ContractAddress:      "0xabc",
		OwnerAddress:         "gonka1owner",
		Amount:               "100",
		BlockNumber:          "1000",
		ReceiptIndex:         "1",
		ReceiptsRoot:         "0xroot",
		EpochIndex:           335,
		Status:               types.BridgeTransactionStatus_BRIDGE_PENDING,
		TotalValidationPower: 10,
	}
	k.SetBridgeTransaction(ctx, btx)
	other := sdk.MustAccAddressFromBech32(testutil.Validator2)
	require.NoError(t, k.AddBridgeTransactionValidator(ctx, btx, other.String()))

	before, found := k.GetBridgeTransactionByContent(ctx, btx)
	require.True(t, found)
	beforeValidators, err := k.ListBridgeTransactionValidators(ctx, before)
	require.NoError(t, err)
	beforeStatus := before.Status
	beforePower := before.TotalValidationPower

	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{{
				GroupId: 1,
				Member:  &group.Member{Address: validator, Weight: "40"},
			}},
		}, nil,
	).AnyTimes()

	msg := &types.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
	}
	_, err = k.ValidateBridgeExchange(ctx, msg)
	require.NoError(t, err)

	after, found := k.GetBridgeTransactionByContent(ctx, btx)
	require.True(t, found)
	afterValidators, err := k.ListBridgeTransactionValidators(ctx, after)
	require.NoError(t, err)
	require.Equal(t, beforeValidators, afterValidators)
	require.Equal(t, beforeStatus, after.Status)
	require.Equal(t, beforePower, after.TotalValidationPower)

	has, err := k.HasBridgeTransactionValidator(ctx, after, addr.String())
	require.NoError(t, err)
	require.False(t, has, "validate must not record the validator")
}

func TestValidateBridgeExchange_PreviousEpochActiveCanVoteExisting(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	validator := testutil.Validator
	addr := sdk.MustAccAddressFromBech32(validator)
	prevEpoch := uint64(334)
	currentEpoch := uint64(335)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{
		Index:               currentEpoch,
		PocStartBlockHeight: 2,
	}))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{
		Index:               prevEpoch,
		PocStartBlockHeight: 1,
	}))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   currentEpoch,
		ModelId:      "",
		EpochGroupId: 2,
		TotalWeight:  100,
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:   prevEpoch,
		ModelId:      "",
		EpochGroupId: 1,
		TotalWeight:  80,
	})
	// Active only in previous epoch — not current.
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      prevEpoch,
		Participants: []*types.ActiveParticipant{{Index: validator}},
	}))
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      currentEpoch,
		Participants: []*types.ActiveParticipant{},
	}))
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, addr).Return(true).AnyTimes()

	btx := &types.BridgeTransaction{
		ChainId:              "ethereum",
		ContractAddress:      "0xabc",
		OwnerAddress:         "gonka1owner",
		Amount:               "100",
		BlockNumber:          "1000",
		ReceiptIndex:         "1",
		ReceiptsRoot:         "0xroot",
		EpochIndex:           prevEpoch,
		Status:               types.BridgeTransactionStatus_BRIDGE_PENDING,
		TotalValidationPower: 10,
	}
	k.SetBridgeTransaction(ctx, btx)

	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{{
				GroupId: 1,
				Member:  &group.Member{Address: validator, Weight: "30"},
			}},
		}, nil,
	).AnyTimes()

	msg := &types.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
	}
	validated, err := k.ValidateBridgeExchange(ctx, msg)
	require.NoError(t, err)
	require.False(t, validated.IsCreate)
	require.Equal(t, prevEpoch, validated.EpochIndex)
	require.Equal(t, int64(30), validated.ValidatorPower)
	require.Equal(t, int64(80), validated.TotalEpochPower)
}
