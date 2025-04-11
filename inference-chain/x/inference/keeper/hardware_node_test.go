package keeper_test

import (
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"reflect"
	"testing"
)

func TestSetAndGetHardwareNodes(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	_ = ctx
	participantId := "cosmosparticipant1"
	nodes := &types.HardwareNodes{
		Participant: participantId,
		HardwareNodes: []*types.HardwareNode{
			{
				LocalId: "localId1",
				Hardware: []*types.Hardware{
					{
						Type:  "A100",
						Count: 10,
					},
				},
			},
			{
				LocalId: "localId2",
				Hardware: []*types.Hardware{
					{
						Type:  "A100",
						Count: 2,
					},
					{
						Type:  "V100",
						Count: 1,
					},
				},
			},
		},
	}
	err := keeper.SetHardwareNodes(ctx, nodes)
	if err != nil {
		t.Fatal("Failed to set hardware nodes", err)
	}

	retrievedNodes, found := keeper.GetHardwareNodes(ctx, participantId)
	if !found {
		t.Fatal("Failed to get hardware nodes")
	}

	if !reflect.DeepEqual(nodes, retrievedNodes) {
		t.Errorf("Mismatch:\nexpected: %#v\ngot: %#v", nodes, retrievedNodes)
	}

	nodesForParticipants, err := keeper.GetHardwareNodesForParticipants(ctx, []string{participantId})
	if err != nil {
		t.Fatal("Failed to get hardware nodes for participants", err)
	}

	if !reflect.DeepEqual(nodesForParticipants, []*types.HardwareNodes{retrievedNodes}) {
		t.Errorf("Mismatch:\nexpected: %#v\ngot: %#v", nodes, retrievedNodes)
	}

	allNodes, err := keeper.GetAllHardwareNodes(ctx)
	if err != nil {
		t.Fatal("Failed to get all hardware nodes", err)
	}

	if !reflect.DeepEqual(allNodes, nodesForParticipants) {
		t.Errorf("Mismatch:\nexpected: %#v\ngot: %#v", []*types.HardwareNodes{nodes}, allNodes)
	}
}
