package keeper

import (
	"bytes"
	"encoding/hex"
	"testing"

	"cosmossdk.io/log"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestSetBridgeMintPendingRefund_RejectsDuplicateRequestID(t *testing.T) {
	k, ctx := setupKeeperForPendingRefundGuardTests(t)
	requestID := bytes.Repeat([]byte{0xA1}, 32)
	requestKey := hex.EncodeToString(requestID)

	first := types.MsgRequestBridgeMint{
		Creator:            "gonka1firstmintrequestcreatoraddress00000000000",
		Amount:             "1000",
		DestinationAddress: "0x111",
		ChainId:            "ethereum",
	}
	second := types.MsgRequestBridgeMint{
		Creator:            "gonka1secondmintrequestcreatoraddress0000000000",
		Amount:             "2000",
		DestinationAddress: "0x222",
		ChainId:            "base",
	}

	require.NoError(t, k.setBridgeMintPendingRefund(ctx, requestID, &first))

	err := k.setBridgeMintPendingRefund(ctx, requestID, &second)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")

	stored, err := k.BridgeMintRefundsMap.Get(ctx, requestKey)
	require.NoError(t, err)
	require.Equal(t, first, stored)
}

func TestSetBridgeWithdrawalPendingRefund_RejectsDuplicateRequestID(t *testing.T) {
	k, ctx := setupKeeperForPendingRefundGuardTests(t)
	requestID := bytes.Repeat([]byte{0xB2}, 32)
	requestKey := hex.EncodeToString(requestID)

	first := types.MsgRequestBridgeWithdrawal{
		Creator:            "gonka1firstwithdrawcreatoraddress0000000000000",
		UserAddress:        "gonka1firstwithdrawuseraddress000000000000000",
		Amount:             "3000",
		DestinationAddress: "0x333",
	}
	firstRef := types.BridgeTokenReference{
		ChainId:         "ethereum",
		ContractAddress: "0x333",
	}
	second := types.MsgRequestBridgeWithdrawal{
		Creator:            "gonka1secondwithdrawcreatoraddress000000000000",
		UserAddress:        "gonka1secondwithdrawuseraddress00000000000000",
		Amount:             "4000",
		DestinationAddress: "0x444",
	}
	secondRef := types.BridgeTokenReference{
		ChainId:         "polygon",
		ContractAddress: "0x444",
	}

	require.NoError(t, k.setBridgeWithdrawalPendingRefund(ctx, requestID, &first, firstRef))

	err := k.setBridgeWithdrawalPendingRefund(ctx, requestID, &second, secondRef)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")

	stored, err := k.BridgeWithdrawalRefundsMap.Get(ctx, requestKey)
	require.NoError(t, err)
	require.Equal(t, first, stored)

	storedRef, err := k.BridgeWithdrawalTokenRefsMap.Get(ctx, requestKey)
	require.NoError(t, err)
	require.Equal(t, firstRef, storedRef)
}

func setupKeeperForPendingRefundGuardTests(t *testing.T) (Keeper, sdk.Context) {
	t.Helper()

	storeKey := storetypes.NewKVStoreKey(types.StoreKey)
	transientStoreKey := storetypes.NewTransientStoreKey(types.TransientStoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(storeKey, storetypes.StoreTypeIAVL, db)
	stateStore.MountStoreWithDB(transientStoreKey, storetypes.StoreTypeTransient, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)
	authorityBech32, err := sdk.Bech32ifyAddressBytes(sdk.GetConfig().GetBech32AccountAddrPrefix(), authority)
	require.NoError(t, err)

	k := NewKeeper(
		cdc,
		runtime.NewKVStoreService(storeKey),
		runtime.NewTransientStoreService(transientStoreKey),
		log.NewNopLogger(),
		authorityBech32,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())
	require.NoError(t, k.SetParams(ctx, types.DefaultParams()))

	return k, ctx
}
