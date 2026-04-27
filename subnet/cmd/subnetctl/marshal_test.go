package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"subnet/state"
	"subnet/types"
)

// TestMarshalSettlement_RoundTrip verifies that the settlement JSON produced by
// marshalSettlement can be parsed and re-verified with the chain-side logic:
//
//	sha256(recomputed_host_stats_hash || rest_hash || 0x02) == state_root
//
// This test catches the bug that causes state_root mismatch on-chain.
func TestMarshalSettlement_RoundTrip(t *testing.T) {
	// Build a realistic settlement payload.
	hostStats := map[uint32]*types.HostStats{
		0: {Missed: 0, Invalid: 0, Cost: 150, RequiredValidations: 2, CompletedValidations: 2},
		1: {Missed: 1, Invalid: 0, Cost: 100, RequiredValidations: 2, CompletedValidations: 1},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {
			Status: types.StatusFinished, ExecutorSlot: 0, Model: "llama",
			PromptHash: []byte("abcdefghijklmnopqrstuvwxyz012345"), // 32 bytes
			InputLength: 100, MaxTokens: 50, ReservedCost: 150,
		},
	}

	restHash, err := state.ComputeRestHash(9850, inferences, nil)
	require.NoError(t, err)

	payload := &state.SettlementPayload{
		EscrowID:   "42",
		Nonce:      5,
		RestHash:   restHash,
		HostStats:  hostStats,
		Signatures: map[uint32][]byte{0: []byte("sig0"), 1: []byte("sig1")},
	}

	// Step 1: marshal (subnetctl side)
	data, err := marshalSettlement(payload)
	require.NoError(t, err)
	t.Logf("Settlement JSON:\n%s", string(data))

	// Step 2: parse (simulates Kotlin or chain CLI)
	var parsed SettlementJSON
	require.NoError(t, json.Unmarshal(data, &parsed))

	// Step 3: verify state_root consistency (chain-side logic)
	stateRoot, err := base64.StdEncoding.DecodeString(parsed.StateRoot)
	require.NoError(t, err)
	parsedRestHash, err := base64.StdEncoding.DecodeString(parsed.RestHash)
	require.NoError(t, err)

	// Recompute host_stats_hash from parsed host_stats
	parsedHostStats := make(map[uint32]*types.HostStats, len(parsed.HostStats))
	for _, hs := range parsed.HostStats {
		parsedHostStats[hs.SlotID] = &types.HostStats{
			Missed: hs.Missed, Invalid: hs.Invalid, Cost: hs.Cost,
			RequiredValidations: hs.RequiredValidations, CompletedValidations: hs.CompletedValidations,
		}
	}
	hsHash, err := state.ComputeHostStatsHash(parsedHostStats)
	require.NoError(t, err)

	h := sha256.New()
	h.Write(hsHash)
	h.Write(parsedRestHash)
	h.Write([]byte{0x02})
	expectedRoot := h.Sum(nil)

	require.Equal(t, expectedRoot, stateRoot,
		"state_root mismatch: sha256(host_stats_hash || rest_hash || 0x02) != state_root")
}

// TestMarshalSettlement_KotlinReserialize simulates the Kotlin Gson round-trip:
// 1. Parse with cosmosJson (LOWER_CASE_WITH_UNDERSCORES, LongDeserializer)
// 2. Re-serialize with plain Gson()
// In Go, we simulate this by: unmarshal -> marshal (plain) -> unmarshal again.
func TestMarshalSettlement_KotlinReserialize(t *testing.T) {
	hostStats := map[uint32]*types.HostStats{
		0: {Cost: 150, CompletedValidations: 2, RequiredValidations: 2},
		1: {Cost: 100, CompletedValidations: 1, RequiredValidations: 2, Missed: 1},
	}
	inferences := map[uint64]*types.InferenceRecord{
		1: {
			Status: types.StatusFinished, ExecutorSlot: 0,
			PromptHash: []byte("abcdefghijklmnopqrstuvwxyz012345"),
		},
	}

	restHash, err := state.ComputeRestHash(9850, inferences, nil)
	require.NoError(t, err)

	payload := &state.SettlementPayload{
		EscrowID: "42", Nonce: 5, RestHash: restHash,
		HostStats:  hostStats,
		Signatures: map[uint32][]byte{0: []byte("sig0")},
	}

	original, err := marshalSettlement(payload)
	require.NoError(t, err)

	// Simulate Kotlin: parse and re-serialize (plain JSON, no custom serializers)
	var parsed SettlementJSON
	require.NoError(t, json.Unmarshal(original, &parsed))
	reserialized, err := json.Marshal(parsed)
	require.NoError(t, err)

	t.Logf("Original:\n%s", string(original))
	t.Logf("Reserialized:\n%s", string(reserialized))

	// Parse the reserialized JSON and verify state_root
	var reparsed SettlementJSON
	require.NoError(t, json.Unmarshal(reserialized, &reparsed))

	stateRoot, err := base64.StdEncoding.DecodeString(reparsed.StateRoot)
	require.NoError(t, err)
	parsedRestHash, err := base64.StdEncoding.DecodeString(reparsed.RestHash)
	require.NoError(t, err)

	parsedHostStats := make(map[uint32]*types.HostStats, len(reparsed.HostStats))
	for _, hs := range reparsed.HostStats {
		parsedHostStats[hs.SlotID] = &types.HostStats{
			Missed: hs.Missed, Invalid: hs.Invalid, Cost: hs.Cost,
			RequiredValidations: hs.RequiredValidations, CompletedValidations: hs.CompletedValidations,
		}
	}
	hsHash, err := state.ComputeHostStatsHash(parsedHostStats)
	require.NoError(t, err)

	h := sha256.New()
	h.Write(hsHash)
	h.Write(parsedRestHash)
	h.Write([]byte{0x02})
	expectedRoot := h.Sum(nil)

	require.Equal(t, expectedRoot, stateRoot,
		"state_root mismatch after Kotlin-style re-serialization")
}
