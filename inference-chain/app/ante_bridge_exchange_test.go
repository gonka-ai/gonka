package app

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	protov2 "google.golang.org/protobuf/proto"
)

type bridgeTestTx struct {
	msgs []sdk.Msg
}

func (t bridgeTestTx) GetMsgs() []sdk.Msg                    { return t.msgs }
func (t bridgeTestTx) ValidateBasic() error                  { return nil }
func (t bridgeTestTx) GetMsgsV2() ([]protov2.Message, error) { return nil, nil }

func setupBridgeAnte(t *testing.T) (keeper.Keeper, sdk.Context, *keepertest.InferenceMocks, string, sdk.AccAddress) {
	t.Helper()
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	validator := testutil.Validator
	addr := sdk.MustAccAddressFromBech32(validator)
	epochIndex := uint64(335)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))
	require.NoError(t, k.SetEpoch(ctx, &inferencetypes.Epoch{
		Index:               epochIndex,
		PocStartBlockHeight: 1,
	}))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex:   epochIndex,
		ModelId:      "",
		EpochGroupId: 1,
		TotalWeight:  100,
	})
	require.NoError(t, k.SetActiveParticipants(ctx, inferencetypes.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: []*inferencetypes.ActiveParticipant{{Index: validator}},
	}))
	mocks.AccountKeeper.EXPECT().HasAccount(ctx, addr).Return(true).AnyTimes()

	return k, ctx, &mocks, validator, addr
}

func TestBridgeExchangeEarlyReject_NotInTxEpochGroup_CheckTx(t *testing.T) {
	k, ctx, mocks, validator, _ := setupBridgeAnte(t)

	// Kaitaku production shape: existing COMPLETED tx; validator is in ActiveParticipants
	// but not in stamped epoch SDK group_members.
	btx := &inferencetypes.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "32914000000000",
		BlockNumber:     "25586367",
		ReceiptIndex:    "144",
		ReceiptsRoot:    "0xroot",
		EpochIndex:      335,
		Status:          inferencetypes.BridgeTransactionStatus_BRIDGE_COMPLETED,
	}
	k.SetBridgeTransaction(ctx, btx)

	// WithIsCheckTx clones ctx; match on any context.
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{{
				GroupId: 1,
				Member:  &group.Member{Address: testutil.Validator2, Weight: "10"},
			}},
		}, nil,
	).AnyTimes()

	decorator := NewBridgeExchangeEarlyRejectDecorator(&k)
	msg := &inferencetypes.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "32914000000000",
		BlockNumber:     "25586367",
		ReceiptIndex:    "144",
		ReceiptsRoot:    "0xroot",
	}
	_, err := decorator.AnteHandle(ctx.WithIsCheckTx(true), bridgeTestTx{msgs: []sdk.Msg{msg}}, false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) { return ctx, nil })
	require.ErrorIs(t, err, inferencetypes.ErrBridgeValidatorNotInTxEpochGroup)
	require.Contains(t, err.Error(), inferencetypes.ErrBridgeValidatorNotInTxEpochGroup.Error())

	// Prove the registered message still matches API classifyBridgeExchangeSubmit after Sync wraps Code!=0.
	apiShaped := "bridge exchange failed: code=5 rawLog=" + err.Error()
	require.Contains(t, strings.ToLower(apiShaped), strings.ToLower(inferencetypes.ErrBridgeValidatorNotInTxEpochGroup.Error()))
}

func TestBridgeExchangeEarlyReject_AlreadyValidated_CheckTx(t *testing.T) {
	k, ctx, mocks, validator, addr := setupBridgeAnte(t)

	btx := &inferencetypes.BridgeTransaction{
		ChainId:         "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
		EpochIndex:      335,
		Status:          inferencetypes.BridgeTransactionStatus_BRIDGE_PENDING,
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

	decorator := NewBridgeExchangeEarlyRejectDecorator(&k)
	msg := &inferencetypes.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
	}
	_, err := decorator.AnteHandle(ctx.WithIsCheckTx(true), bridgeTestTx{msgs: []sdk.Msg{msg}}, false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) { return ctx, nil })
	require.ErrorIs(t, err, inferencetypes.ErrBridgeAlreadyValidated)
	require.Contains(t, err.Error(), inferencetypes.ErrBridgeAlreadyValidated.Error())
}

func TestBridgeExchangeEarlyReject_CreateNotInSDKGroup_CheckTx(t *testing.T) {
	k, ctx, mocks, validator, _ := setupBridgeAnte(t)

	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{{
				GroupId: 1,
				Member:  &group.Member{Address: testutil.Validator2, Weight: "10"},
			}},
		}, nil,
	).AnyTimes()

	decorator := NewBridgeExchangeEarlyRejectDecorator(&k)
	msg := &inferencetypes.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "2000",
		ReceiptIndex:    "2",
		ReceiptsRoot:    "0xroot",
	}
	_, err := decorator.AnteHandle(ctx.WithIsCheckTx(true), bridgeTestTx{msgs: []sdk.Msg{msg}}, false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) { return ctx, nil })
	require.ErrorIs(t, err, inferencetypes.ErrBridgeValidatorNotInActiveGroup)
	require.Contains(t, err.Error(), inferencetypes.ErrBridgeValidatorNotInActiveGroup.Error())
}

func TestBridgeExchangeEarlyReject_BypassesDeliverTx(t *testing.T) {
	k, ctx, mocks, validator, _ := setupBridgeAnte(t)

	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{Members: nil}, nil,
	).AnyTimes()

	decorator := NewBridgeExchangeEarlyRejectDecorator(&k)
	msg := &inferencetypes.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "2000",
		ReceiptIndex:    "2",
		ReceiptsRoot:    "0xroot",
	}
	_, err := decorator.AnteHandle(ctx.WithIsCheckTx(false), bridgeTestTx{msgs: []sdk.Msg{msg}}, false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) { return ctx, nil })
	require.NoError(t, err)
}

func TestBridgeExchangeEarlyReject_NotActivePermission_CheckTx(t *testing.T) {
	k, ctx, _, _, _ := setupBridgeAnte(t)

	outsider := sdk.AccAddress([]byte("outsider____________")).String()
	decorator := NewBridgeExchangeEarlyRejectDecorator(&k)
	msg := &inferencetypes.MsgBridgeExchange{
		Validator:       outsider,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "2000",
		ReceiptIndex:    "2",
		ReceiptsRoot:    "0xroot",
	}
	_, err := decorator.AnteHandle(ctx.WithIsCheckTx(true), bridgeTestTx{msgs: []sdk.Msg{msg}}, false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) { return ctx, nil })
	require.ErrorIs(t, err, inferencetypes.ErrActiveParticipantNotFound)
	require.Contains(t, err.Error(), inferencetypes.ErrActiveParticipantNotFound.Error())
}

func TestBridgeExchangeEarlyReject_EligibleVote_CheckTxPasses(t *testing.T) {
	k, ctx, mocks, validator, _ := setupBridgeAnte(t)

	btx := &inferencetypes.BridgeTransaction{
		ChainId:              "ethereum",
		ContractAddress:      "0xabc",
		OwnerAddress:         "gonka1owner",
		Amount:               "100",
		BlockNumber:          "1000",
		ReceiptIndex:         "1",
		ReceiptsRoot:         "0xroot",
		EpochIndex:           335,
		Status:               inferencetypes.BridgeTransactionStatus_BRIDGE_PENDING,
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

	calledNext := false
	decorator := NewBridgeExchangeEarlyRejectDecorator(&k)
	msg := &inferencetypes.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "1000",
		ReceiptIndex:    "1",
		ReceiptsRoot:    "0xroot",
	}
	_, err := decorator.AnteHandle(ctx.WithIsCheckTx(true), bridgeTestTx{msgs: []sdk.Msg{msg}}, false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			calledNext = true
			return ctx, nil
		})
	require.NoError(t, err)
	require.True(t, calledNext)
}

func TestBridgeExchangeEarlyReject_EligibleCreate_CheckTxPasses(t *testing.T) {
	k, ctx, mocks, validator, _ := setupBridgeAnte(t)

	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{
			Members: []*group.GroupMember{{
				GroupId: 1,
				Member:  &group.Member{Address: validator, Weight: "40"},
			}},
		}, nil,
	).AnyTimes()

	calledNext := false
	decorator := NewBridgeExchangeEarlyRejectDecorator(&k)
	msg := &inferencetypes.MsgBridgeExchange{
		Validator:       validator,
		OriginChain:     "ethereum",
		ContractAddress: "0xabc",
		OwnerAddress:    "gonka1owner",
		Amount:          "100",
		BlockNumber:     "2000",
		ReceiptIndex:    "2",
		ReceiptsRoot:    "0xroot",
	}
	_, err := decorator.AnteHandle(ctx.WithIsCheckTx(true), bridgeTestTx{msgs: []sdk.Msg{msg}}, false,
		func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
			calledNext = true
			return ctx, nil
		})
	require.NoError(t, err)
	require.True(t, calledNext)
}
