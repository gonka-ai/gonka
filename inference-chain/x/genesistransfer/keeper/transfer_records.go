package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"

	"github.com/productscience/inference/x/genesistransfer/types"
)

// GetAllTransferRecords returns all transfer records with pagination support
// Used by the TransferHistory query endpoint
func (k Keeper) GetAllTransferRecords(ctx context.Context, pagination *query.PageRequest) ([]types.TransferRecord, *query.PageResponse, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))

	var records []types.TransferRecord
	pageRes, err := query.Paginate(transferStore, pagination, func(key []byte, value []byte) error {
		var record types.TransferRecord
		if err := k.cdc.Unmarshal(value, &record); err != nil {
			// Skip malformed records instead of failing the entire query
			k.Logger().Warn("skipping malformed transfer record", "key", string(key), "error", err)
			return nil
		}
		records = append(records, record)
		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	return records, pageRes, nil
}

// HasTransferRecord checks if a transfer record exists for a genesis account
// Used for quick existence checks
func (k Keeper) HasTransferRecord(ctx context.Context, genesisAddr sdk.AccAddress) bool {
	if genesisAddr == nil {
		return false
	}
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))
	key := genesisAddr.Bytes()
	return transferStore.Has(key)
}

// GetTransferRecordsCount returns the total number of transfer records
// Used by tests to verify record creation
func (k Keeper) GetTransferRecordsCount(ctx context.Context) (uint64, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))

	iterator := transferStore.Iterator(nil, nil)
	defer iterator.Close()

	count := uint64(0)
	for ; iterator.Valid(); iterator.Next() {
		count++
	}

	return count, nil
}
