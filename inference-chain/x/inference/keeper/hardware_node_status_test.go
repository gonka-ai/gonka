package keeper_test

import (
	"testing"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func statusTestNode(id string, status types.HardwareNodeStatus, port string) *types.HardwareNode {
	return &types.HardwareNode{
		LocalId:  id,
		Status:   status,
		Models:   []string{"model1"},
		Hardware: []*types.Hardware{{Type: "H100", Count: 8}},
		Host:     "10.0.0.1",
		Port:     port,
		Version:  "3.1.0",
	}
}

func TestSubmitHardwareDiff_StatusOnlyKeepsBlob(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	creator := NewMockAccount(testutil.Creator)
	MustAddParticipant(t, ms, ctx, *creator)
	registerTestModels(t, k, ms, ctx, "model1")
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	store := keeper.PrefixStore(sdkCtx, &k, []byte(keeper.HardwareNodesKeysPrefix))
	blob := func() []byte { return store.Get(keeper.HardwareNodesKey(testutil.Creator)) }

	_, err := ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{Creator: testutil.Creator, NewOrModified: []*types.HardwareNode{
		statusTestNode("a", types.HardwareNodeStatus_INFERENCE, "8080"),
		statusTestNode("b", types.HardwareNodeStatus_INFERENCE, "8081"),
	}})
	require.NoError(t, err)
	before := blob()

	// Status-only diff: the blob stays byte for byte, reads see the new status.
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{Creator: testutil.Creator, NewOrModified: []*types.HardwareNode{
		statusTestNode("b", types.HardwareNodeStatus_POC, "8081"),
	}})
	require.NoError(t, err)
	require.Equal(t, before, blob())

	want := &types.HardwareNodes{Participant: testutil.Creator, HardwareNodes: []*types.HardwareNode{
		statusTestNode("a", types.HardwareNodeStatus_INFERENCE, "8080"),
		statusTestNode("b", types.HardwareNodeStatus_POC, "8081"),
	}}
	got, found := k.GetHardwareNodes(ctx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, want, got)
	all, err := k.GetAllHardwareNodes(ctx)
	require.NoError(t, err)
	require.Equal(t, []*types.HardwareNodes{want}, all)
	some, err := k.GetHardwareNodesForParticipants(ctx, []string{testutil.Creator})
	require.NoError(t, err)
	require.Equal(t, []*types.HardwareNodes{want}, some)
	resp, err := k.HardwareNodes(ctx, &types.QueryHardwareNodesRequest{Participant: testutil.Creator})
	require.NoError(t, err)
	require.Equal(t, want, resp.Nodes)

	// The same status again is a no-op.
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{Creator: testutil.Creator, NewOrModified: []*types.HardwareNode{
		statusTestNode("b", types.HardwareNodeStatus_POC, "8081"),
	}})
	require.NoError(t, err)
	require.Equal(t, before, blob())

	// A non-status change rewrites the blob with current statuses and drops
	// the separate status record.
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{Creator: testutil.Creator, NewOrModified: []*types.HardwareNode{
		statusTestNode("a", types.HardwareNodeStatus_STOPPED, "9090"),
	}})
	require.NoError(t, err)
	statusStore := keeper.PrefixStore(sdkCtx, &k, []byte(keeper.HardwareNodeStatusKeysPrefix))
	require.False(t, statusStore.Has(keeper.HardwareNodesKey(testutil.Creator)))
	got, _ = k.GetHardwareNodes(ctx, testutil.Creator)
	require.Equal(t, &types.HardwareNodes{Participant: testutil.Creator, HardwareNodes: []*types.HardwareNode{
		statusTestNode("a", types.HardwareNodeStatus_STOPPED, "9090"),
		statusTestNode("b", types.HardwareNodeStatus_POC, "8081"),
	}}, got)

	// Removal is not status-only.
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{Creator: testutil.Creator, Removed: []*types.HardwareNode{{LocalId: "a"}}})
	require.NoError(t, err)
	got, _ = k.GetHardwareNodes(ctx, testutil.Creator)
	require.Equal(t, []*types.HardwareNode{statusTestNode("b", types.HardwareNodeStatus_POC, "8081")}, got.HardwareNodes)

	// Re-adding a removed node and a second participant's statuses stay separate.
	other := "gonka1other"
	require.NoError(t, k.SetHardwareNodes(ctx, &types.HardwareNodes{Participant: other, HardwareNodes: []*types.HardwareNode{
		statusTestNode("a", types.HardwareNodeStatus_INFERENCE, "7070"),
	}}))
	require.NoError(t, k.SetHardwareNodesStatuses(ctx, &types.HardwareNodes{Participant: other, HardwareNodes: []*types.HardwareNode{
		statusTestNode("a", types.HardwareNodeStatus_FAILED, "7070"),
	}}))
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{Creator: testutil.Creator, NewOrModified: []*types.HardwareNode{
		statusTestNode("a", types.HardwareNodeStatus_INFERENCE, "8080"),
	}})
	require.NoError(t, err)
	got, _ = k.GetHardwareNodes(ctx, testutil.Creator)
	require.Equal(t, []*types.HardwareNode{
		statusTestNode("a", types.HardwareNodeStatus_INFERENCE, "8080"),
		statusTestNode("b", types.HardwareNodeStatus_POC, "8081"),
	}, got.HardwareNodes)
	got, _ = k.GetHardwareNodes(ctx, other)
	require.Equal(t, []*types.HardwareNode{statusTestNode("a", types.HardwareNodeStatus_FAILED, "7070")}, got.HardwareNodes)
}
