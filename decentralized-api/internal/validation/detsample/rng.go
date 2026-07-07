// Package detsample is the Go side of the deterministic-sampling contract
// (see ../DETERMINISTIC_SAMPLING_CONTRACT.md). It reproduces the vLLM reference
// pipeline bit-for-bit so the chain validator can replay an executor's sampling.
//
// This file implements the portable, dependency-free half: the SHA256
// counter-mode RNG and the integer categorical sampler (contract §5, §6). Both
// are exact integer/crypto operations and require no decimal library. The
// decimal pipeline (logprobs -> integer weights, contract §4) lands separately.
package detsample

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Sha256CounterRNG is the portable RNG from contract §5:
//
//	u64 = big-endian uint64 of first 8 bytes of SHA256(seedBytes || be_u64(counter))
//
// counter starts at 0 and advances by one per draw. Identical sequence to the
// Python Sha256CounterRNG for the same seed string.
type Sha256CounterRNG struct {
	seed    []byte
	counter uint64
}

// NewFromSeedString creates an RNG seeded with the UTF-8 bytes of s.
func NewFromSeedString(s string) *Sha256CounterRNG {
	return &Sha256CounterRNG{seed: []byte(s), counter: 0}
}

// NextU64 returns the next pseudorandom uint64 and advances the counter.
func (r *Sha256CounterRNG) NextU64() uint64 {
	buf := make([]byte, len(r.seed)+8)
	copy(buf, r.seed)
	binary.BigEndian.PutUint64(buf[len(r.seed):], r.counter)
	sum := sha256.Sum256(buf)
	r.counter++
	return binary.BigEndian.Uint64(sum[:8])
}

// IterU64 returns count draws from a fresh RNG seeded with seed. Mirrors the
// Python iter_u64 helper used for the reference vector.
func IterU64(seed string, count int) []uint64 {
	r := NewFromSeedString(seed)
	out := make([]uint64, count)
	for i := 0; i < count; i++ {
		out[i] = r.NextU64()
	}
	return out
}

// Uint64Below returns an unbiased draw in [0, n) via rejection sampling
// (contract §6). n must be > 0.
func Uint64Below(r *Sha256CounterRNG, n uint64) (uint64, error) {
	if n == 0 {
		return 0, fmt.Errorf("detsample: n must be > 0")
	}
	// Python: limit = 2^64 - (2^64 % n). When n divides 2^64 (e.g. the 2^16
	// weight scale), 2^64 % n == 0 and limit == 2^64, so every draw is accepted.
	// 2^64 % n, computed in uint64: 2^64 == MaxUint64 + 1.
	twoTo64ModN := (^uint64(0)%n + 1) % n
	if twoTo64ModN == 0 {
		// n | 2^64: accept any draw.
		return r.NextU64() % n, nil
	}
	limit := -twoTo64ModN // wraps to 2^64 - twoTo64ModN
	for {
		x := r.NextU64()
		if x < limit {
			return x % n, nil
		}
	}
}

// SampleCategoricalWeights draws an index in [0, len(weights)) with probability
// proportional to the non-negative integer weights (contract §6). The RNG state
// advances. Returns an error on a negative weight or a non-positive total (no
// silent fallback — a zero total in zero-tolerance validation is an upstream
// bug).
func SampleCategoricalWeights(weights []int64, r *Sha256CounterRNG) (int, error) {
	if len(weights) == 0 {
		return 0, fmt.Errorf("detsample: weights is empty")
	}
	var total uint64
	for i, w := range weights {
		if w < 0 {
			return 0, fmt.Errorf("detsample: negative weight at index %d: %d", i, w)
		}
		total += uint64(w)
	}
	if total == 0 {
		return 0, fmt.Errorf("detsample: weights sum to zero")
	}

	rv, err := Uint64Below(r, total)
	if err != nil {
		return 0, err
	}
	var cum uint64
	for i, w := range weights {
		cum += uint64(w)
		if rv < cum {
			return i, nil
		}
	}
	// Unreachable when total == sum(weights); kept for completeness.
	return len(weights) - 1, nil
}
