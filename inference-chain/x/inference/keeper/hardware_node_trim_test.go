package keeper_test

import (
	"fmt"
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func trimTestNodes(n int, status types.HardwareNodeStatus) []*types.HardwareNode {
	nodes := make([]*types.HardwareNode, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, &types.HardwareNode{
			LocalId:  fmt.Sprintf("node%d", i),
			Status:   status,
			Models:   []string{"model1"},
			Hardware: []*types.Hardware{{Type: "NVIDIA H100 80GB HBM3", Count: 8}},
			Host:     "10.0.0.1",
			Port:     "8080",
			Version:  "v3.0.12",
		})
	}
	return nodes
}

func rawHardwareNodes(t *testing.T, k keeper.Keeper, ctx sdk.Context, participant string) *types.HardwareNodes {
	bz := keeper.PrefixStore(ctx, &k, []byte(keeper.HardwareNodesKeysPrefix)).Get(keeper.HardwareNodesKey(participant))
	require.NotNil(t, bz)
	raw := &types.HardwareNodes{}
	require.NoError(t, raw.Unmarshal(bz))
	return raw
}

func TestHardwareNodes_ValueOmitsParticipant(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	in := &types.HardwareNodes{Participant: testutil.Creator, HardwareNodes: trimTestNodes(6, types.HardwareNodeStatus_INFERENCE)}

	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.SetHardwareNodes(gctx, in))
	t.Logf("SetHardwareNodes gas, 6 nodes: %d", gctx.GasMeter().GasConsumed())
	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, found := k.GetHardwareNodes(gctx, testutil.Creator)
	require.True(t, found)
	t.Logf("GetHardwareNodes gas, 6 nodes: %d", gctx.GasMeter().GasConsumed())

	require.Empty(t, rawHardwareNodes(t, k, ctx, testutil.Creator).Participant)
	require.Equal(t, testutil.Creator, in.Participant, "caller's value is not mutated")

	got, found := k.GetHardwareNodes(ctx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, in, got)
	all, err := k.GetAllHardwareNodes(ctx)
	require.NoError(t, err)
	require.Equal(t, []*types.HardwareNodes{in}, all)
	forP, err := k.GetHardwareNodesForParticipants(ctx, []string{testutil.Creator})
	require.NoError(t, err)
	require.Equal(t, []*types.HardwareNodes{in}, forP)
}

func TestHardwareNodes_EmptyListStoredWhole(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	in := &types.HardwareNodes{Participant: testutil.Creator, HardwareNodes: []*types.HardwareNode{}}
	require.NoError(t, k.SetHardwareNodes(ctx, in))
	require.Equal(t, testutil.Creator, rawHardwareNodes(t, k, ctx, testutil.Creator).Participant)
	got, found := k.GetHardwareNodes(ctx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, testutil.Creator, got.Participant)
	require.Empty(t, got.HardwareNodes)
}

func TestHardwareNodes_LegacyFullRecord(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	legacy := &types.HardwareNodes{Participant: testutil.Creator, HardwareNodes: trimTestNodes(2, types.HardwareNodeStatus_INFERENCE)}
	bz, err := legacy.Marshal()
	require.NoError(t, err)
	keeper.PrefixStore(ctx, &k, []byte(keeper.HardwareNodesKeysPrefix)).Set(keeper.HardwareNodesKey(testutil.Creator), bz)

	got, found := k.GetHardwareNodes(ctx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, legacy, got)
	all, err := k.GetAllHardwareNodes(ctx)
	require.NoError(t, err)
	require.Equal(t, []*types.HardwareNodes{legacy}, all)
}

func TestHardwareNodes_DiffGas(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	MustAddParticipant(t, ms, ctx, *NewMockAccount(testutil.Creator))
	registerTestModels(t, k, ms, ctx, "model1")

	_, err := ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{Creator: testutil.Creator, NewOrModified: trimTestNodes(6, types.HardwareNodeStatus_INFERENCE)})
	require.NoError(t, err)

	flip := trimTestNodes(6, types.HardwareNodeStatus_INFERENCE)[:1]
	flip[0].Status = types.HardwareNodeStatus_POC
	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	_, err = ms.SubmitHardwareDiff(gctx, &types.MsgSubmitHardwareDiff{Creator: testutil.Creator, NewOrModified: flip})
	require.NoError(t, err)
	t.Logf("SubmitHardwareDiff gas, one node of 6 changes status: %d", gctx.GasMeter().GasConsumed())

	got, found := k.GetHardwareNodes(ctx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, testutil.Creator, got.Participant)
	require.Equal(t, types.HardwareNodeStatus_POC, got.HardwareNodes[0].Status)
}

func TestHardwareNodes_UpperCaseParticipantReadsBack(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	upper := strings.ToUpper(testutil.Creator)
	in := &types.HardwareNodes{Participant: upper, HardwareNodes: trimTestNodes(1, types.HardwareNodeStatus_INFERENCE)}
	require.NoError(t, k.SetHardwareNodes(ctx, in))
	got, found := k.GetHardwareNodes(ctx, upper)
	require.True(t, found)
	require.Equal(t, in, got)
	all, err := k.GetAllHardwareNodes(ctx)
	require.NoError(t, err)
	require.Equal(t, []*types.HardwareNodes{in}, all)
}
