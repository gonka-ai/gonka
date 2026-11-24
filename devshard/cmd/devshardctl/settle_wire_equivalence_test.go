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

	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
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

func settleWireMessageValidatedFinished() settlement.Message {
	return settlement.Message{
		EscrowID:  126,
		StateRoot: []byte("fixed-state-root-32-bytes-long!"),
		Nonce:     456,
		RestHash:  []byte("fixed-rest-hash-bytes"),
		HostStats: []settlement.HostStat{
			{SlotID: 1, Missed: 2, Invalid: 3, Cost: 400, RequiredValidations: 5, CompletedValidations: 6, Validated: 4, Finished: 9},
			{SlotID: 7, Missed: 8, Invalid: 9, Cost: 1000, RequiredValidations: 11, CompletedValidations: 12},
			{SlotID: 8, Missed: 1, Invalid: 1, Cost: 50, RequiredValidations: 2, CompletedValidations: 2, Validated: 3},
			{SlotID: 9, Missed: 1, Invalid: 1, Cost: 60, RequiredValidations: 2, CompletedValidations: 2, Finished: 300},
		},
		SlotSignatures: []settlement.SlotSignature{
			{SlotID: 1, Signature: []byte("fixed-signature-one")},
			{SlotID: 7, Signature: []byte("fixed-signature-two")},
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

func broadcastSettleMessageBytes(t *testing.T, txBytes []byte) []byte {
	t.Helper()
	var raw txtypes.TxRaw
	require.NoError(t, proto.Unmarshal(txBytes, &raw))
	var body txtypes.TxBody
	require.NoError(t, proto.Unmarshal(raw.BodyBytes, &body))
	require.Len(t, body.Messages, 1)
	require.Equal(t, txexec.SettleEscrowMsgTypeURL, body.Messages[0].TypeUrl)
	return body.Messages[0].Value
}

// Test flow:
//  1. Start the in-process mock chain with a tx ledger and a `common/chain/tx` manager over it, signing with a fixed key.
//  2. For the full message, the one with empty host stats and signatures, the one whose slot 0 and counters are zero, and the one whose host stats carry non-zero and zero validated and finished counts, put its escrow on the mock chain.
//  3. Send the message as devshardctl does: settlement JSON, `settleParamsFromJSON`, `Manager.SettleDevshardEscrow`.
//  4. Read the broadcast tx from the ledger and take the bytes of the single `MsgSettleDevshardEscrow` in its body.
//  5. Marshal the same message with `settlementwire.Marshal` for the same settler and assert the decoded messages are equal.
//  6. For every message with no zero counter of fields 1 to 6 or slot id and with fees and a version set, assert the two byte strings are equal, so fields the generated type does not know cannot differ unseen.
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
		name           string
		message        settlement.Message
		wantEqualBytes bool
	}{
		{"full", settleWireMessageFull(), true},
		{"empty_optional", settleWireMessageEmptyOptional(), true},
		{"zero_scalars", settleWireMessageZeroScalars(), false},
		{"validated_finished", settleWireMessageValidatedFinished(), true},
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
			broadcastBytes := broadcastSettleMessageBytes(t, storedTx.Raw)

			wireBytes, err := settlementwire.Marshal(signer.Address(), testCase.message)
			require.NoError(t, err)
			require.Equal(t, decodeSettleMessage(t, wireBytes), decodeSettleMessage(t, broadcastBytes))
			if testCase.wantEqualBytes {
				require.Equal(t, broadcastBytes, wireBytes)
			}
		})
	}
}
