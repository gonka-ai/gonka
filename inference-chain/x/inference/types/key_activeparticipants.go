package types

import "strconv"

func ActiveParticipantsFullKey(epoch uint64) []byte {
	var key []byte

	key = append(key, []byte("ActiveParticipants/")...)
	key = append(key, []byte(strconv.FormatUint(epoch, 10))...)
	key = append(key, []byte("/value/")...)

	return key
}
