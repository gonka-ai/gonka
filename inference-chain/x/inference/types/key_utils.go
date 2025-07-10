package types

import sdk "github.com/cosmos/cosmos-sdk/types"

func StringKey(id string) []byte {
	var key []byte

	idBytes := []byte(id)
	key = append(key, idBytes...)
	key = append(key, []byte("/")...)

	return key
}

func uintKey(id uint64) []byte {
	var key []byte
	idBytes := sdk.Uint64ToBigEndian(id)
	key = append(key, idBytes...)
	key = append(key, []byte("/")...)

	return key
}

func stringsKey(ids ...string) []byte {
	var key []byte
	for _, id := range ids {
		key = append(key, id...)
		key = append(key, '/')
	}
	return key
}
