package v0_2_17

import (
	"testing"

	"cosmossdk.io/collections"
	"cosmossdk.io/store/prefix"
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	inferencekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.17", UpgradeName)
}

// TestMigrateLegacyPruningLimits covers the backfill that moved here from the
// v0.2.16 handler once v0.2.16 had executed. Without it a chain carrying the
// original all-zero defaults keeps every pruner a no-op.
func TestMigrateLegacyPruningLimits(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.InferencePruningMax = 0
	params.EpochParams.PocPruningMax = -1
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migrateLegacyPruningLimits(ctx, k))
	migrated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.DefaultEpochParams().InferencePruningMax, migrated.EpochParams.InferencePruningMax)
	require.Equal(t, inferencetypes.DefaultEpochParams().PocPruningMax, migrated.EpochParams.PocPruningMax)
	require.NoError(t, migrated.Validate())

	// The migration is idempotent and preserves already-positive limits.
	migrated.EpochParams.InferencePruningMax = 123
	migrated.EpochParams.PocPruningMax = 456
	require.NoError(t, k.SetParams(ctx, migrated))
	require.NoError(t, migrateLegacyPruningLimits(ctx, k))
	again, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(123), again.EpochParams.InferencePruningMax)
	require.Equal(t, int64(456), again.EpochParams.PocPruningMax)
}

// TestCleanupLeftoverState covers the leftover-state cleanup moved here from
// the v0.2.16 handler (#1287) now that v0.2.16 has executed. It is idempotent:
// the test runs it twice.
func TestCleanupLeftoverState(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	store := inferencekeeper.EmptyPrefixStore(ctx, &k)

	currentEpoch := uint64(2)
	previousEpoch := uint64(1)
	oldEpoch := uint64(0)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, currentEpoch))

	currentValidation := inferencetypes.EpochGroupValidations{
		Participant:         "current-participant",
		EpochIndex:          currentEpoch,
		ValidatedInferences: []string{"current-inf-1", "current-inf-2"},
	}
	previousValidation := inferencetypes.EpochGroupValidations{
		Participant:         "previous-participant",
		EpochIndex:          previousEpoch,
		ValidatedInferences: []string{"previous-inf-1"},
	}
	oldValidation := inferencetypes.EpochGroupValidations{
		Participant:         "old-participant",
		EpochIndex:          oldEpoch,
		ValidatedInferences: []string{"old-inf-1"},
	}
	require.NoError(t, k.EpochGroupValidationsMap.Set(ctx, collections.Join(currentEpoch, currentValidation.Participant), currentValidation))
	require.NoError(t, k.EpochGroupValidationsMap.Set(ctx, collections.Join(previousEpoch, previousValidation.Participant), previousValidation))
	require.NoError(t, k.EpochGroupValidationsMap.Set(ctx, collections.Join(oldEpoch, oldValidation.Participant), oldValidation))

	topMinerAddr := sdk.MustAccAddressFromBech32(sample.AccAddress())
	require.NoError(t, k.TopMiners.Set(ctx, topMinerAddr, inferencetypes.TopMiner{Address: topMinerAddr.String()}))

	execAddr := sdk.MustAccAddressFromBech32(sample.AccAddress())
	startAddr := sdk.MustAccAddressFromBech32(sample.AccAddress())
	require.NoError(t, k.TrainingExecAllowListSet.Set(ctx, execAddr))
	require.NoError(t, k.TrainingStartAllowListSet.Set(ctx, startAddr))

	trainingKeys := [][]byte{
		[]byte(inferencetypes.TrainingTaskKeyPrefix + "1"),
		[]byte(inferencetypes.TrainingTaskSequenceKey),
		[]byte(inferencetypes.QueuedTrainingTaskKeyPrefix + "1"),
		[]byte(inferencetypes.InProgressTrainingTaskKeyPrefix + "1"),
		[]byte(inferencetypes.TrainingTaskKvRecordKeyPrefix + "1/key"),
		[]byte("TrainingTask/sync/1/store/key/value"),
		[]byte("TrainingTask/sync/1/heartbeat/0/participant/node"),
		[]byte("TrainingTask/sync/1/barrier/b1/0/participant/node/value"),
	}
	for _, key := range trainingKeys {
		store.Set(key, []byte("training"))
	}

	legacyPoCPrefixes := [][]byte{
		inferencetypes.LegacyPoCValidationV2Prefix,
		inferencetypes.LegacyPoCV2StoreCommitPrefix,
		inferencetypes.LegacyMLNodeWeightDistributionPrefix,
	}
	for i, pfx := range legacyPoCPrefixes {
		key := append(append([]byte{}, pfx...), byte(i), byte(i+1))
		store.Set(key, []byte("legacy-poc"))
		require.Equal(t, 1, countPrefixEntries(t, store, pfx))
	}

	require.NoError(t, k.SetPocV2EnabledEpoch(ctx, 123))

	require.NoError(t, cleanupLeftoverState(ctx, k))
	require.NoError(t, cleanupLeftoverState(ctx, k))

	migratedCurrent, found := k.GetEpochGroupValidations(ctx, currentValidation.Participant, currentEpoch)
	require.True(t, found)
	require.ElementsMatch(t, currentValidation.ValidatedInferences, migratedCurrent.ValidatedInferences)

	migratedPrevious, found := k.GetEpochGroupValidations(ctx, previousValidation.Participant, previousEpoch)
	require.True(t, found)
	require.ElementsMatch(t, previousValidation.ValidatedInferences, migratedPrevious.ValidatedInferences)

	_, found = k.GetEpochGroupValidations(ctx, oldValidation.Participant, oldEpoch)
	require.False(t, found)

	legacyIter, err := k.EpochGroupValidationsMap.Iterate(ctx, nil)
	require.NoError(t, err)
	legacyValues, err := legacyIter.Values()
	require.NoError(t, err)
	require.Empty(t, legacyValues)

	hasTopMiner, err := k.TopMiners.Has(ctx, topMinerAddr)
	require.NoError(t, err)
	require.False(t, hasTopMiner)

	hasExec, err := k.TrainingExecAllowListSet.Has(ctx, execAddr)
	require.NoError(t, err)
	require.False(t, hasExec)

	hasStart, err := k.TrainingStartAllowListSet.Has(ctx, startAddr)
	require.NoError(t, err)
	require.False(t, hasStart)

	for _, key := range trainingKeys {
		require.Nil(t, store.Get(key), "expected key %q to be deleted", string(key))
	}
	for _, pfx := range legacyPoCPrefixes {
		require.Equal(t, 0, countPrefixEntries(t, store, pfx))
	}

	pocV2Epoch, found := k.GetPocV2EnabledEpoch(ctx)
	require.True(t, found)
	require.Equal(t, uint64(123), pocV2Epoch)
}

func countPrefixEntries(t *testing.T, store *prefix.Store, pfx []byte) int {
	t.Helper()

	sub := prefix.NewStore(store, pfx)
	iter := sub.Iterator(nil, nil)
	defer iter.Close()

	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	return count
}
