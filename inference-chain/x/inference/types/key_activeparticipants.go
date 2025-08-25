package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"strconv"
)

const ActiveParticipantsKeyPrefixV1 = "ActiveParticipants/"
const ActiveParticipantsKeyPrefix = "ActiveParticipants/value/"
const ActiveParticipantsProofKeyPrefix = "ActiveParticipantsProof/"

func ActiveParticipantsFullKeyV1(epochGroupId uint64) []byte {
	var key []byte

	key = append(key, []byte(ActiveParticipantsKeyPrefixV1)...)
	key = append(key, []byte(strconv.FormatUint(epochGroupId, 10))...)
	key = append(key, []byte("/value/")...)

	return key
}

func ActiveParticipantsFullKey(epoch uint64) []byte {
	var key []byte

	key = append(key, []byte(ActiveParticipantsKeyPrefix)...)
	key = append(key, sdk.Uint64ToBigEndian(epoch)...)
	key = append(key, []byte("/")...)

	return key
}

func ActiveParticipantsProofFullKey(blockHeight uint64) []byte {
	var key []byte
	key = append(key, []byte(ActiveParticipantsProofKeyPrefix)...)
	key = append(key, sdk.Uint64ToBigEndian(blockHeight)...)
	key = append(key, []byte("/")...)

	return key
}
