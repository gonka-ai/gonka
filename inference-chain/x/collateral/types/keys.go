package types

import (
	"cosmossdk.io/collections"
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
	ParamsKey = collections.NewPrefix(0)

	// CurrentEpochKey is the key to store the current epoch index for the collateral module
	CurrentEpochKey = collections.NewPrefix(1)

	// CollateralKey is the prefix to store collateral for participants
	CollateralKey = collections.NewPrefix(2)

	// UnbondingKey is the legacy prefix for unbonding entries (raw store)
	// Format: unbonding/{completionEpoch}/{participantAddress}
	UnbondingKey = collections.NewPrefix(3)

	// New collections prefixes for UnbondingCollateral IndexedMap and its secondary index
	UnbondingCollPrefix               = collections.NewPrefix(4)
	UnbondingByParticipantIndexPrefix = collections.NewPrefix(5)

	// JailedKey is the prefix for jailed participant entries
	JailedKey = collections.NewPrefix(6)

	// SlashedInEpochKey is a keyset to record that a participant was slashed for a given reason in a given epoch
	// Key: (epoch:uint64, participant:AccAddress, reason:string)
	SlashedInEpochKey = collections.NewPrefix(7)
)
