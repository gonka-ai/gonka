package types

import "strconv"

const RandomSeedKeyPrefix = "RandomSeed/value/"

func RandomSeedKey(blockHeight int64, participantAddress string) []byte {
	var key []byte

	key = append(key, []byte(strconv.FormatInt(blockHeight, 10))...)
	key = append(key, []byte("/")...)
	key = append(key, []byte(participantAddress)...)
	key = append(key, []byte("/")...)

	return key
}
