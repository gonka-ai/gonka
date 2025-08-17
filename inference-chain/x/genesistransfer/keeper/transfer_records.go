package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/store/prefix"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/query"

	"github.com/productscience/inference/x/genesistransfer/types"
)

// GetAllTransferRecords retrieves all transfer records with optional pagination
// This function supports historical transfer record enumeration for audit and querying
func (k Keeper) GetAllTransferRecords(ctx context.Context, pagination *query.PageRequest) ([]types.TransferRecord, *query.PageResponse, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))

	var transferRecords []types.TransferRecord

	pageRes, err := query.Paginate(transferStore, pagination, func(key []byte, value []byte) error {
		var record types.TransferRecord
		if err := k.cdc.Unmarshal(value, &record); err != nil {
			return errorsmod.Wrapf(err, "failed to unmarshal transfer record")
		}
		transferRecords = append(transferRecords, record)
		return nil
	})

	if err != nil {
		return nil, nil, errorsmod.Wrapf(err, "failed to paginate transfer records")
	}

	return transferRecords, pageRes, nil
}

// GetTransferRecordsByRecipient retrieves all transfer records for a specific recipient
// This function enables querying transfers received by a particular address
func (k Keeper) GetTransferRecordsByRecipient(ctx context.Context, recipientAddr string) ([]types.TransferRecord, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))

	var matchingRecords []types.TransferRecord

	iterator := transferStore.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var record types.TransferRecord
		if err := k.cdc.Unmarshal(iterator.Value(), &record); err != nil {
			k.Logger().Error(
				"failed to unmarshal transfer record during recipient search",
				"error", err,
				"key", string(iterator.Key()),
			)
			continue // Skip malformed records but continue processing
		}

		if record.RecipientAddress == recipientAddr {
			matchingRecords = append(matchingRecords, record)
		}
	}

	return matchingRecords, nil
}

// GetTransferRecordsCount returns the total number of transfer records
// This function provides statistics for monitoring and administrative purposes
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

// GetTransferRecordsByHeight retrieves transfer records from a specific block height
// This function enables querying transfers that occurred at a particular height
func (k Keeper) GetTransferRecordsByHeight(ctx context.Context, height uint64) ([]types.TransferRecord, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))

	var matchingRecords []types.TransferRecord

	iterator := transferStore.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var record types.TransferRecord
		if err := k.cdc.Unmarshal(iterator.Value(), &record); err != nil {
			k.Logger().Error(
				"failed to unmarshal transfer record during height search",
				"error", err,
				"key", string(iterator.Key()),
			)
			continue // Skip malformed records but continue processing
		}

		if record.TransferHeight == height {
			matchingRecords = append(matchingRecords, record)
		}
	}

	return matchingRecords, nil
}

// DeleteTransferRecord removes a transfer record from storage
// This function is primarily for administrative purposes and testing
// Note: In production, transfer records should generally be immutable for audit purposes
func (k Keeper) DeleteTransferRecord(ctx context.Context, genesisAddr sdk.AccAddress) error {
	if genesisAddr == nil {
		return errors.ErrInvalidAddress.Wrap("genesis address cannot be nil")
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))

	key := genesisAddr.Bytes()
	if !transferStore.Has(key) {
		return types.ErrAccountNotFound.Wrapf("transfer record not found for genesis address %s", genesisAddr.String())
	}

	transferStore.Delete(key)

	k.Logger().Info(
		"transfer record deleted",
		"genesis_address", genesisAddr.String(),
	)

	return nil
}

// HasTransferRecord checks if a transfer record exists for a genesis account
// This function provides a quick existence check without retrieving the full record
func (k Keeper) HasTransferRecord(ctx context.Context, genesisAddr sdk.AccAddress) bool {
	if genesisAddr == nil {
		return false
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))

	key := genesisAddr.Bytes()
	return transferStore.Has(key)
}

// ValidateTransferRecord performs comprehensive validation of a transfer record
// This function ensures record integrity and consistency
func (k Keeper) ValidateTransferRecord(ctx context.Context, record types.TransferRecord) error {
	// Use the existing validation from types
	if err := record.Validate(); err != nil {
		return errorsmod.Wrapf(err, "transfer record validation failed")
	}

	// Additional keeper-level validations
	genesisAddr, err := sdk.AccAddressFromBech32(record.GenesisAddress)
	if err != nil {
		return errorsmod.Wrapf(err, "invalid genesis address in record")
	}

	recipientAddr, err := sdk.AccAddressFromBech32(record.RecipientAddress)
	if err != nil {
		return errorsmod.Wrapf(err, "invalid recipient address in record")
	}

	// Validate that the genesis and recipient addresses are different
	if genesisAddr.Equals(recipientAddr) {
		return types.ErrInvalidTransfer.Wrap("genesis and recipient addresses cannot be the same")
	}

	// Validate transfer height is reasonable (not in the future)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	currentHeight := uint64(sdkCtx.BlockHeight())
	if record.TransferHeight > currentHeight {
		return types.ErrInvalidTransfer.Wrapf(
			"transfer height %d cannot be in the future (current height: %d)",
			record.TransferHeight,
			currentHeight,
		)
	}

	return nil
}

// GetTransferRecordIterator returns an iterator over all transfer records
// This function provides low-level access for advanced use cases
func (k Keeper) GetTransferRecordIterator(ctx context.Context) storetypes.Iterator {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))
	return transferStore.Iterator(nil, nil)
}

// ExportTransferRecords exports all transfer records for genesis export
// This function supports chain upgrades and state exports
func (k Keeper) ExportTransferRecords(ctx context.Context) ([]types.TransferRecord, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	transferStore := prefix.NewStore(store, []byte(types.TransferRecordKeyPrefix))

	var allRecords []types.TransferRecord

	iterator := transferStore.Iterator(nil, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var record types.TransferRecord
		if err := k.cdc.Unmarshal(iterator.Value(), &record); err != nil {
			return nil, errorsmod.Wrapf(err, "failed to unmarshal transfer record during export")
		}
		allRecords = append(allRecords, record)
	}

	k.Logger().Info(
		"transfer records exported",
		"count", len(allRecords),
	)

	return allRecords, nil
}

// ImportTransferRecords imports transfer records during genesis import
// This function supports chain initialization and state imports
func (k Keeper) ImportTransferRecords(ctx context.Context, records []types.TransferRecord) error {
	for i, record := range records {
		// Validate each record before import
		if err := k.ValidateTransferRecord(ctx, record); err != nil {
			return errorsmod.Wrapf(err, "invalid transfer record at index %d", i)
		}

		// Import the record
		if err := k.SetTransferRecord(ctx, record); err != nil {
			return errorsmod.Wrapf(err, "failed to import transfer record at index %d", i)
		}
	}

	k.Logger().Info(
		"transfer records imported",
		"count", len(records),
	)

	return nil
}
