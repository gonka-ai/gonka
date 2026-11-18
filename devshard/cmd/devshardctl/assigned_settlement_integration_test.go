//go:build settlementintegration

package main

import (
	"encoding/json"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/keeper"
	chainTypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

// Cross the real runtime hash, gateway JSON, manual wire encoder, generated
// chain decoder and chain verifier, so field-number/conversion drift is caught.
func TestAssignedSettlementRuntimeToChain(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	payload := &state.SettlementPayload{
		EscrowID: "42", StateRootAndProtocolVersion: "v7", Nonce: 100,
		RestHash: make([]byte, 32),
		HostStats: map[uint32]*types.HostStats{
			0: {Assigned: 0},
			1: {Assigned: 3, Missed: 1, Invalid: 1, Cost: 10, Validated: 1, Finished: 2},
			2: {Assigned: 1, Cost: 20, Validated: 1, Finished: 1},
		},
		Signatures: map[uint32][]byte{},
	}
	hash, err := state.ComputeHostStatsHash(payload.HostStats)
	require.NoError(t, err)
	root := state.ComputeStateRootFromRestHash(hash, payload.RestHash, payload.Fees, types.PhaseSettlement, "v7")
	signBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(&types.StateSignatureContent{
		StateRoot: root, EscrowId: "42", Nonce: payload.Nonce,
	})
	require.NoError(t, err)
	slots := make([]string, len(keys))
	for i, key := range keys {
		payload.Signatures[uint32(i)], err = key.Sign(signBytes)
		require.NoError(t, err)
		slots[i] = key.Address()
	}
	wireJSON, err := marshalSettlement(payload)
	require.NoError(t, err)
	var parsed SettlementJSON
	require.NoError(t, json.Unmarshal(wireJSON, &parsed))
	params, err := settleParamsFromJSON(parsed)
	require.NoError(t, err)
	wantAssigned := map[uint32]uint64{0: 0, 1: 3, 2: 1}
	paramsAssigned := make(map[uint32]uint64, len(params.HostStats))
	for _, hs := range params.HostStats {
		paramsAssigned[hs.SlotID] = hs.Assigned
	}
	require.Equal(t, wantAssigned, paramsAssigned, "common tx conversion must retain Assigned for each slot")
	encoded, err := encodeMsgSettleDevshardEscrow(slots[0], parsed)
	require.NoError(t, err)
	var msg chainTypes.MsgSettleDevshardEscrow
	require.NoError(t, msg.Unmarshal(encoded))
	chainAssigned := make(map[uint32]uint64, len(msg.HostStats))
	for _, hs := range msg.HostStats {
		chainAssigned[hs.SlotId] = hs.Assigned
	}
	require.Equal(t, wantAssigned, chainAssigned)
	chainHash, err := keeper.ComputeDevshardHostStatsHash(msg.HostStats)
	require.NoError(t, err)
	require.Equal(t, hash, chainHash)
	escrow := chainTypes.DevshardEscrow{Id: 42, Creator: slots[0], Amount: 10000, Slots: slots}
	limits := &chainTypes.DevshardEscrowParams{MaxNonce: chainTypes.DefaultDevshardMaxNonce}
	require.NoError(t, keeper.VerifyDevshardSettlement(escrow, &msg, limits, nil, nil))
	for _, hs := range msg.HostStats {
		if hs.SlotId == 1 {
			hs.Assigned++
		}
	}
	require.ErrorContains(t, keeper.VerifyDevshardSettlement(escrow, &msg, limits, nil, nil), "state_root mismatch")

}
