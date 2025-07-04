package v1_11

import (
	"context"
	"fmt"
	"sort"

	"cosmossdk.io/store/prefix"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

/*
Changes description:

1. Epoch: Added the entity, now for every root epoch group (modelId == "") an epoch is created.
   EpochGroupData is linked to Epoch by PocStartBlockHeight.
2. EpochGroupData: added EpochId field to link it to Epoch.
3. Inference:
  a. Deprecated epoch_group_id, which was actually the PocStartBlockHeight.
  b. Added epoch_id, which is the EpochId from Epoch.
  c. Added epoch_poc_start_block_height, which is the PocStartBlockHeight from EpochGroupData.
4. InferenceValidationDetails:
  a. Deprecated epoch_id, which was actually the epoch_group_id (the thing created by group module)
  b. Added epoch_group_id, which is the EpochId from Epoch.
5. ActiveParticipants:
  a. Deprecated epoch_group_id (the thing created by group module). It was also THE KEY of the entity in the KV storage.
  b. Added epoch_id, which is the new KEY. The migration includes key changes.
*/

// kvPair is a small helper type for buffered writes.
type kvPair struct {
	key   []byte
	value []byte
}

// writeBuffered writes accumulated kvPairs to the provided store and resets the buffer slice.
// It returns the (now reset) slice so it can be reused without extra allocations.
func writeBuffered(store *prefix.Store, buf []kvPair) []kvPair {
	for _, p := range buf {
		store.Set(p.key, p.value)
	}
	// Reuse the slice memory: set length to zero but keep capacity.
	return buf[:0]
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		for moduleName, version := range vm {
			fmt.Printf("Module: %s, Version: %d\n", moduleName, version)
		}
		fmt.Printf("OrderMigrations: %v\n", mm.OrderMigrations)
		pocStartBlockHeightToEpochId := createEpochs(ctx, k)
		setEpochIdToInferences(ctx, k, pocStartBlockHeightToEpochId)

		renameInferenceValidationDetailsEpochId(ctx, k)
		renameActiveParticipantsEpochId(ctx, k, pocStartBlockHeightToEpochId)

		return vm, nil
	}
}

func createEpochs(ctx context.Context, k keeper.Keeper) map[uint64]uint64 {
	epochGroupData := k.GetAllEpochGroupData(ctx)
	k.LogInfo(UpgradeName+" - queried all epochGroupData", types.Upgrades, "len(epochGroupData)", len(epochGroupData))
	rootEpochGroups := make([]*types.EpochGroupData, 0)
	for _, epochData := range epochGroupData {
		if epochData.ModelId == "" {
			rootEpochGroups = append(rootEpochGroups, &epochData)
		}
	}
	k.LogInfo(UpgradeName+" - filtered root epoch groups", types.Upgrades, "len(rootEpochGroups)", len(rootEpochGroups))

	sort.Slice(rootEpochGroups, func(i, j int) bool {
		return rootEpochGroups[i].PocStartBlockHeight < rootEpochGroups[j].PocStartBlockHeight
	})

	startBlockHeightToEpochId := make(map[uint64]uint64)
	var lastEpochIndex uint64
	for i, epochGroup := range rootEpochGroups {
		epochId := uint64(i + 1)
		lastEpochIndex = epochId

		k.LogInfo(UpgradeName+" - processing epoch group. "+
			"About to create an epoch and update epochGroupData with EpochId", types.Upgrades,
			"epochGroup.PocStartBlockHeight", epochGroup.PocStartBlockHeight,
			"i", i,
			"epochId", epochId)
		epoch := &types.Epoch{
			Index:               epochId,
			PocStartBlockHeight: int64(epochGroup.PocStartBlockHeight),
		}
		k.SetEpoch(ctx, epoch)

		startBlockHeightToEpochId[epochGroup.PocStartBlockHeight] = epochId

		epochGroup.EpochId = epochId
		k.SetEpochGroupData(ctx, *epochGroup)
	}

	k.LogInfo(UpgradeName+" - created epochs, running SetEffectiveEpochIndex", types.Upgrades, "lastEpochIndex", lastEpochIndex)
	k.SetEffectiveEpochIndex(ctx, lastEpochIndex)

	// TODO: Create genesis epoch
	genesisEpoch := &types.Epoch{
		Index:               0,
		PocStartBlockHeight: 0,
	}
	k.SetEpoch(ctx, genesisEpoch)

	return startBlockHeightToEpochId
}

func setEpochIdToInferences(ctx context.Context, k keeper.Keeper, pocStartBlockHeightToEpochId map[uint64]uint64) {
	// Stream through the store instead of loading everything into RAM.
	store := keeper.PrefixStore(ctx, &k, []byte(types.InferenceKeyPrefix))
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	const batchSize = 1000
	var updates []kvPair

	i := 0
	skipped := 0
	for ; iterator.Valid(); iterator.Next() {
		var inf types.Inference
		if err := k.Codec().Unmarshal(iterator.Value(), &inf); err != nil {
			k.LogError(UpgradeName+" - failed to unmarshal inference", types.Upgrades, "err", err)
			continue
		}

		epochId, ok := pocStartBlockHeightToEpochId[inf.EpochGroupId]
		if !ok {
			k.LogError(UpgradeName+" - EpochId not found for Inference", types.Upgrades,
				"inferenceId", inf.InferenceId,
				"epochGroupId", inf.EpochGroupId)
			skipped++
			continue
		}

		inf.EpochId = epochId
		inf.EpochPocStartBlockHeight = inf.EpochGroupId // field rename

		bz, err := k.Codec().Marshal(&inf)
		if err != nil {
			k.LogError(UpgradeName+" - failed to marshal inference", types.Upgrades, "err", err)
			skipped++
			continue
		}
		keyCopy := append([]byte(nil), iterator.Key()...)
		updates = append(updates, kvPair{keyCopy, bz})

		i++

		if len(updates) >= batchSize {
			updates = writeBuffered(store, updates)
		}
	}

	if len(updates) > 0 {
		writeBuffered(store, updates)
	}

	total := i + skipped
	k.LogInfo(UpgradeName+" - set EpochId to Inferences", types.Upgrades,
		"processed", i,
		"skipped", skipped)

	// validation
	validateCount(ctx, k, []byte(types.InferenceKeyPrefix), total, "inferences")
}

func renameInferenceValidationDetailsEpochId(ctx context.Context, k keeper.Keeper) {
	store := keeper.PrefixStore(ctx, &k, []byte(types.InferenceValidationDetailsKeyPrefix))
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	type kv struct {
		key   []byte
		value []byte
	}
	const batchSize = 1000
	var updates []kvPair

	i := 0
	skipped := 0
	for ; iterator.Valid(); iterator.Next() {
		var vd types.InferenceValidationDetails
		if err := k.Codec().Unmarshal(iterator.Value(), &vd); err != nil {
			k.LogError(UpgradeName+" - failed to unmarshal validation details", types.Upgrades, "err", err)
			skipped++
			continue
		}

		vd.EpochGroupId = vd.EpochId

		bz, err := k.Codec().Marshal(&vd)
		if err != nil {
			k.LogError(UpgradeName+" - failed to marshal validation details", types.Upgrades, "err", err)
			skipped++
			continue
		}
		keyCopy := append([]byte(nil), iterator.Key()...)
		updates = append(updates, kvPair{keyCopy, bz})

		i++
		if len(updates) >= batchSize {
			updates = writeBuffered(store, updates)
		}
	}

	if len(updates) > 0 {
		writeBuffered(store, updates)
	}

	total := i + skipped
	k.LogInfo(UpgradeName+" - renamed InferenceValidationDetails EpochId to EpochGroupId", types.Upgrades,
		"processed", i,
		"skipped", skipped)

	// validation
	validateCount(ctx, k, []byte(types.InferenceValidationDetailsKeyPrefix), total, "validationDetails")
}

func renameActiveParticipantsEpochId(ctx context.Context, k keeper.Keeper, pocStartBlockHeightToEpochId map[uint64]uint64) {
	emptyPrefixStore := keeper.EmptyPrefixStore(ctx, &k)
	store := keeper.PrefixStore(ctx, &k, []byte(types.ActiveParticipantsKeyPrefixV1))
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	const batchSize = 1000
	var updates []kvPair

	i := 0
	skipped := 0
	for ; iterator.Valid(); iterator.Next() {
		var ap types.ActiveParticipants
		if err := k.Codec().Unmarshal(iterator.Value(), &ap); err != nil {
			k.LogError(UpgradeName+" - failed to unmarshal active participants", types.Upgrades, "err", err)
			skipped++
			continue
		}

		epochId, ok := pocStartBlockHeightToEpochId[uint64(ap.PocStartBlockHeight)]
		if !ok {
			k.LogError(UpgradeName+" - EpochId not found for ActiveParticipants", types.Upgrades,
				"pocStartBlockHeight", ap.PocStartBlockHeight)
			skipped++
			continue
		}
		ap.EpochId = epochId

		bz, err := k.Codec().Marshal(&ap)
		if err != nil {
			k.LogError(UpgradeName+" - failed to marshal active participants", types.Upgrades, "err", err)
			skipped++
			continue
		}

		newKey := types.ActiveParticipantsFullKey(epochId)
		updates = append(updates, kvPair{newKey, bz})
		i++

		if len(updates) >= batchSize {
			updates = writeBuffered(emptyPrefixStore, updates)
		}
	}

	if len(updates) > 0 {
		writeBuffered(emptyPrefixStore, updates)
	}

	total := i + skipped

	// validation – just count all current ActiveParticipants keys
	// 2 x total, because they share the same key prefix
	validateCount(ctx, k, []byte(types.ActiveParticipantsKeyPrefixV1), 2*total, "activeParticipants")
	validateCount(ctx, k, []byte(types.ActiveParticipantsKeyPrefix), total, "activeParticipants")
}

func validateCount(ctx context.Context, k keeper.Keeper, keyPrefix []byte, expected int, label string) {
	store := keeper.PrefixStore(ctx, &k, keyPrefix)
	iter := store.Iterator(nil, nil)
	defer iter.Close()

	actual := 0
	for ; iter.Valid(); iter.Next() {
		actual++
	}

	if expected == actual {
		k.LogInfo(fmt.Sprintf("%s[migration-validation] %s count: SUCCESS", UpgradeName, label), types.Upgrades,
			"expected", expected, "actual", actual)
	} else {
		k.LogInfo(fmt.Sprintf("%s[migration-validation] %s count: FAILURE", UpgradeName, label), types.Upgrades,
			"expected", expected, "actual", actual)
	}

}
