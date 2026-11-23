package main

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

	chaintx "common/chain/tx"
	"devshard/gatewayprotocol/settlement"
	"devshard/gatewayprotocol/settlementwire"
	"devshard/signing"
	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/txexec"
	"devshard/testenv/mockchain/txledger"

	"github.com/cosmos/gogoproto/proto"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func settleWireMessageFull() settlement.Message {
	return settlement.Message{
		EscrowID:  123,
		StateRoot: []byte("fixed-state-root-32-bytes-long!"),
		Nonce:     456,
		RestHash:  []byte("fixed-rest-hash-bytes"),
		HostStats: []settlement.HostStat{
			{SlotID: 1, Missed: 2, Invalid: 3, Cost: 400, RequiredValidations: 5, CompletedValidations: 6},
			{SlotID: 7, Missed: 8, Invalid: 9, Cost: 1000, RequiredValidations: 11, CompletedValidations: 12},
		},
		SlotSignatures: []settlement.SlotSignature{
			{SlotID: 1, Signature: []byte("fixed-signature-one")},
			{SlotID: 7, Signature: []byte("fixed-signature-two")},
		},
		Fees:    789,
		Version: "v1.2.3",
	}
}

func settleWireMessageEmptyOptional() settlement.Message {
	return settlement.Message{
		EscrowID:  124,
		StateRoot: []byte("fixed-state-root-32-bytes-long!"),
		Nonce:     456,
		RestHash:  []byte("fixed-rest-hash-bytes"),
		Fees:      789,
		Version:   "v1.2.3",
	}
}

func settleWireMessageZeroScalars() settlement.Message {
	return settlement.Message{
		EscrowID:  125,
		StateRoot: []byte("fixed-state-root-32-bytes-long!"),
		Nonce:     456,
		RestHash:  []byte("fixed-rest-hash-bytes"),
		HostStats: []settlement.HostStat{
			{SlotID: 0},
			{SlotID: 1, Cost: 400},
			{SlotID: 2, Missed: 3, Invalid: 1, Cost: 555, RequiredValidations: 10, CompletedValidations: 9},
		},
		SlotSignatures: []settlement.SlotSignature{
			{SlotID: 0, Signature: []byte("fixed-signature-zero")},
			{SlotID: 1, Signature: []byte("fixed-signature-one")},
			{SlotID: 2, Signature: []byte("fixed-signature-two")},
		},
		Fees:    789,
		Version: "v1.2.3",
	}
}

func settlementJSONFromMessage(message settlement.Message) SettlementJSON {
	hostStats := make([]HostStatsJSON, 0, len(message.HostStats))
	for _, hostStat := range message.HostStats {
		hostStats = append(hostStats, HostStatsJSON(hostStat))
	}
	signatures := make([]SlotSignatureJSON, 0, len(message.SlotSignatures))
	for _, slotSignature := range message.SlotSignatures {
		signatures = append(signatures, SlotSignatureJSON{SlotID: slotSignature.SlotID, Signature: base64.StdEncoding.EncodeToString(slotSignature.Signature)})
	}
	return SettlementJSON{
		EscrowID:                    strconv.FormatUint(message.EscrowID, 10),
		StateRootAndProtocolVersion: message.Version,
		StateRoot:                   base64.StdEncoding.EncodeToString(message.StateRoot),
		Nonce:                       message.Nonce,
		Fees:                        message.Fees,
		RestHash:                    base64.StdEncoding.EncodeToString(message.RestHash),
		HostStats:                   hostStats,
		Signatures:                  signatures,
	}
}

func decodeSettleMessage(t *testing.T, messageBytes []byte) *inferencetypes.MsgSettleDevshardEscrow {
	t.Helper()
	var message inferencetypes.MsgSettleDevshardEscrow
	require.NoError(t, proto.Unmarshal(messageBytes, &message))
	return &message
}

func decodeBroadcastSettleMessage(t *testing.T, txBytes []byte) *inferencetypes.MsgSettleDevshardEscrow {
	t.Helper()
	messages, err := txexec.DecodeTxMessages(txBytes)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.NotNil(t, messages[0].Settle, "broadcast tx carries no MsgSettleDevshardEscrow")
	return messages[0].Settle
}

// Test flow:
//  1. Start the in-process mock chain with a tx ledger and a `common/chain/tx` manager over it, signing with a fixed key.
//  2. For the full message, the one with empty host stats and signatures, and the one whose slot 0 and counters are zero, put its escrow on the mock chain.
//  3. Send the message as devshardctl does: settlement JSON, `settleParamsFromJSON`, `Manager.SettleDevshardEscrow`.
//  4. Read the broadcast tx from the ledger and decode TxRaw, TxBody and the single Any into `MsgSettleDevshardEscrow`.
//  5. Marshal the same message with `settlementwire.Marshal` for the same settler, decode it, and assert both decoded messages are equal.
func TestSettleDevshardEscrowDecodesToSettlementWireMessage(t *testing.T) {
	chainStore := seed.Defaults()
	rpcService, err := rpcface.NewService(chainStore, rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)
	ledger := txledger.New()
	connection, cleanup := startGRPCWithTx(t, chainStore, rpcService, ledger)
	t.Cleanup(cleanup)

	txManager, err := chaintx.New(connection, chaintx.Config{
		ChainID:      "gonka-test",
		FeeAmount:    123,
		GasLimit:     456,
		PollInterval: time.Millisecond,
		PollTimeout:  2 * time.Second,
	})
	require.NoError(t, err)
	signer, err := signing.SignerFromHex(strings.Repeat("11", 32))
	require.NoError(t, err)

	cases := []struct {
		name    string
		message settlement.Message
	}{
		{"full", settleWireMessageFull()},
		{"empty_optional", settleWireMessageEmptyOptional()},
		{"zero_scalars", settleWireMessageZeroScalars()},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			chainStore.PutEscrow(&inferencetypes.DevshardEscrow{Id: testCase.message.EscrowID})
			params, err := settleParamsFromJSON(settlementJSONFromMessage(testCase.message))
			require.NoError(t, err)
			result, err := txManager.SettleDevshardEscrow(t.Context(), signer, params)
			require.NoError(t, err)
			storedTx, found := ledger.Get(result.TxHash)
			require.True(t, found, "broadcast tx %s not in the ledger", result.TxHash)
			broadcastMessage := decodeBroadcastSettleMessage(t, storedTx.Raw)

			wireBytes, err := settlementwire.Marshal(signer.Address(), testCase.message)
			require.NoError(t, err)
			require.Equal(t, decodeSettleMessage(t, wireBytes), broadcastMessage)
		})
	}
}
