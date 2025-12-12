# collections_guidelines.instructions.md
applyTo:
- inference-chain/x/**/*.go

---
When using any cosmos collections from a cosmos keeper (keeper.go), optimize iterations so that we do not load into memory or iterate more than we need. For example, for a collections.Pair[uint64,string] key,
you can iterate over all records that have the first value (the uint64) via something like:

```
func (k Keeper) GetEpochGroupDataForEpoch(
	ctx context.Context,
	epochIndex uint64,
) (val []types.EpochGroupData, found bool) {
	iter, err := k.EpochGroupDataMap.Iterate(ctx, collections.NewPrefixedPairRange[uint64, string](epochIndex))
	if err != nil {
		return val, false
	}
	epochGroupDataList, err := iter.Values()
	if err != nil {
		return val, false
	}
	return epochGroupDataList, true
}
```

This is MUCH better than loading in the entire collection and iterating. Look for this pattern in code.