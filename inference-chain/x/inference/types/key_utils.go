package types

import "strconv"

func stringKey(id string) []byte {
	var key []byte

	idBytes := []byte(id)
	key = append(key, idBytes...)
	key = append(key, []byte("/")...)

	return key
}

func stringsKey(id1, id2 string) []byte {
	var key []byte

	id1Bytes := []byte(id1)
	key = append(key, id1Bytes...)
	key = append(key, []byte("/")...)

	id2Bytes := []byte(id2)
	key = append(key, id2Bytes...)
	key = append(key, []byte("/")...)

	return key
}

func intKey(id int64) []byte {
	idStr := strconv.FormatInt(id, 10)
	return stringKey(idStr)
}

func uintKey(id uint64) []byte {
	idStr := strconv.FormatUint(id, 10)
	return stringKey(idStr)
}
