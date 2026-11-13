package keeper_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/math"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const bridgeTestEpoch = uint64(415)

// setupBridgeEpoch stores a signed BLS epoch and a root epoch group of 30 members,
// and returns the trace marker of a root group data read.
func setupBridgeEpoch(t *testing.T, k keeper.Keeper, ctx sdk.Context) string {
	blsK, ok := k.BlsKeeper.(blskeeper.Keeper)
	require.True(t, ok)
	require.NoError(t, blsK.SetEpochBLSData(ctx, blstypes.EpochBLSData{
		EpochId:        bridgeTestEpoch,
		DkgPhase:       blstypes.DKGPhase_DKG_PHASE_SIGNED,
		GroupPublicKey: []byte{1},
	}))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, bridgeTestEpoch))
	data := types.EpochGroupData{EpochIndex: bridgeTestEpoch, SubGroupModels: []string{"model-a", "model-b", "model-c"}}
	for i := 0; i < 30; i++ {
		addr := sdk.AccAddress([]byte(fmt.Sprintf("member_%013d", i))).String()
		data.ValidationWeights = append(data.ValidationWeights, &types.ValidationWeight{MemberAddress: addr, Weight: 1000, Reputation: 100})
		data.MemberSeedSignatures = append(data.MemberSeedSignatures, &types.SeedSignature{MemberAddress: addr, Signature: strings.Repeat("ab", 64)})
	}
	k.SetEpochGroupData(ctx, data)

	m := k.EpochGroupDataMap
	key, err := collections.EncodeKeyWithPrefix(m.GetPrefix(), m.KeyCodec(), collections.Join(bridgeTestEpoch, ""))
	require.NoError(t, err)
	return `"operation":"read","key":"` + base64.StdEncoding.EncodeToString(key) + `"`
}

// The bridge handlers need only the epoch index: they check the root group exists
// without decoding it.
func TestRequestBridgeMint_DoesNotReadRootGroupData(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	readOp := setupBridgeEpoch(t, k, ctx)

	signer, err := sdk.AccAddressFromBech32(testutil.Creator)
	require.NoError(t, err)
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), signer).Return(true).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), signer, types.BaseCoin).Return(sdk.NewCoin(types.BaseCoin, math.NewInt(1000))).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), signer, types.BridgeEscrowAccName, gomock.Any(), "bridge_escrow").Return(nil).Times(1)
	bridge := "0x1111111111111111111111111111111111111111"
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{ChainId: "ethereum", Address: bridge})

	var trace bytes.Buffer
	ctx.MultiStore().SetTracer(&trace)
	gasBefore := ctx.GasMeter().GasConsumed()
	resp, err := ms.RequestBridgeMint(ctx, &types.MsgRequestBridgeMint{
		Creator:                  testutil.Creator,
		Amount:                   "100",
		DestinationAddress:       "0x3333333333333333333333333333333333333333",
		ChainId:                  "ethereum",
		DestinationBridgeAddress: bridge,
	})
	t.Logf("gas %d", ctx.GasMeter().GasConsumed()-gasBefore)
	ctx.MultiStore().SetTracer(nil)
	require.NoError(t, err)
	require.Equal(t, bridgeTestEpoch, resp.EpochIndex)
	require.Equal(t, 0, strings.Count(trace.String(), readOp))
}

func TestRequestBridgeWithdrawal_DoesNotReadRootGroupData(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	readOp := setupBridgeEpoch(t, k, ctx)

	creator := sdk.AccAddress([]byte("contract_addr______")).String()
	ms := keeper.NewMsgServerWithWasmKeeper(k, mockWasmKeeper{
		GetContractInfoFn: func(context.Context, sdk.AccAddress) *wasmtypes.ContractInfo {
			return &wasmtypes.ContractInfo{CodeID: 1, Creator: creator, Admin: creator, Label: "test"}
		},
	})
	require.NoError(t, k.SetWrappedTokenContract(ctx, types.BridgeWrappedTokenContract{
		ChainId:                "ethereum",
		ContractAddress:        "0x4444444444444444444444444444444444444444",
		WrappedContractAddress: creator,
	}))
	bridge := "0x2222222222222222222222222222222222222222"
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{ChainId: "ethereum", Address: bridge})

	var trace bytes.Buffer
	ctx.MultiStore().SetTracer(&trace)
	gasBefore := ctx.GasMeter().GasConsumed()
	resp, err := ms.RequestBridgeWithdrawal(ctx, &types.MsgRequestBridgeWithdrawal{
		Creator:                  creator,
		UserAddress:              testutil.Creator,
		Amount:                   "500",
		DestinationAddress:       "0x3333333333333333333333333333333333333333",
		DestinationBridgeAddress: bridge,
	})
	t.Logf("gas %d", ctx.GasMeter().GasConsumed()-gasBefore)
	ctx.MultiStore().SetTracer(nil)
	require.NoError(t, err)
	require.Equal(t, bridgeTestEpoch, resp.EpochIndex)
	require.Equal(t, 0, strings.Count(trace.String(), readOp))
}

func TestGetCurrentEpochIndexWithGroup_MissingGroup(t *testing.T) {
	k, _, ctx, _ := setupKeeperWithMocks(t)
	_, err := k.GetCurrentEpochIndexWithGroup(ctx)
	require.ErrorIs(t, err, types.ErrEffectiveEpochNotFound)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, bridgeTestEpoch))
	_, err = k.GetCurrentEpochIndexWithGroup(ctx)
	require.ErrorIs(t, err, types.ErrEpochGroupDataNotFound)
}
