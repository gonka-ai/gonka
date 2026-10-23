package mesh

import "encoding/base64"

// keyBytes is what two public keys are compared by: base64 ignores the low bits of the last
// character, so one 32-byte key has several spellings and only its bytes identify it. A string
// that is not a 32-byte key is no key at all
func keyBytes(key string) ([32]byte, bool) {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return [32]byte{}, false
	}
	return [32]byte(raw), true
}
