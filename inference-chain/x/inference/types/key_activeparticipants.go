package types

import (
	"strconv"
)

const ActiveParticipantsKeyPrefix = "ActiveParticipants/"

// TODO [CHAIN-RELAUNCH]:
//  1. Start using EpochId as the key
//  2. Marshall it to bigendian for sortability
func ActiveParticipantsFullKey(epoch uint64) []byte {
	var key []byte

	key = append(key, []byte(ActiveParticipantsKeyPrefix)...)
	key = append(key, []byte(strconv.FormatUint(epoch, 10))...)
	key = append(key, []byte("/value/")...)

	return key
}
