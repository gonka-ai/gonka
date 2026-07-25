package rpcface

import (
	"crypto/sha256"
	"encoding/binary"
	"time"
)

// IntervalForHeight returns a deterministic block interval with symmetric jitter,
// matching devshard/chainoracle/blocks/observer.Mock (heightsyncd reference).
func IntervalForHeight(base, delta time.Duration, seed, height int64) time.Duration {
	if delta <= 0 {
		return base
	}
	deltaNs := delta.Nanoseconds()
	if deltaNs <= 0 {
		return base
	}

	var seedBuf [16]byte
	binary.BigEndian.PutUint64(seedBuf[:8], uint64(seed)^0x9e3779b97f4a7c15)
	binary.BigEndian.PutUint64(seedBuf[8:], uint64(height))
	state := sha256.Sum256(append([]byte("mock-interval-jitter:"), seedBuf[:]...))

	span := uint64(2*deltaNs + 1)
	j := int64(binary.BigEndian.Uint64(state[:8])%span) - deltaNs
	d := base + time.Duration(j)
	if d <= 0 {
		return time.Millisecond
	}
	return d
}
