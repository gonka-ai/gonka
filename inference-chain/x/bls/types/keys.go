package types

import (
	"encoding/binary"
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
	ParamsKey                     = []byte("p_bls")
	EpochBLSDataPrefix            = []byte("epoch_bls_data")
	ThresholdSigningRequestPrefix = []byte("threshold_signing_request")
	ExpirationIndexPrefix         = []byte("expiration_index")
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
