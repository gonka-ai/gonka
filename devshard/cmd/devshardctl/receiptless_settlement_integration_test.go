//go:build settlementintegration

package main

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/keeper"
	chainTypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestReceiptlessRestartSettlementChainVerifier(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	payload, group, creator := completionRestartFixture(t)
	jsonSettlement, err := buildSettlementJSON(payload)
	require.NoError(t, err)
	params, err := settleParamsFromJSON(jsonSettlement)
	require.NoError(t, err)
	msg := &chainTypes.MsgSettleDevshardEscrow{Settler: creator, EscrowId: 42, StateRoot: params.StateRoot, Nonce: params.Nonce, RestHash: params.RestHash, Fees: params.Fees, StateRootAndProtocolVersion: payload.StateRootAndProtocolVersion}
	for _, hs := range params.HostStats {
		msg.HostStats = append(msg.HostStats, &chainTypes.DevshardSettlementHostStats{SlotId: hs.SlotID, Missed: uint32(hs.Missed), Invalid: uint32(hs.Invalid), Cost: hs.Cost, RequiredValidations: uint32(hs.RequiredValidations), CompletedValidations: uint32(hs.CompletedValidations)})
	}
	for _, sig := range params.Signatures {
		msg.Signatures = append(msg.Signatures, &chainTypes.DevshardSlotSignature{SlotId: sig.SlotID, Signature: sig.Signature})
	}
	slots := make([]string, len(group))
	for i, slot := range group {
		slots[i] = slot.ValidatorAddress
	}
	escrow := chainTypes.DevshardEscrow{Id: 42, Creator: creator, Amount: 1000000, Slots: slots}
	require.NoError(t, keeper.VerifyDevshardSettlement(escrow, msg, &chainTypes.DevshardEscrowParams{MaxNonce: chainTypes.DefaultDevshardMaxNonce}, nil))
	for _, hs := range msg.HostStats {
		require.Zero(t, hs.Cost)
	}
}
