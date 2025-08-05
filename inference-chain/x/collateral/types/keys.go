package types

import (
	"encoding/binary"
	"fmt"
)

const (
	// ModuleName defines the module name
	ModuleName = "collateral"

	SubAccountCollateral = "collateral"

	SubAccountUnbonding = "collateral-unbonding"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_collateral"
)

var (
	ParamsKey = []byte("p_collateral")

	// CurrentEpochKey is the key to store the current epoch index for the collateral module
	CurrentEpochKey = []byte("CurrentEpoch")

	// CollateralKey is the prefix to store collateral for participants
	CollateralKey = []byte("collateral/")

	// UnbondingKey is the prefix for unbonding entries
	// Format: unbonding/{completionEpoch}/{participantAddress}
	UnbondingKey = []byte("unbonding/")

	// JailedKey is the prefix for jailed participant entries
	JailedKey = []byte("jailed/")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}

// GetCollateralKey returns the store key for a participant's collateral
func GetCollateralKey(participantAddress string) []byte {
	return append(CollateralKey, []byte(participantAddress)...)
}

// GetUnbondingKey returns the store key for an unbonding entry
// Format: unbonding/{completionEpoch}/{participantAddress}
func GetUnbondingKey(completionEpoch uint64, participantAddress string) []byte {
	epochBz := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBz, completionEpoch)
	return append(append(UnbondingKey, epochBz...), []byte(participantAddress)...)
}

// GetUnbondingEpochPrefix returns the prefix for all unbonding entries for a specific epoch
func GetUnbondingEpochPrefix(completionEpoch uint64) []byte {
	epochBz := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBz, completionEpoch)
	return append(UnbondingKey, epochBz...)
}

// GetJailedKey creates a key for a specific participant's jailed status
func GetJailedKey(participantAddress string) []byte {
	return append(JailedKey, []byte(participantAddress)...)
}

// ParseUnbondingKey parses the completion epoch and participant address from an unbonding key
func ParseUnbondingKey(key []byte) (completionEpoch uint64, participantAddress string, err error) {
	if len(key) < len(UnbondingKey)+8 {
		return 0, "", fmt.Errorf("invalid unbonding key length")
	}

	epochBz := key[len(UnbondingKey) : len(UnbondingKey)+8]
	completionEpoch = binary.BigEndian.Uint64(epochBz)
	participantAddress = string(key[len(UnbondingKey)+8:])

	return completionEpoch, participantAddress, nil
}
