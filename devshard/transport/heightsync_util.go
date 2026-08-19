package transport

import (
	"encoding/hex"
	"strings"
)

func heightSyncHashPrefix(hexStr string) string {
	h := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(hexStr), "0x"), "0X")
	if len(h) > 16 {
		return h[:16]
	}
	return h
}

func decodeMainnetBlockHashHex(s string) ([]byte, error) {
	h := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "0x"), "0X")
	if len(h)%2 == 1 {
		h = "0" + h
	}
	return hex.DecodeString(h)
}
