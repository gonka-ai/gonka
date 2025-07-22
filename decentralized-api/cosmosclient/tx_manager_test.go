package cosmosclient

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/internal/nats/client"
	"decentralized-api/internal/nats/server"
	"encoding/json"
	"fmt"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/google/uuid"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosaccount"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient/mocks"
	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/api/inference/inference"
	testutil "github.com/productscience/inference/testutil/cosmoclient"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"log/slog"
	"testing"
	"time"
)

func setUpNats(t *testing.T) *nats.Conn {
	// run streams in memory
	testNatsConfig := apiconfig.NatsServerConfig{
		Host: "localhost",
		Port: 4123,
		//TestMode:   true,
		StorageDir: "./nats/test",
	}

	srv := server.NewServer(testNatsConfig)
	err := srv.Start()
	assert.NoError(t, err)

	conn, err := client.ConnectToNats(testNatsConfig.Host, testNatsConfig.Port, "txs-queue-test")
	assert.NoError(t, err)
	js, err := conn.JetStream()
	_ = js.DeleteConsumer(server.TxsToObserveStream, txObserverConsumer)
	return conn
}

func TestTxManager_Success(t *testing.T) {
	const (
		accountName      = "join1"
		notExistingHash1 = "8237097132E1E813B6F6DC9A571A049954F33E353C941617B929CB10968F32FD"
		notExistingHash2 = "4D66F342571F81D5C39404B5B0241AECBE585CDEADFD482AF5CF9F01DA4FF78E"
	)

	slog.SetLogLoggerLevel(slog.LevelDebug)

	chainNode := apiconfig.ChainNodeConfig{
		Url:            "http://localhost:8101",
		AccountName:    accountName,
		KeyringBackend: "test",
		KeyringDir:     "/home/zb/jobs/productai/code/inference-ignite/local-test-net/prod-local/join1",
		SeedApiUrl:     "http://localhost:9000",
	}

	ctx := context.Background()
	natsConn := setUpNats(t)

	cosmoclient, err := cosmosclient.New(
		ctx,
		cosmosclient.WithAddressPrefix("gonka"),
		cosmosclient.WithNodeAddress(chainNode.Url),
		cosmosclient.WithKeyringBackend(cosmosaccount.KeyringBackend(chainNode.KeyringBackend)),
		cosmosclient.WithKeyringDir(chainNode.KeyringDir),
		cosmosclient.WithGasPrices("0icoin"),
		cosmosclient.WithFees("0icoin"),
		cosmosclient.WithGas("auto"),
		cosmosclient.WithGasAdjustment(5),
	)
	assert.NoError(t, err)

	account, err := cosmoclient.AccountRegistry.GetByName(chainNode.AccountName)
	assert.NoError(t, err)

	addr, err := account.Address("gonka")
	assert.NoError(t, err)

	fmt.Println(addr)
	mn, err := NewTxManager(ctx, &cosmoclient, &account, authtypes.AccountRetriever{}, natsConn, addr, 5)
	assert.NoError(t, err)

	err = mn.SendTxs()
	assert.NoError(t, err)

	txsHashs := make([]string, 0)
	for i := 0; i < 3; i++ {
		txResp, err := mn.SendTransactionAsyncWithRetry(
			&inference.MsgFinishInference{
				Creator:              addr,
				InferenceId:          uuid.NewString(),
				PromptTokenCount:     10,
				CompletionTokenCount: 20,
				ExecutedBy:           addr,
			})
		assert.NoError(t, err)
		fmt.Printf("sent tx with hash: %v\n", txResp.TxHash)
		txsHashs = append(txsHashs, txResp.TxHash)
	}

	blockHeight, err := cosmoclient.LatestBlockHeight(ctx)
	assert.NoError(t, err)

	fmt.Printf("block height: %v\n", blockHeight)

	notExistingTxUuid1 := uuid.NewString()
	fmt.Printf("notExisstingTxUuid1: %v\n", notExistingTxUuid1)

	err = mn.putTxToObserve(notExistingTxUuid1, &inference.MsgFinishInference{
		Creator:              addr,
		InferenceId:          uuid.NewString(),
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           addr,
	}, notExistingHash1, uint64(mn.highestSequence+1), uint64(blockHeight+1))
	assert.NoError(t, err)

	notExistingTxUuid2 := uuid.NewString()
	fmt.Printf("notExistingTxUuid2: %v\n", notExistingTxUuid2)

	err = mn.putTxToObserve(notExistingTxUuid2, &inference.MsgFinishInference{
		Creator:              addr,
		InferenceId:          uuid.NewString(),
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           addr,
	}, notExistingHash2, uint64(mn.highestSequence+1), uint64(blockHeight+1))
	assert.NoError(t, err)

	err = mn.ObserveTxs()
	assert.NoError(t, err)

	/*for i := 0; i < 2; i++ {
		txResp, err := mn.SendTransactionAsyncWithRetry(
			&inference.MsgFinishInference{
				Creator:              addr,
				InferenceId:          uuid.NewString(),
				PromptTokenCount:     10,
				CompletionTokenCount: 20,
				ExecutedBy:           addr,
			})
		assert.NoError(t, err)
		fmt.Printf("sent tx with hash: %v\n", txResp.TxHash)
		txsHashs = append(txsHashs, txResp.TxHash)
	}

	for _, hash := range txsHashs {
		ctxWithTimeput, cancel := context.WithTimeout(ctx, time.Second*30)
		defer cancel()
		txResp, err := cosmoclient.WaitForTx(ctxWithTimeput, hash)
		assert.NoError(t, err)
		fmt.Printf("test tx found: %v\n", txResp.Hash)
	}*/

	time.Sleep(time.Second * 60 * 10)
}

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

	b, err := json.Marshal(&txToSend{TxInfo: txInfo{RawTx: bz}})
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
