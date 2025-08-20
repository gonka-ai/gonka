package types

const (
	// ModuleName defines the module name
	ModuleName = "streamvesting"

	// StoreKey defines the primary module store key
	StoreKey = ModuleName

	// MemStoreKey defines the in-memory store key
	MemStoreKey = "mem_streamvesting"
)

var (
	ParamsKey = []byte("p_streamvesting")

	// VestingScheduleKeyPrefix is the prefix for storing vesting schedules
	VestingScheduleKeyPrefix = []byte("vesting_schedule")
)

func KeyPrefix(p string) []byte {
	return []byte(p)
}

// VestingScheduleKey returns the store key for a participant's vesting schedule
func VestingScheduleKey(participantAddress string) []byte {
	return append(VestingScheduleKeyPrefix, []byte(participantAddress)...)
}
