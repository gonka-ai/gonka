package calculations

import (
	"crypto/sha256"
	"encoding/binary"
)

// SeedFromBytes derives a deterministic int64 seed from arbitrary bytes using SHA-256.
// The first 8 bytes of the hash (big-endian) are used as the seed. This is the canonical
// way to derive a seed for math/rand.NewSource(seed) so that all participant selection,
// weighted sampling, and shuffle logic use the same random math consistently.
func SeedFromBytes(b []byte) int64 {
	sum := sha256.Sum256(b)
	return int64(binary.BigEndian.Uint64(sum[:8]))
}
