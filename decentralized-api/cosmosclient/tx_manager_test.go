package cosmosclient

import (
	"decentralized-api/apiconfig"
	"decentralized-api/internal/nats/client"
	"decentralized-api/internal/nats/server"
	"encoding/json"
	"fmt"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/google/uuid"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient/mocks"
	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/api/inference/inference"
	testutil "github.com/productscience/inference/testutil/cosmoclient"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"testing"
)

/*
Тесты:
Sucess case:
- создать клиент и включить натс-сервер
- сетапнуть текущий блок в моке
- создать процедуру, которая будет пушить сигнал "новый блок создан" каждую секунду
- отправить 2-3 транзакции с таймаутом + 5 блоков
- ждем
- проверяем, что, что вторая очередь запросила результат
- ждем несколько блоков, убеждаемся, что в стриме ничего нет и транзакции не бли отправлены повторно
*/

func setUpNats(t *testing.T) *nats.Conn {
	// run streams in memory
	testNatsConfig := apiconfig.NatsServerConfig{
		Host:     "localhost",
		Port:     4222,
		TestMode: true,
	}

	srv := server.NewServer(testNatsConfig)
	err := srv.Start()
	assert.NoError(t, err)

	conn, err := client.ConnectToNats(fmt.Sprintf("nats://%v:%v", testNatsConfig.Host, testNatsConfig.Port), "txs-queue-test")
	assert.NoError(t, err)
	return conn
}

/*func TestTxManager_Success(t *testing.T) {
	const (
		network = "cosmos"

		accountName = "cosmosaccount"
		mnemonic    = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
		passphrase  = "testpass"

		blockTimeout = 3

		accountNumber = 1234
		txHash1       = "DAE23C7706D30F7AC5483B2D97288F1C7309D31B25903DB8217F05FAA721EE35"
		txHash2       = "196655D02ABBE76B2400FDE7C7E8571851B9E8785E2E2702BC57A1822914D36F"
	)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	hashes := []string{txHash1, txHash2}
	currentBlockHeight := int64(10)
	highestSequence := int64(0)

	rpc := mocks.NewRPCClient(t)

	cosmosclient := testutil.NewMockClient(t, rpc, network, accountName, mnemonic, passphrase)

	account, err := cosmosclient.AccountRegistry.GetByName(accountName)
	assert.NoError(t, err)

	addr, err := account.Address(network)
	assert.NoError(t, err)

	txs := []sdk.Msg{
		&inference.MsgStartInference{
			Creator:     addr,
			InferenceId: uuid.New().String(),
		},
		&inference.MsgStartInference{
			Creator:     addr,
			InferenceId: uuid.New().String(),
		},
	}

	natsConn := setUpNats(t)

	rpc.EXPECT().Status(gomock.Any()).Return(&coretypes.ResultStatus{
		SyncInfo: coretypes.SyncInfo{
			LatestBlockHeight: currentBlockHeight,
		},
	}, nil)

	accountRetriever := sdkclient.MockAccountRetriever{
		ReturnAccNum: accountNumber,
		ReturnAccSeq: uint64(highestSequence),
	}

	ctx := context.TODO()
	mn, err := NewTxManager(ctx, &cosmosclient, &account, accountRetriever, natsConn, addr, uint64(blockTimeout))
	assert.NoError(t, err)

	for i, tx := range txs {
		hash := bytes.HexBytes{}
		err = hash.Unmarshal([]byte(hashes[i]))
		assert.NoError(t, err)

		rpc.EXPECT().Status(ctx).Return(&coretypes.ResultStatus{
			SyncInfo: coretypes.SyncInfo{
				LatestBlockHeight: currentBlockHeight,
			},
		}, nil)

		address, err := account.Record.GetAddress()
		assert.NoError(t, err)

		fmt.Printf("TEST tx \n")
		signedTxBytes, _, err := buildAndSignTx(
			ctx,
			tx,
			&cosmosclient,
			address,
			accountRetriever,
			highestSequence,
			blockTimeout,
			account.Name,
		)
		fmt.Println("----------------")

		rpc.EXPECT().BroadcastTxSync(context.Background(), cmttypes.Tx(signedTxBytes)).Return(
			&coretypes.ResultBroadcastTx{Code: 0, Hash: hash}, nil)

		assert.NoError(t, mn.PutTxToSend(tx))
		highestSequence++
		break
	}
	assert.NoError(t, mn.SendTxs())

	time.Sleep(15 * time.Second)
	// assert.NoError(t, mn.ObserveTxs())
}
*/

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
