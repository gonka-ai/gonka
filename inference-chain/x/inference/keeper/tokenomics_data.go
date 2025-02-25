package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetTokenomicsData set tokenomicsData in the store
func (k Keeper) SetTokenomicsData(ctx context.Context, tokenomicsData types.TokenomicsData) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.TokenomicsDataKey))
	b := k.cdc.MustMarshal(&tokenomicsData)
	store.Set([]byte{0}, b)
}

// GetTokenomicsData returns tokenomicsData
func (k Keeper) GetTokenomicsData(ctx context.Context) (val types.TokenomicsData, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.TokenomicsDataKey))

	b := store.Get([]byte{0})
	if b == nil {
		return val, false
	}

	k.cdc.MustUnmarshal(b, &val)
	return val, true
}

func (k Keeper) AddTokenomicsData(ctx context.Context, tokenomicsData *types.TokenomicsData) {
	k.LogInfo("Adding tokenomics data", types.Tokenomics, "tokenomicsData", tokenomicsData)
	current, found := k.GetTokenomicsData(ctx)
	if !found {
		k.LogError("Tokenomics data not found", types.Tokenomics)
	}
	current.TotalBurned = current.TotalBurned + tokenomicsData.TotalBurned
	current.TotalFees = current.TotalFees + tokenomicsData.TotalFees
	current.TotalSubsidies = current.TotalSubsidies + tokenomicsData.TotalSubsidies
	current.TotalRefunded = current.TotalRefunded + tokenomicsData.TotalRefunded
	k.SetTokenomicsData(ctx, current)
	newData, _ := k.GetTokenomicsData(ctx)
	k.LogInfo("Tokenomics data added", types.Tokenomics, "tokenomicsData", newData)
}
