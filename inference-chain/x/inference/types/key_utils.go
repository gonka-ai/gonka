package types

import "strconv"

func StringKey(id string) []byte {
	var key []byte

	idBytes := []byte(id)
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

func intKey(id int64) []byte {
	idStr := strconv.FormatInt(id, 10)
	return StringKey(idStr)
}

func uintKey(id uint64) []byte {
	idStr := strconv.FormatUint(id, 10)
	return StringKey(idStr)
}
