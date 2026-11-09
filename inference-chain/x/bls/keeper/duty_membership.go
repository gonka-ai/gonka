package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/bls/types"
)

// IsEpochBLSParticipant reports whether addr is in epochID's DKG participant
// list. Reads only the base record, without the dealer-part, verification and
// complaint sub-keys GetEpochBLSData merges, so the ante handler can call it.
func (k Keeper) IsEpochBLSParticipant(ctx sdk.Context, epochID uint64, addr string) (bool, error) {
	value, err := k.storeService.OpenKVStore(ctx).Get(types.EpochBLSDataKey(epochID))
	if err != nil {
		return false, err
	}
	if value == nil {
		return false, types.ErrEpochBLSDataNotFound
	}
	var base types.EpochBLSData
	if err := k.cdc.Unmarshal(value, &base); err != nil {
		return false, err
	}
	for _, p := range base.Participants {
		if p.Address == addr {
			return true, nil
		}
	}
	return false, nil
}

// SigningRequestEpoch returns the epoch whose participants sign a threshold
// signing request, without merging its partial-signature sub-keys.
func (k Keeper) SigningRequestEpoch(ctx sdk.Context, requestID []byte) (uint64, error) {
	value, err := k.storeService.OpenKVStore(ctx).Get(types.ThresholdSigningRequestKey(requestID))
	if err != nil {
		return 0, err
	}
	if value == nil {
		return 0, types.ErrSigningRequestNotFound
	}
	var request types.ThresholdSigningRequest
	if err := k.cdc.Unmarshal(value, &request); err != nil {
		return 0, err
	}
	return request.CurrentEpochId, nil
}
