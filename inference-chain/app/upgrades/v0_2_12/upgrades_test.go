package v0_2_12

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	inferencekeeper "github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.12", UpgradeName)
}

func TestClearTrainingState(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	execAddr := sdk.MustAccAddressFromBech32(sample.AccAddress())
	startAddr := sdk.MustAccAddressFromBech32(sample.AccAddress())

	require.NoError(t, k.TrainingExecAllowListSet.Set(ctx, execAddr))
	require.NoError(t, k.TrainingStartAllowListSet.Set(ctx, startAddr))

	store := inferencekeeper.EmptyPrefixStore(ctx, &k)
	store.Set([]byte(inferencetypes.TrainingTaskKeyPrefix+"1"), []byte("task"))
	store.Set([]byte(inferencetypes.TrainingTaskSequenceKey), []byte{1})
	store.Set([]byte(inferencetypes.QueuedTrainingTaskKeyPrefix+"1"), []byte{1})
	store.Set([]byte(inferencetypes.InProgressTrainingTaskKeyPrefix+"1"), []byte{1})
	store.Set([]byte("TrainingTask/sync/1/store/key/value"), []byte("value"))
	store.Set([]byte("TrainingTask/sync/1/heartbeat/0/participant/node"), []byte("hb"))
	store.Set([]byte("TrainingTask/sync/1/barrier/b1/0/participant/node/value"), []byte("barrier"))

	require.NoError(t, clearTrainingState(ctx, k))

	hasExec, err := k.TrainingExecAllowListSet.Has(ctx, execAddr)
	require.NoError(t, err)
	require.False(t, hasExec)

	hasStart, err := k.TrainingStartAllowListSet.Has(ctx, startAddr)
	require.NoError(t, err)
	require.False(t, hasStart)

	for _, key := range [][]byte{
		[]byte(inferencetypes.TrainingTaskKeyPrefix + "1"),
		[]byte(inferencetypes.TrainingTaskSequenceKey),
		[]byte(inferencetypes.QueuedTrainingTaskKeyPrefix + "1"),
		[]byte(inferencetypes.InProgressTrainingTaskKeyPrefix + "1"),
		[]byte("TrainingTask/sync/1/store/key/value"),
		[]byte("TrainingTask/sync/1/heartbeat/0/participant/node"),
		[]byte("TrainingTask/sync/1/barrier/b1/0/participant/node/value"),
	} {
		require.Nil(t, store.Get(key), "expected key %q to be deleted", string(key))
	}
}
