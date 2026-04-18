package types

import (
	"encoding/binary"
	"fmt"
)

const (
	// ModuleName defines the module name
	ModuleName = "bls"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_bls"
)

var (
	ParamsKey                       = []byte("p_bls")
	EpochBLSDataPrefix              = []byte("epoch_bls_data")
	DealerPartPrefix                = []byte("epoch_bls_dealer_part/")
	ThresholdSigningRequestPrefix   = []byte("threshold_signing_request")
	ExpirationIndexPrefix           = []byte("expiration_index")
	GroupValidationPrefix           = []byte("group_validation_")
	CompletedPostProcessRetryPrefix = []byte("completed_post_process_retry")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}

// EpochBLSDataKey generates a key for storing EpochBLSData by epoch ID
func EpochBLSDataKey(epochID uint64) []byte {
	key := make([]byte, len(EpochBLSDataPrefix)+8)
	copy(key, EpochBLSDataPrefix)
	binary.BigEndian.PutUint64(key[len(EpochBLSDataPrefix):], epochID)
	return key
}

// DealerPartEpochPrefix returns the prefix used to iterate all dealer parts
// for a given epoch ID. Callers wrap the module KV store in a prefix.Store
// scoped to this prefix, then use DealerPartSubKey for per-participant
// point access.
//
// Storing each dealer part under its own key prevents the entire
// EpochBLSData struct from being rewritten on every MsgSubmitDealerPart
// submission. Before this change, the Nth dealer paid gas proportional to N
// (because the dealing struct accumulated dealer parts inline), which
// created a race where the DAPI's simulation-based gas estimate was too low
// by the time the tx landed, pushing later dealers over the declared gas
// limit and failing them out of the DKG entirely.
//
// Full layout: {DealerPartPrefix}{epoch_id:uint64 BE}/{participant_index:uint32 BE}.
func DealerPartEpochPrefix(epochID uint64) []byte {
	prefix := make([]byte, len(DealerPartPrefix)+8+1)
	copy(prefix, DealerPartPrefix)
	binary.BigEndian.PutUint64(prefix[len(DealerPartPrefix):], epochID)
	prefix[len(DealerPartPrefix)+8] = '/'
	return prefix
}

// DealerPartSubKey returns the sub-key portion of a dealer part entry (the
// bytes under DealerPartEpochPrefix). This is what callers use when working
// through a prefix.Store scoped to a single epoch.
func DealerPartSubKey(participantIndex uint32) []byte {
	sub := make([]byte, 4)
	binary.BigEndian.PutUint32(sub, participantIndex)
	return sub
}

// ParseDealerPartSubKey decodes a sub-key produced by DealerPartSubKey back
// into a participant index.
func ParseDealerPartSubKey(sub []byte) (uint32, error) {
	if len(sub) != 4 {
		return 0, fmt.Errorf("invalid dealer part sub-key length %d (want 4)", len(sub))
	}
	return binary.BigEndian.Uint32(sub), nil
}

// ThresholdSigningRequestKey generates a key for storing ThresholdSigningRequest by request ID
// This results in a variable length key, as we put no constraints on the request_id
func ThresholdSigningRequestKey(requestID []byte) []byte {
	key := make([]byte, len(ThresholdSigningRequestPrefix)+len(requestID))
	copy(key, ThresholdSigningRequestPrefix)
	copy(key[len(ThresholdSigningRequestPrefix):], requestID)
	return key
}

// ExpirationIndexKey generates a key for the expiration index: expiration_index/{deadline_block_height}/{request_id}
func ExpirationIndexKey(deadlineBlockHeight int64, requestID []byte) []byte {
	deadlineBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(deadlineBytes, uint64(deadlineBlockHeight))

	key := make([]byte, len(ExpirationIndexPrefix)+8+len(requestID))
	copy(key, ExpirationIndexPrefix)
	copy(key[len(ExpirationIndexPrefix):], deadlineBytes)
	copy(key[len(ExpirationIndexPrefix)+8:], requestID)
	return key
}

// ExpirationIndexPrefixForBlock generates a prefix to scan all requests expiring at a specific block height
func ExpirationIndexPrefixForBlock(blockHeight int64) []byte {
	deadlineBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(deadlineBytes, uint64(blockHeight))

	prefix := make([]byte, len(ExpirationIndexPrefix)+8)
	copy(prefix, ExpirationIndexPrefix)
	copy(prefix[len(ExpirationIndexPrefix):], deadlineBytes)
	return prefix
}

// GroupValidationKey generates a key for the group validation state by epoch ID
func GroupValidationKey(epochID uint64) []byte {
	return []byte(fmt.Sprintf("%s%d", GroupValidationPrefix, epochID))
}

func CompletedPostProcessRetryKey(requestID []byte) []byte {
	key := make([]byte, len(CompletedPostProcessRetryPrefix)+len(requestID))
	copy(key, CompletedPostProcessRetryPrefix)
	copy(key[len(CompletedPostProcessRetryPrefix):], requestID)
	return key
}
