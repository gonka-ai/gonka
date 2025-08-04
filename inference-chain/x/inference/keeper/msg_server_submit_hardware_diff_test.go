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
