package mesh

import "encoding/base64"

// sameKey is what two public keys are compared by: base64 ignores the low bits of the last
// character, so one 32-byte key has several spellings and only its bytes identify it
func sameKey(key string) string {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return "s:" + key
	}
	return "b:" + string(raw)
}
