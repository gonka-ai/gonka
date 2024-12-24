package types

import "strconv"

const RandomSeedKeyPrefix = "RandomSeed/value/"

func RandomSeedKey(seed *RandomSeed) []byte {
	var key []byte

	key = append(key, []byte(strconv.FormatInt(seed.BlockHeight, 10))...)
	key = append(key, []byte("/")...)
	key = append(key, []byte(seed.Participant)...)
	key = append(key, []byte("/")...)

	return key
}
