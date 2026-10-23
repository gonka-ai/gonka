package v0_2_17

import (
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestBackfillTrainingParamDefaults(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.TrainingParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillTrainingParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.TrainingParams)
	require.Equal(t, inferencetypes.DefaultTrainingMaxDurationBlocks, got.TrainingParams.MaxDurationBlocks)
	require.Equal(t, inferencetypes.DefaultTrainingOptInTtlBlocks, got.TrainingParams.OptInTtlBlocks)
	require.Equal(t, inferencetypes.DefaultTrainingReleaseBufferBlocks, got.TrainingParams.ReleaseBufferBlocks)
}

func TestBackfillTrainingParamDefaults_FillsTheNodeWeightCapFromOldParams(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.TrainingParams.NodeWeightToleranceBps = 0
	params.TrainingParams.MinWeightRateParticipants = 0
	bz, err := params.TrainingParams.Marshal()
	require.NoError(t, err)
	var decoded inferencetypes.TrainingParams
	require.NoError(t, decoded.Unmarshal(bz))
	require.Zero(t, decoded.NodeWeightToleranceBps)
	require.Zero(t, decoded.MinWeightRateParticipants)
	params.TrainingParams = &decoded
	require.Error(t, params.Validate())
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillTrainingParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.DefaultTrainingNodeWeightTolerance, got.TrainingParams.NodeWeightToleranceBps)
	require.Equal(t, inferencetypes.DefaultTrainingMinRateParticipants, got.TrainingParams.MinWeightRateParticipants)
	require.NoError(t, got.Validate())
}

func TestBackfillTrainingParamDefaults_RaisesRetentionButKeepsTheOptInShort(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.EpochLength = 2000
	params.TrainingParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillTrainingParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.DefaultTrainingOptInTtlBlocks, got.TrainingParams.OptInTtlBlocks)
	require.Equal(t, 2*int64(2000)+got.TrainingParams.ReleaseBufferBlocks, got.TrainingParams.SettledShardRetentionBlocks)

	got.TrainingParams.TrainingEnabled = true
	require.NoError(t, got.Validate())
}

func TestBackfillTrainingParamDefaults_PreservesOverrides(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.TrainingParams.OptInTtlBlocks = 777
	params.TrainingParams.ReleaseBufferBlocks = 0
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillTrainingParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(777), got.TrainingParams.OptInTtlBlocks)
	require.Equal(t, inferencetypes.DefaultTrainingReleaseBufferBlocks, got.TrainingParams.ReleaseBufferBlocks)
}

func TestBackfillTrainshardNodeStatuses(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, k.Trainshards.Set(ctx, 1, inferencetypes.Trainshard{
		TrainshardId: 1,
		Status:       inferencetypes.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		Nodes:        []*inferencetypes.TrainshardReservedNode{{Participant: "host", NodeId: "node-a"}},
	}))
	require.NoError(t, k.Trainshards.Set(ctx, 2, inferencetypes.Trainshard{
		TrainshardId:   2,
		Status:         inferencetypes.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		ClosedAtHeight: 150,
		Nodes:          []*inferencetypes.TrainshardReservedNode{{Participant: "host", NodeId: "node-b"}},
	}))

	require.NoError(t, backfillTrainshardNodeStatuses(ctx, k))

	active, err := k.Trainshards.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE, active.Nodes[0].Status)

	closed, err := k.Trainshards.Get(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE, closed.Nodes[0].Status)
	require.Equal(t, int64(150), closed.Nodes[0].ReleasedAtHeight)
	require.Equal(t, int64(150), closed.Nodes[0].ReservedUntilHeight)
}

func TestBackfillTrainshardWeightEpochs(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	for epochIndex, effectiveAt := range map[uint64]int64{3: 100, 4: 200, 5: 300} {
		k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{EpochIndex: epochIndex, EffectiveBlockHeight: effectiveAt})
	}
	createdAt := map[uint64]int64{1: 250, 2: 200, 3: 301, 4: 50, 5: 250}
	for id, height := range createdAt {
		require.NoError(t, k.Trainshards.Set(ctx, id, inferencetypes.Trainshard{TrainshardId: id, CreatedAtHeight: height}))
	}
	require.NoError(t, k.TrainshardWeightEpoch.Set(ctx, 5, 3))

	require.NoError(t, backfillTrainshardWeightEpochs(ctx, k))

	for id, want := range map[uint64]uint64{1: 4, 2: 3, 3: 5, 5: 3} {
		got, err := k.TrainshardWeightEpoch.Get(ctx, id)
		require.NoError(t, err)
		require.Equal(t, want, got, "trainshard %d", id)
	}
	unresolved, err := k.TrainshardWeightEpoch.Has(ctx, 4)
	require.NoError(t, err)
	require.False(t, unresolved)
}

func TestBackfillTrainshardWeightEpochs_NeedsNoEpochWithoutShards(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, backfillTrainshardWeightEpochs(ctx, k))
}

func TestRepairTrainshardReservations(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	node := func(id string, status inferencetypes.TrainshardNodeStatus, until int64) *inferencetypes.TrainshardReservedNode {
		return &inferencetypes.TrainshardReservedNode{Participant: "host", NodeId: id, ModelId: "model1", Status: status, ReservedUntilHeight: until}
	}
	require.NoError(t, k.Trainshards.Set(ctx, 1, inferencetypes.Trainshard{
		TrainshardId: 1,
		Status:       inferencetypes.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		Nodes: []*inferencetypes.TrainshardReservedNode{
			node("leased", inferencetypes.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE, 0),
			node("kicked", inferencetypes.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_AUTOKICKED, 120),
		},
	}))
	require.NoError(t, k.Trainshards.Set(ctx, 2, inferencetypes.Trainshard{
		TrainshardId: 2,
		Status:       inferencetypes.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		Nodes:        []*inferencetypes.TrainshardReservedNode{node("returning", inferencetypes.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE, 300)},
	}))
	for nodeId, shardId := range map[string]uint64{"leased": 1, "kicked": 1, "returning": 2, "deferred": 2, "foreign": 2, "orphan": 3} {
		require.NoError(t, k.TrainshardReservations.Set(ctx, collections.Join("host", nodeId), shardId))
	}
	shard2, err := k.Trainshards.Get(ctx, 2)
	require.NoError(t, err)
	shard2.Nodes = append(shard2.Nodes, node("deferred", inferencetypes.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE, 300))
	require.NoError(t, k.Trainshards.Set(ctx, 2, shard2))
	require.NoError(t, k.TrainshardReleaseIndex.Set(ctx, collections.Join3(int64(301), "host", "deferred")))
	require.NoError(t, k.TrainshardReleaseIndex.Set(ctx, collections.Join3(int64(250), "host", "leased")))

	require.NoError(t, repairTrainshardReservations(ctx, k))

	for nodeId, kept := range map[string]bool{"leased": true, "kicked": true, "returning": true, "deferred": true, "foreign": false, "orphan": false} {
		has, err := k.TrainshardReservations.Has(ctx, collections.Join("host", nodeId))
		require.NoError(t, err)
		require.Equal(t, kept, has, nodeId)
	}
	var indexed []collections.Triple[int64, string, string]
	require.NoError(t, k.TrainshardReleaseIndex.Walk(ctx, nil, func(key collections.Triple[int64, string, string]) (bool, error) {
		indexed = append(indexed, key)
		return false, nil
	}))
	require.Equal(t, []collections.Triple[int64, string, string]{
		collections.Join3(int64(120), "host", "kicked"),
		collections.Join3(int64(300), "host", "returning"),
		collections.Join3(int64(301), "host", "deferred"),
	}, indexed)

	require.NoError(t, repairTrainshardReservations(ctx, k))
	indexedAgain := 0
	require.NoError(t, k.TrainshardReleaseIndex.Walk(ctx, nil, func(collections.Triple[int64, string, string]) (bool, error) {
		indexedAgain++
		return false, nil
	}))
	require.Equal(t, len(indexed), indexedAgain)
}

func TestClearTrainshardAutokickRequests(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	byName, ok := ctx.MultiStore().(interface{ GetStoreByName(string) storetypes.Store })
	require.True(t, ok)
	store, ok := byName.GetStoreByName(inferencetypes.StoreKey).(storetypes.KVStore)
	require.True(t, ok)

	codec := collections.PairKeyCodec(collections.Uint64Key, collections.StringKey)
	var requests [][]byte
	for _, key := range []collections.Pair[uint64, string]{collections.Join(uint64(7), "req-1"), collections.Join(uint64(9), "req-2")} {
		raw, err := collections.EncodeKeyWithPrefix(retiredTrainshardAutokickRequestPrefix, codec, key)
		require.NoError(t, err)
		store.Set(raw, []byte("host/node-a"))
		requests = append(requests, raw)
	}
	require.NoError(t, k.TrainshardWeightEpoch.Set(ctx, 7, 3))

	require.NoError(t, clearTrainshardAutokickRequests(ctx, k))

	for _, raw := range requests {
		require.False(t, store.Has(raw))
	}
	weightEpoch, err := k.TrainshardWeightEpoch.Get(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, uint64(3), weightEpoch)
}

func TestClearStaleTrainingOptIns(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join("host", "node-a"), int64(0)))
	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join("host", "node-b"), int64(500)))

	require.NoError(t, clearStaleTrainingOptIns(ctx, k))

	has, err := k.TrainingNodeOptIns.Has(ctx, collections.Join("host", "node-a"))
	require.NoError(t, err)
	require.False(t, has)

	expiresAt, err := k.TrainingNodeOptIns.Get(ctx, collections.Join("host", "node-b"))
	require.NoError(t, err)
	require.Equal(t, int64(500), expiresAt)
}

func TestClearStaleTrainingOptIns_DropsNodeIdsTheDaemonCannotAddress(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	for _, nodeId := range []string{"node/a", "..", "node-b"} {
		key := collections.Join("host", nodeId)
		require.NoError(t, k.TrainingNodeOptIns.Set(ctx, key, int64(500)))
		require.NoError(t, k.TrainingNodeEndpoints.Set(ctx, key, "https://a.example.com"))
	}
	require.NoError(t, k.TrainingNodeEndpoints.Set(ctx, collections.Join("host", "node c"), "https://c.example.com"))

	require.NoError(t, clearStaleTrainingOptIns(ctx, k))

	for _, nodeId := range []string{"node/a", ".."} {
		has, err := k.TrainingNodeOptIns.Has(ctx, collections.Join("host", nodeId))
		require.NoError(t, err)
		require.False(t, has, nodeId)
	}
	for _, nodeId := range []string{"node/a", "..", "node c"} {
		has, err := k.TrainingNodeEndpoints.Has(ctx, collections.Join("host", nodeId))
		require.NoError(t, err)
		require.False(t, has, nodeId)
	}
	expiresAt, err := k.TrainingNodeOptIns.Get(ctx, collections.Join("host", "node-b"))
	require.NoError(t, err)
	require.Equal(t, int64(500), expiresAt)
	endpoint, err := k.TrainingNodeEndpoints.Get(ctx, collections.Join("host", "node-b"))
	require.NoError(t, err)
	require.Equal(t, "https://a.example.com", endpoint)
}
