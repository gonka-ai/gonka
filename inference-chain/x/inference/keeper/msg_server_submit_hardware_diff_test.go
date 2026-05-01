package keeper_test

import (
	"context"
	"testing"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func registerTestModels(t *testing.T, k keeper.Keeper, ms types.MsgServer, ctx context.Context, models ...string) {
	for _, model := range models {
		_, err := ms.RegisterModel(ctx, &types.MsgRegisterModel{
			Authority:           k.GetAuthority(),
			Id:                  model,
			ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2},
		})
		require.NoError(t, err)
	}
}

func TestMsgServer_SubmitHardwareDiff(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	mockCreator := NewMockAccount(testutil.Creator)
	// Create a participant
	MustAddParticipant(t, ms, ctx, *mockCreator)
	registerTestModels(t, k, ms, ctx, "model1", "model2", "model3", "model4")

	// Test adding new hardware nodes
	newNode1 := &types.HardwareNode{
		LocalId: "node1",
		Status:  types.HardwareNodeStatus_INFERENCE,
		Models:  []string{"model1", "model2"},
		Hardware: []*types.Hardware{
			{
				Type:  "GPU",
				Count: 2,
			},
		},
		Host: "localhost",
		Port: "8080",
	}

	newNode2 := &types.HardwareNode{
		LocalId: "node2",
		Status:  types.HardwareNodeStatus_TRAINING,
		Models:  []string{"model3"},
		Hardware: []*types.Hardware{
			{
				Type:  "CPU",
				Count: 8,
			},
		},
		Host: "localhost",
		Port: "8081",
	}

	// Submit new hardware nodes
	_, err := ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator:       testutil.Creator,
		NewOrModified: []*types.HardwareNode{newNode1, newNode2},
		Removed:       []*types.HardwareNode{},
	})
	require.NoError(t, err)

	// Verify that the hardware nodes were added
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	hardwareNodes, found := k.GetHardwareNodes(sdkCtx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, 2, len(hardwareNodes.HardwareNodes))
	require.Equal(t, "node1", hardwareNodes.HardwareNodes[0].LocalId)
	require.Equal(t, "node2", hardwareNodes.HardwareNodes[1].LocalId)

	// Test modifying an existing hardware node
	modifiedNode1 := &types.HardwareNode{
		LocalId: "node1",
		Status:  types.HardwareNodeStatus_POC,
		Models:  []string{"model1", "model2", "model4"},
		Hardware: []*types.Hardware{
			{
				Type:  "GPU",
				Count: 4,
			},
		},
		Host: "localhost",
		Port: "8080",
	}

	// Submit modified hardware node
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator:       testutil.Creator,
		NewOrModified: []*types.HardwareNode{modifiedNode1},
		Removed:       []*types.HardwareNode{},
	})
	require.NoError(t, err)

	// Verify that the hardware node was modified
	sdkCtx = sdk.UnwrapSDKContext(ctx)
	hardwareNodes, found = k.GetHardwareNodes(sdkCtx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, 2, len(hardwareNodes.HardwareNodes))
	require.Equal(t, "node1", hardwareNodes.HardwareNodes[0].LocalId)
	require.Equal(t, types.HardwareNodeStatus_POC, hardwareNodes.HardwareNodes[0].Status)
	require.Equal(t, 3, len(hardwareNodes.HardwareNodes[0].Models))
	require.Equal(t, uint32(4), hardwareNodes.HardwareNodes[0].Hardware[0].Count)

	// Test removing a hardware node
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator:       testutil.Creator,
		NewOrModified: []*types.HardwareNode{},
		Removed:       []*types.HardwareNode{newNode2},
	})
	require.NoError(t, err)

	// Verify that the hardware node was removed
	sdkCtx = sdk.UnwrapSDKContext(ctx)
	hardwareNodes, found = k.GetHardwareNodes(sdkCtx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, 1, len(hardwareNodes.HardwareNodes))
	require.Equal(t, "node1", hardwareNodes.HardwareNodes[0].LocalId)
}

func TestMsgServer_SubmitHardwareDiff_NoExistingNodes(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	mockCreator := NewMockAccount(testutil.Creator)
	// Create a participant
	MustAddParticipant(t, ms, ctx, *mockCreator)

	// Test adding new hardware nodes when no existing nodes
	newNode := &types.HardwareNode{
		LocalId: "node1",
		Status:  types.HardwareNodeStatus_INFERENCE,
		Models:  []string{"model1", "model2"},
		Hardware: []*types.Hardware{
			{
				Type:  "GPU",
				Count: 2,
			},
		},
		Host: "localhost",
		Port: "8080",
	}

	registerTestModels(t, k, ms, sdk.UnwrapSDKContext(ctx), "model1", "model2")

	// Submit new hardware node
	_, err := ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator:       testutil.Creator,
		NewOrModified: []*types.HardwareNode{newNode},
		Removed:       []*types.HardwareNode{},
	})
	require.NoError(t, err)

	// Verify that the hardware node was added
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	hardwareNodes, found := k.GetHardwareNodes(sdkCtx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, 1, len(hardwareNodes.HardwareNodes))
	require.Equal(t, "node1", hardwareNodes.HardwareNodes[0].LocalId)
}

func TestMsgServer_SubmitHardwareDiff_RemoveAll(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	mockCreator := NewMockAccount(testutil.Creator)
	// Create a participant
	MustAddParticipant(t, ms, ctx, *mockCreator)

	// Add a hardware node
	newNode := &types.HardwareNode{
		LocalId: "node1",
		Status:  types.HardwareNodeStatus_INFERENCE,
		Models:  []string{"model1", "model2"},
		Hardware: []*types.Hardware{
			{
				Type:  "GPU",
				Count: 2,
			},
		},
		Host: "localhost",
		Port: "8080",
	}

	registerTestModels(t, k, ms, sdk.UnwrapSDKContext(ctx), "model1", "model2")

	// Submit new hardware node
	_, err := ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator:       testutil.Creator,
		NewOrModified: []*types.HardwareNode{newNode},
		Removed:       []*types.HardwareNode{},
	})
	require.NoError(t, err)

	// Verify that the hardware node was added
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	hardwareNodes, found := k.GetHardwareNodes(sdkCtx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, 1, len(hardwareNodes.HardwareNodes))

	// Remove all hardware nodes
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator:       testutil.Creator,
		NewOrModified: []*types.HardwareNode{},
		Removed:       []*types.HardwareNode{newNode},
	})
	require.NoError(t, err)

	// Verify that all hardware nodes were removed
	sdkCtx = sdk.UnwrapSDKContext(ctx)
	hardwareNodes, found = k.GetHardwareNodes(sdkCtx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, 0, len(hardwareNodes.HardwareNodes))
}

// TestHardwareNodesUnchanged is a focused unit test for the helper that
// gates the SubmitHardwareDiff no-op skip. Compares operational fields
// only — see hardwareNodeOperationalEqual — so flapping informational
// fields like Version don't trigger spurious writes.
func TestHardwareNodesUnchanged(t *testing.T) {
	n1 := &types.HardwareNode{LocalId: "n1", Status: types.HardwareNodeStatus_INFERENCE}
	n2 := &types.HardwareNode{LocalId: "n2", Status: types.HardwareNodeStatus_POC,
		Models: []string{"model1"}, Host: "h", Port: "8080",
		Hardware: []*types.Hardware{{Type: "GPU", Count: 2}}}

	// Both empty: equal.
	require.True(t, keeper.HardwareNodesUnchanged(nil, nil))
	require.True(t, keeper.HardwareNodesUnchanged([]*types.HardwareNode{}, []*types.HardwareNode{}))

	// Identical content, identical order.
	require.True(t, keeper.HardwareNodesUnchanged(
		[]*types.HardwareNode{n1, n2}, []*types.HardwareNode{n1, n2}))

	// Different lengths.
	require.False(t, keeper.HardwareNodesUnchanged(
		[]*types.HardwareNode{n1}, []*types.HardwareNode{n1, n2}))

	// Operational field differs: detected.
	n2DiffStatus := &types.HardwareNode{LocalId: "n2", Status: types.HardwareNodeStatus_INFERENCE,
		Models: n2.Models, Host: n2.Host, Port: n2.Port, Hardware: n2.Hardware}
	require.False(t, keeper.HardwareNodesUnchanged(
		[]*types.HardwareNode{n1, n2DiffStatus}, []*types.HardwareNode{n1, n2}))

	// Models reordered: detected (slices.Equal is positional).
	n2DiffModels := &types.HardwareNode{LocalId: "n2", Status: n2.Status,
		Models: []string{"model2"}, Host: n2.Host, Port: n2.Port, Hardware: n2.Hardware}
	require.False(t, keeper.HardwareNodesUnchanged(
		[]*types.HardwareNode{n1, n2DiffModels}, []*types.HardwareNode{n1, n2}))

	// Hardware count differs: detected.
	n2DiffHW := &types.HardwareNode{LocalId: "n2", Status: n2.Status,
		Models: n2.Models, Host: n2.Host, Port: n2.Port,
		Hardware: []*types.Hardware{{Type: "GPU", Count: 4}}}
	require.False(t, keeper.HardwareNodesUnchanged(
		[]*types.HardwareNode{n1, n2DiffHW}, []*types.HardwareNode{n1, n2}))

	// Version differs but everything else identical: NOT detected (the
	// version field is informational only — flipping it shouldn't defeat
	// the no-op check).
	n2VerA := &types.HardwareNode{LocalId: "n2", Status: n2.Status,
		Models: n2.Models, Host: n2.Host, Port: n2.Port, Hardware: n2.Hardware,
		Version: "v1"}
	n2VerB := &types.HardwareNode{LocalId: "n2", Status: n2.Status,
		Models: n2.Models, Host: n2.Host, Port: n2.Port, Hardware: n2.Hardware,
		Version: "v2"}
	require.True(t, keeper.HardwareNodesUnchanged(
		[]*types.HardwareNode{n1, n2VerA}, []*types.HardwareNode{n1, n2VerB}),
		"flapping the informational Version field should NOT count as a change")
}

// TestMsgServer_SubmitHardwareDiff_IdempotentOnNoChange exercises the
// happy path of the no-op skip end-to-end: a participant submits a diff
// once, then submits an identical (no-op) diff. Both calls succeed and
// the stored state matches the original write.
//
// We can't directly assert "SetHardwareNodes was not called the second
// time" without a keeper spy, so this test pairs with TestHardwareNodesUnchanged
// (unit-level) for full coverage.
func TestMsgServer_SubmitHardwareDiff_IdempotentOnNoChange(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	mockCreator := NewMockAccount(testutil.Creator)
	MustAddParticipant(t, ms, ctx, *mockCreator)
	registerTestModels(t, k, ms, ctx, "model1")

	node := &types.HardwareNode{
		LocalId:  "node1",
		Status:   types.HardwareNodeStatus_INFERENCE,
		Models:   []string{"model1"},
		Hardware: []*types.Hardware{{Type: "GPU", Count: 1}},
		Host:     "localhost",
		Port:     "8080",
	}

	// First submit: writes the node.
	_, err := ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator:       testutil.Creator,
		NewOrModified: []*types.HardwareNode{node},
	})
	require.NoError(t, err)

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	got, found := k.GetHardwareNodes(sdkCtx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, 1, len(got.HardwareNodes))

	// Second submit with the identical node: no-op path. Must still succeed
	// and leave state untouched.
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator:       testutil.Creator,
		NewOrModified: []*types.HardwareNode{node},
	})
	require.NoError(t, err)

	gotAgain, found := k.GetHardwareNodes(sdkCtx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, 1, len(gotAgain.HardwareNodes))
	require.Equal(t, got.HardwareNodes[0].LocalId, gotAgain.HardwareNodes[0].LocalId)
	require.Equal(t, got.HardwareNodes[0].Status, gotAgain.HardwareNodes[0].Status)

	// Empty diff (no add, no remove) is also a no-op.
	_, err = ms.SubmitHardwareDiff(ctx, &types.MsgSubmitHardwareDiff{
		Creator:       testutil.Creator,
		NewOrModified: []*types.HardwareNode{},
		Removed:       []*types.HardwareNode{},
	})
	require.NoError(t, err)

	stillThere, found := k.GetHardwareNodes(sdkCtx, testutil.Creator)
	require.True(t, found)
	require.Equal(t, 1, len(stillThere.HardwareNodes))
}
