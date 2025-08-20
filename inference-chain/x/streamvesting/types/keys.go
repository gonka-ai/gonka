package types

import "cosmossdk.io/collections"

const (
	// ModuleName defines the module name
	ModuleName = "streamvesting"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_streamvesting"
)

var (
	// ParamsKey is the collections prefix for module params (Item)
	ParamsKey = collections.NewPrefix(0)

	// VestingScheduleKey is the collections prefix for storing vesting schedules (Map)
	VestingScheduleKey = collections.NewPrefix(1)
)
