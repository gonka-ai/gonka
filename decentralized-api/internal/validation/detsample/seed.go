package detsample

// Chain-bound seed derivation — Go side of gonka-ai/vllm#56. Mirrors the Python
// derive_chain_bound_seed byte-for-byte so the executor and the chain validator
// derive the same RNG seed. The seed binds Stage-1 replay to chain provenance
// (inference_id), which request-controlled prompt material cannot provide.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"unicode/utf8"
)

const seedDomainTag = "gonka-deterministic-sampling-v1"

const (
	idMinOrd          = 0x21
	idMaxOrd          = 0x7E
	maxInferenceIDLen = 256
)

// DeriveChainBoundSeed returns the lowercase SHA256 hex digest to seed a
// Sha256CounterRNG. userSeed is int64 (vLLM's seed is int-only; the int64 type
// enforces the pinned signed-64-bit range). inferenceID must be printable ASCII
// (0x21..0x7E), non-empty, and at most maxInferenceIDLen characters. It fails
// closed on any other input rather than falling back to request-controlled
// material.
func DeriveChainBoundSeed(userSeed int64, inferenceID string) (string, error) {
	if inferenceID == "" {
		return "", fmt.Errorf("detsample: inference_id must be non-empty")
	}
	if utf8.RuneCountInString(inferenceID) > maxInferenceIDLen {
		return "", fmt.Errorf(
			"detsample: inference_id too long (>%d characters)", maxInferenceIDLen)
	}
	// Language-invariant charset (contract): printable ASCII only, so the
	// accept/reject boundary is identical to the Python executor.
	for _, ch := range inferenceID {
		if ch < idMinOrd || ch > idMaxOrd {
			return "", fmt.Errorf(
				"detsample: inference_id must be printable ASCII (0x21..0x7E); "+
					"found U+%04X", ch)
		}
	}

	seedBytes := []byte(strconv.FormatInt(userSeed, 10))
	idBytes := []byte(inferenceID)

	// Byte-length-prefixed, domain-separated framing (prefixes count bytes).
	material := make([]byte, 0, len(seedDomainTag)+len(seedBytes)+len(idBytes)+64)
	material = append(material, seedDomainTag...)
	material = append(material, "\nuser_seed_len="...)
	material = append(material, strconv.Itoa(len(seedBytes))...)
	material = append(material, '\n')
	material = append(material, seedBytes...)
	material = append(material, "\ninference_id_len="...)
	material = append(material, strconv.Itoa(len(idBytes))...)
	material = append(material, '\n')
	material = append(material, idBytes...)

	sum := sha256.Sum256(material)
	return hex.EncodeToString(sum[:]), nil
}
