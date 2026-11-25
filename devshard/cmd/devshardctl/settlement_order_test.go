package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	chaintx "common/chain/tx"
	"devshard/state"
	"devshard/types"
)

const settlementBuilds = 64

// Test flow:
//  1. Build the settlement payload of a 3-slot escrow whose fees of 100 leave a remainder of 1 over the slot count.
//  2. Build its settle parameters through `buildSettlementJSON` and `settleParamsFromJSON` 64 times, as the settle path does.
//  3. Assert every build lists the host stats in ascending slot order with each slot's counters, so the chain pays the remainder to the same slot every time.
//  4. Assert every build lists the signatures in ascending slot order, each with its slot's signature.
func TestSettleParamsListHostStatsAndSignaturesInAscendingSlotOrder(t *testing.T) {
	payload := &state.SettlementPayload{
		EscrowID:                    "42",
		StateRootAndProtocolVersion: "dev",
		Nonce:                       9,
		Fees:                        100,
		RestHash:                    []byte("rest-hash"),
		HostStats: map[uint32]*types.HostStats{
			2: {Missed: 1, Cost: 300, RequiredValidations: 2, CompletedValidations: 1},
			0: {Cost: 100, RequiredValidations: 3, CompletedValidations: 3},
			1: {Invalid: 1, Cost: 200},
		},
		Signatures: map[uint32][]byte{2: []byte("sig2"), 0: []byte("sig0"), 1: []byte("sig1")},
	}
	require.NotZero(t, payload.Fees%uint64(len(payload.HostStats)))
	wantHostStats := []chaintx.HostStats{
		{SlotID: 0, Cost: 100, RequiredValidations: 3, CompletedValidations: 3},
		{SlotID: 1, Invalid: 1, Cost: 200},
		{SlotID: 2, Missed: 1, Cost: 300, RequiredValidations: 2, CompletedValidations: 1},
	}
	wantSignatures := []chaintx.SlotSignature{
		{SlotID: 0, Signature: []byte("sig0")},
		{SlotID: 1, Signature: []byte("sig1")},
		{SlotID: 2, Signature: []byte("sig2")},
	}

	for build := range settlementBuilds {
		settlement, err := buildSettlementJSON(payload)
		require.NoError(t, err)
		params, err := settleParamsFromJSON(settlement)
		require.NoError(t, err)
		require.Equal(t, wantHostStats, params.HostStats, "build %d must list host_stats in ascending slot order", build)
		require.Equal(t, wantSignatures, params.Signatures, "build %d must list signatures in ascending slot order", build)
	}
}
