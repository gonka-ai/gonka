package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/productscience/inference/x/inference/types"
)

// SetTokenomicsData set tokenomicsData in the store
func (k Keeper) SetTokenomicsData(ctx context.Context, tokenomicsData types.TokenomicsData) error {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.TokenomicsDataKey))
	b, err := k.cdc.Marshal(&tokenomicsData)
	if err != nil {
		return err
	}
	store.Set([]byte{0}, b)
	return nil
}

// GetTokenomicsData returns tokenomicsData
func (k Keeper) GetTokenomicsData(ctx context.Context) (val types.TokenomicsData, found bool) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, types.KeyPrefix(types.TokenomicsDataKey))

	b := store.Get([]byte{0})
	if b == nil {
		return val, false
	}

	err := k.cdc.Unmarshal(b, &val)
	if err != nil {
		return val, false
	}
	return val, true
}

func (k Keeper) AddTokenomicsData(ctx context.Context, tokenomicsData *types.TokenomicsData) error {
	k.LogInfo("Adding tokenomics data", types.Tokenomics, "tokenomicsData", tokenomicsData)
	current, found := k.GetTokenomicsData(ctx)
	if !found {
		k.LogError("Tokenomics data not found", types.Tokenomics)
	}
	current.TotalBurned = current.TotalBurned + tokenomicsData.TotalBurned
	current.TotalFees = current.TotalFees + tokenomicsData.TotalFees
	current.TotalSubsidies = current.TotalSubsidies + tokenomicsData.TotalSubsidies
	current.TotalRefunded = current.TotalRefunded + tokenomicsData.TotalRefunded
	err := k.SetTokenomicsData(ctx, current)
	if err != nil {
		return err
	}
	newData, _ := k.GetTokenomicsData(ctx)
	k.LogInfo("Tokenomics data added", types.Tokenomics, "tokenomicsData", newData)
	return nil
}
