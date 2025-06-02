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
	ParamsKey          = []byte("p_bls")
	EpochBLSDataPrefix = []byte("epoch_bls_data")
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
