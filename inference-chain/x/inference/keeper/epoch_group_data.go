package keeper

import (
	"context"
	"encoding/hex"
	"errors"

	"cosmossdk.io/collections"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// SetEpochGroupData set a specific epochGroupData in the store from its index
func (k Keeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	k.EpochGroupDataMap.Set(ctx, collections.Join(epochGroupData.EpochIndex, epochGroupData.ModelId), storedEpochGroupData(epochGroupData))
	k.forgetEpochGroupData(ctx, epochGroupData.EpochIndex, epochGroupData.ModelId)
}

// storedEpochGroupData keeps each seed signature's member address and hex
// signature as raw bytes; restoredEpochGroupData undoes it. A string that does
// not round-trip stays as it is. The caller's slice is not modified.
func storedEpochGroupData(egd types.EpochGroupData) types.EpochGroupData {
	if len(egd.MemberSeedSignatures) == 0 {
		return egd
	}
	sigs := make([]*types.SeedSignature, len(egd.MemberSeedSignatures))
	for i, s := range egd.MemberSeedSignatures {
		if s == nil {
			continue
		}
		stored := *s
		if b, ok := rawAddress(stored.MemberAddress); ok {
			stored.MemberAddr, stored.MemberAddress = b, ""
		}
		if b, ok := rawHex(stored.Signature); ok {
			stored.SignatureRaw, stored.Signature = b, ""
		}
		sigs[i] = &stored
	}
	egd.MemberSeedSignatures = sigs
	return egd
}

func restoredEpochGroupData(egd types.EpochGroupData) types.EpochGroupData {
	for _, s := range egd.MemberSeedSignatures {
		if s == nil {
			continue
		}
		if len(s.MemberAddr) > 0 {
			s.MemberAddress, s.MemberAddr = sdk.AccAddress(s.MemberAddr).String(), nil
		}
		if len(s.SignatureRaw) > 0 {
			s.Signature, s.SignatureRaw = hex.EncodeToString(s.SignatureRaw), nil
		}
	}
	return egd
}

// rawHex returns the bytes of a non-empty lower-case hex string that encodes back to the same string.
func rawHex(s string) ([]byte, bool) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) == 0 || hex.EncodeToString(b) != s {
		return nil, false
	}
	return b, true
}

// GetEpochGroupData returns a epochGroupData from its index
func (k Keeper) GetEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) (val types.EpochGroupData, found bool) {
	val, found, _ = k.GetEpochGroupDataWithError(ctx, epochIndex, modelId)
	return val, found
}

// GetEpochGroupDataWithError distinguishes a missing record from a store error.
func (k Keeper) GetEpochGroupDataWithError(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) (val types.EpochGroupData, found bool, err error) {
	if c := egdCacheFrom(ctx); c != nil {
		return k.getEpochGroupDataTxCached(ctx, c, epochIndex, modelId)
	}
	val, err = k.EpochGroupDataMap.Get(ctx, collections.Join(epochIndex, modelId))
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return val, false, nil
		}
		return val, false, err
	}
	return restoredEpochGroupData(val), true, nil
}

// RemoveEpochGroupData removes a epochGroupData from the store
func (k Keeper) RemoveEpochGroupData(
	ctx context.Context,
	epochIndex uint64,
	modelId string,
) {
	k.EpochGroupDataMap.Remove(ctx, collections.Join(epochIndex, modelId))
	k.forgetEpochGroupData(ctx, epochIndex, modelId)
}

// egdTxCache keeps EpochGroupData bytes read in one tx or EndBlock; a written key is
// not cached again, since the write may sit in a discarded CacheContext.
type egdTxCache struct {
	bz      map[string][]byte // by store key: collections.Pair holds pointers
	written map[string]bool
}

func egdCacheFrom(ctx context.Context) *egdTxCache {
	if c, ok := ctx.Value(txParamsCacheKey{}).(*txParamsCache); ok && c != nil {
		return &c.egd
	}
	return nil
}

func (k Keeper) egdStoreKey(epochIndex uint64, modelId string) ([]byte, error) {
	return collections.EncodeKeyWithPrefix(k.EpochGroupDataMap.GetPrefix(), k.EpochGroupDataMap.KeyCodec(), collections.Join(epochIndex, modelId))
}

func (k Keeper) forgetEpochGroupData(ctx context.Context, epochIndex uint64, modelId string) {
	c := egdCacheFrom(ctx)
	if c == nil {
		return
	}
	storeKey, err := k.egdStoreKey(epochIndex, modelId)
	if err != nil {
		return // the Map write failed on the same encoding
	}
	delete(c.bz, string(storeKey))
	if c.written == nil {
		c.written = make(map[string]bool)
	}
	c.written[string(storeKey)] = true
}

func (k Keeper) getEpochGroupDataTxCached(
	ctx context.Context,
	c *egdTxCache,
	epochIndex uint64,
	modelId string,
) (val types.EpochGroupData, found bool, err error) {
	storeKey, err := k.egdStoreKey(epochIndex, modelId)
	if err != nil {
		return val, false, err
	}
	bz, ok := c.bz[string(storeKey)]
	if !ok {
		bz = runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx)).Get(storeKey)
		if bz == nil {
			return val, false, nil
		}
		if !c.written[string(storeKey)] {
			if c.bz == nil {
				c.bz = make(map[string][]byte)
			}
			c.bz[string(storeKey)] = append([]byte(nil), bz...)
		}
	}
	val, err = k.EpochGroupDataMap.ValueCodec().Decode(bz)
	if err != nil {
		return types.EpochGroupData{}, false, err
	}
	return restoredEpochGroupData(val), true, nil
}

// GetAllEpochGroupData returns all epochGroupData
func (k Keeper) GetAllEpochGroupData(ctx context.Context) (list []types.EpochGroupData) {
	iter, err := k.EpochGroupDataMap.Iterate(ctx, nil)
	if err != nil {
		return nil
	}
	epochGroupDataList, err := iter.Values()
	if err != nil {
		return nil
	}
	for i := range epochGroupDataList {
		epochGroupDataList[i] = restoredEpochGroupData(epochGroupDataList[i])
	}
	return epochGroupDataList
}
