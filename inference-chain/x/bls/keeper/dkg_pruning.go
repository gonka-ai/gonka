package keeper

import (
	"encoding/binary"
	"fmt"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/types"
)

// DKGSubKeyPruningMaxEpochsPerBlock bounds the EndBlock work of
// PruneDKGSubKeys. One epoch is a few dozen to ~100 deletes per prefix.
const DKGSubKeyPruningMaxEpochsPerBlock = 1

// PruneDKGSubKeys deletes the dealer parts, verification submissions and
// dealer complaints of epochs whose DKG material nobody can use any more.
// The base EpochBLSData record (participants, slot ranges, group public
// key, validation signature) is kept: old signatures are verified against
// it and queries keep answering.
//
// Epoch E is prunable when
//   - DKG for E+2 or later has been initiated. DKG for N starts at the end
//     of PoC validation in epoch N-1, so E is no longer the effective epoch
//     (no new signing request can name it) and E+1's DKG, whose group key
//     E's validators sign, is over;
//   - no threshold signing request for E is still pending. dAPI recovers
//     its slot shares from E's dealer parts (GetOrRecoverVerificationResult)
//     to sign such a request after a restart, and a request stays pending
//     until its deadline (signing_deadline_blocks, 923460 on mainnet).
//
// Epochs are pruned in order; a pending request holds the cursor at its
// epoch until the request completes, fails or expires.
func (k Keeper) PruneDKGSubKeys(ctx sdk.Context) error {
	latest, found := k.latestEpochBLSDataID(ctx)
	if !found {
		return nil
	}
	next, found := k.getDKGSubKeysPrunedEpoch(ctx)
	if found {
		next++
	} else {
		first, ok := k.firstEpochBLSDataID(ctx)
		if !ok {
			return nil
		}
		next = first
	}

	for i := 0; i < DKGSubKeyPruningMaxEpochsPerBlock; i++ {
		if next+2 > latest {
			return nil
		}
		pending, err := k.hasPendingSigningRequestForEpoch(ctx, next)
		if err != nil {
			return err
		}
		if pending {
			return nil
		}
		if err := k.DeleteDealerPartsForEpoch(ctx, next); err != nil {
			return fmt.Errorf("prune dealer parts epoch %d: %w", next, err)
		}
		if err := k.DeleteVerificationSubmissionsForEpoch(ctx, next); err != nil {
			return fmt.Errorf("prune verification submissions epoch %d: %w", next, err)
		}
		if err := k.DeleteDealerComplaintsForEpoch(ctx, next); err != nil {
			return fmt.Errorf("prune dealer complaints epoch %d: %w", next, err)
		}
		k.setDKGSubKeysPrunedEpoch(ctx, next)
		next++
	}
	return nil
}

func (k Keeper) epochBLSDataStore(ctx sdk.Context) prefix.Store {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return prefix.NewStore(store, types.EpochBLSDataPrefix)
}

// latestEpochBLSDataID returns the highest epoch with a base EpochBLSData
// record, i.e. the latest epoch whose DKG was initiated. Keys are the
// big-endian epoch id, so the first key of a reverse scan is the maximum.
func (k Keeper) latestEpochBLSDataID(ctx sdk.Context) (uint64, bool) {
	it := k.epochBLSDataStore(ctx).ReverseIterator(nil, nil)
	defer it.Close()
	return epochIDFromBLSDataKey(it)
}

func (k Keeper) firstEpochBLSDataID(ctx sdk.Context) (uint64, bool) {
	it := k.epochBLSDataStore(ctx).Iterator(nil, nil)
	defer it.Close()
	return epochIDFromBLSDataKey(it)
}

func epochIDFromBLSDataKey(it interface {
	Valid() bool
	Key() []byte
}) (uint64, bool) {
	if !it.Valid() || len(it.Key()) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(it.Key()), true
}

// hasPendingSigningRequestForEpoch reports whether a threshold signing
// request for epochID is still collecting signatures. Every such request
// has an expiration index entry until it reaches a terminal state, so the
// index is the set of pending requests.
func (k Keeper) hasPendingSigningRequestForEpoch(ctx sdk.Context, epochID uint64) (bool, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	it := prefix.NewStore(store, types.ExpirationIndexPrefix).Iterator(nil, nil)
	defer it.Close()
	for ; it.Valid(); it.Next() {
		_, requestID, err := parseExpirationIndexEntry(it.Key())
		if err != nil {
			// Unparseable entry: be conservative and keep the data.
			return true, nil
		}
		value := store.Get(types.ThresholdSigningRequestKey(requestID))
		if value == nil {
			continue
		}
		var request types.ThresholdSigningRequest
		if err := k.cdc.Unmarshal(value, &request); err != nil {
			return false, fmt.Errorf("unmarshal threshold signing request %x: %w", requestID, err)
		}
		if request.CurrentEpochId == epochID {
			return true, nil
		}
	}
	return false, nil
}

func (k Keeper) getDKGSubKeysPrunedEpoch(ctx sdk.Context) (uint64, bool) {
	value, err := k.storeService.OpenKVStore(ctx).Get(types.DKGSubKeysPrunedEpochKey)
	if err != nil || len(value) != 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(value), true
}

func (k Keeper) setDKGSubKeysPrunedEpoch(ctx sdk.Context, epochID uint64) {
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, epochID)
	_ = k.storeService.OpenKVStore(ctx).Set(types.DKGSubKeysPrunedEpochKey, value)
}
