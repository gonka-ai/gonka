package tx_manager

import (
	"encoding/json"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/google/uuid"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient/mocks"
	"github.com/productscience/inference/api/inference/inference"
	testutil "github.com/productscience/inference/testutil/cosmoclient"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestPack_Unpack_Msg(t *testing.T) {
	const (
		network = "cosmos"

		accountName = "cosmosaccount"
		mnemonic    = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
		passphrase  = "testpass"
	)

	rpc := mocks.NewRPCClient(t)
	client := testutil.NewMockClient(t, rpc, network, accountName, mnemonic, passphrase)

	rawTx := &inference.MsgFinishInference{
		Creator:              "some_address",
		InferenceId:          uuid.New().String(),
		ResponseHash:         "some_hash",
		ResponsePayload:      "resp",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           "executor",
	}

	bz, err := client.Context().Codec.MarshalInterfaceJSON(rawTx)
	assert.NoError(t, err)

	timeout := getTimestamp(time.Now().UnixNano(), time.Second)
	b, err := json.Marshal(&txToSend{TxInfo: txInfo{RawTx: bz, Timeout: timeout}})
	assert.NoError(t, err)

	var tx txToSend
	err = json.Unmarshal(b, &tx)
	assert.NoError(t, err)

	var unpackedAny codectypes.Any
	err = client.Context().Codec.UnmarshalJSON(tx.TxInfo.RawTx, &unpackedAny)
	assert.NoError(t, err)

	var unmarshalledRawTx sdk.Msg
	err = client.Context().Codec.UnpackAny(&unpackedAny, &unmarshalledRawTx)
	assert.NoError(t, err)

	result := unmarshalledRawTx.(*types.MsgFinishInference)

	assert.Equal(t, rawTx.InferenceId, result.InferenceId)
	assert.Equal(t, rawTx.Creator, result.Creator)
	assert.Equal(t, rawTx.ResponseHash, result.ResponseHash)
	assert.Equal(t, rawTx.ResponsePayload, result.ResponsePayload)
	assert.Equal(t, rawTx.PromptTokenCount, result.PromptTokenCount)
	assert.Equal(t, rawTx.CompletionTokenCount, result.CompletionTokenCount)
	assert.Equal(t, rawTx.ExecutedBy, result.ExecutedBy)
}

func TestPack_Unpack_Batch(t *testing.T) {
	const (
		network     = "cosmos"
		accountName = "cosmosaccount"
		mnemonic    = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
		passphrase  = "testpass"
	)

	rpc := mocks.NewRPCClient(t)
	client := testutil.NewMockClient(t, rpc, network, accountName, mnemonic, passphrase)
	cdc := client.Context().Codec

	msgs := []*inference.MsgStartInference{
		{Creator: "addr1", InferenceId: uuid.New().String(), Model: "model-a"},
		{Creator: "addr2", InferenceId: uuid.New().String(), Model: "model-b"},
		{Creator: "addr3", InferenceId: uuid.New().String(), Model: "model-c"},
	}

	rawBatch := make([][]byte, len(msgs))
	for i, msg := range msgs {
		bz, err := cdc.MarshalInterfaceJSON(msg)
		assert.NoError(t, err)
		rawBatch[i] = bz
	}

	timeout := getTimestamp(time.Now().UnixNano(), time.Second)
	b, err := json.Marshal(&txToSend{
		TxInfo: txInfo{
			Id:       "batch-id",
			RawBatch: rawBatch,
			Timeout:  timeout,
		},
		Sent:     false,
		Attempts: 0,
	})
	assert.NoError(t, err)

	var tx txToSend
	err = json.Unmarshal(b, &tx)
	assert.NoError(t, err)

	assert.True(t, tx.TxInfo.IsBatch())
	assert.Len(t, tx.TxInfo.RawBatch, 3)

	for i, rawMsg := range tx.TxInfo.RawBatch {
		var unpackedAny codectypes.Any
		err = cdc.UnmarshalJSON(rawMsg, &unpackedAny)
		assert.NoError(t, err)

		var unmarshalledMsg sdk.Msg
		err = cdc.UnpackAny(&unpackedAny, &unmarshalledMsg)
		assert.NoError(t, err)

		result := unmarshalledMsg.(*types.MsgStartInference)
		assert.Equal(t, msgs[i].Creator, result.Creator)
		assert.Equal(t, msgs[i].InferenceId, result.InferenceId)
		assert.Equal(t, msgs[i].Model, result.Model)
	}
}
