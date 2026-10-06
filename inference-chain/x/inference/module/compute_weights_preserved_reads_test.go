package inference

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
)

// A participant serving inference on a preserved node while mining PoC on another
// has its participant record and seed read once per ComputeNewWeights.
func TestComputeNewWeightsReadsPreservedCommitterOnce(t *testing.T) {
	k, ctx := newMinimalInferenceKeeper(t)
	am := NewAppModule(nil, k, nil, nil, nil, nil)

	currentEpoch := types.Epoch{Index: 1, PocStartBlockHeight: 50}
	require.NoError(t, k.SetEpoch(ctx, &currentEpoch))
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch.Index))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex:          currentEpoch.Index,
		PocStartBlockHeight: uint64(currentEpoch.PocStartBlockHeight),
		ValidationWeights:   []*types.ValidationWeight{{MemberAddress: testutil.Executor, Weight: 30}},
		SubGroupModels:      []string{"model-a"},
	})
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: currentEpoch.Index,
		ModelId:    "model-a",
		ValidationWeights: []*types.ValidationWeight{{
			MemberAddress: testutil.Executor,
			Weight:        30,
			MlNodes: []*types.MLNodeInfo{
				{NodeId: "node-1", PocWeight: 10, TimeslotAllocation: []bool{true, false}},
				{NodeId: "node-2", PocWeight: 20, TimeslotAllocation: []bool{true, false}},
			},
		}},
	})
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        testutil.Executor,
		Address:      testutil.Executor,
		ValidatorKey: "validator-key",
		InferenceUrl: "http://executor",
		Status:       types.ParticipantStatus_ACTIVE,
	}))
	require.NoError(t, k.SetRandomSeed(ctx, types.RandomSeed{Participant: testutil.Executor, EpochIndex: 2, Signature: "seed-signature"}))
	require.NoError(t, k.SetPreservedNodesSnapshot(ctx, types.PreservedNodesSnapshot{
		EpisodeAnchorHeight: 100,
		ModelPreservedNodes: []*types.ModelPreservedNodes{{
			ModelId:      "model-a",
			Participants: []*types.ParticipantPreservedNodes{{ParticipantId: testutil.Executor, NodeIds: []string{"node-1"}}},
		}},
	}))
	require.NoError(t, k.SetPoCV2StoreCommit(ctx, types.PoCV2StoreCommit{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 100,
		Count:                    10,
		RootHash:                 make([]byte, 32),
		CommitBlockHeight:        100,
		ModelId:                  "model-a",
	}))
	require.NoError(t, k.SetMLNodeWeightDistribution(ctx, types.MLNodeWeightDistribution{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 100,
		ModelId:                  "model-a",
		Weights:                  []*types.MLNodeWeight{{NodeId: "node-2", Weight: 20}},
	}))

	prefixes := map[string][]byte{
		"participant": k.Participants.GetPrefix(),
		"seed":        k.RandomSeeds.GetPrefix(),
	}
	var trace bytes.Buffer
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	ctx.MultiStore().SetTracer(&trace)
	computed := am.computeNewWeights(ctx, types.Epoch{Index: 2, PocStartBlockHeight: 100})
	ctx.MultiStore().SetTracer(nil)

	reads := map[string]int{}
	for _, line := range strings.Split(trace.String(), "\n") {
		var op struct {
			Operation string `json:"operation"`
			Key       string `json:"key"`
		}
		if line == "" || json.Unmarshal([]byte(line), &op) != nil || op.Operation != "read" {
			continue
		}
		key, err := base64.StdEncoding.DecodeString(op.Key)
		require.NoError(t, err)
		for name, p := range prefixes {
			if bytes.HasPrefix(key, p) {
				reads[name]++
			}
		}
	}
	t.Logf("reads=%v gas=%d", reads, ctx.GasMeter().GasConsumed())
	require.Len(t, computed.participants, 1)
	require.NotNil(t, computed.participants[0].Seed)
	require.Equal(t, 1, reads["participant"])
	require.Equal(t, 1, reads["seed"])
}
