package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// ClearTrainingState removes all persisted training state while preserving the
// keeper schema and key definitions needed for upgrade-time access.
func (k Keeper) ClearTrainingState(ctx context.Context) error {
	if err := k.TrainingExecAllowListSet.Clear(ctx, nil); err != nil {
		return err
	}
	if err := k.TrainingStartAllowListSet.Clear(ctx, nil); err != nil {
		return err
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	for _, keyPrefix := range [][]byte{
		[]byte(inferencetypes.TrainingTaskKeyPrefix),
		[]byte(inferencetypes.TrainingTaskSequenceKey),
		[]byte(inferencetypes.QueuedTrainingTaskKeyPrefix),
		[]byte(inferencetypes.InProgressTrainingTaskKeyPrefix),
		[]byte("TrainingTask/sync/"),
	} {
		prefixStore := prefix.NewStore(store, keyPrefix)
		iterator := prefixStore.Iterator(nil, nil)
		var keysToDelete [][]byte
		for ; iterator.Valid(); iterator.Next() {
			key := iterator.Key()
			keyCopy := make([]byte, len(key))
			copy(keyCopy, key)
			keysToDelete = append(keysToDelete, keyCopy)
		}
		iterator.Close()

		for _, key := range keysToDelete {
			prefixStore.Delete(key)
		}
	}

	return nil
}
