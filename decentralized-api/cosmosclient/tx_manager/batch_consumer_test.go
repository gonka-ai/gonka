package tx_manager

import (
	"context"
	"sync"
	"testing"
	"time"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/golang/protobuf/proto"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient/mocks"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	testutil "github.com/productscience/inference/testutil/cosmoclient"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"decentralized-api/apiconfig"
)

type mockTxManager struct {
	sendBatchCalls [][]sdk.Msg
	mu             sync.Mutex
}

func (m *mockTxManager) SendBatchAsyncWithRetry(msgs []sdk.Msg, deadlineBlock ...int64) error {
	m.mu.Lock()
	m.sendBatchCalls = append(m.sendBatchCalls, msgs)
	m.mu.Unlock()
	return nil
}

func (m *mockTxManager) getBatchCalls() [][]sdk.Msg {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sendBatchCalls
}

func (m *mockTxManager) SendTransactionAsyncWithRetry(sdk.Msg, ...int64) (*sdk.TxResponse, error) {
	return &sdk.TxResponse{}, nil
}
func (m *mockTxManager) SendTransactionAsyncNoRetry(sdk.Msg) (*sdk.TxResponse, error) {
	return &sdk.TxResponse{}, nil
}
func (m *mockTxManager) SendTransactionSyncNoRetry(proto.Message) (*ctypes.ResultTx, error) {
	return nil, nil
}
func (m *mockTxManager) BroadcastMessages(string, ...sdk.Msg) (*sdk.TxResponse, time.Time, error) {
	return &sdk.TxResponse{}, time.Now(), nil
}
func (m *mockTxManager) GetClientContext() client.Context    { return client.Context{} }
func (m *mockTxManager) GetKeyring() *keyring.Keyring        { return nil }
func (m *mockTxManager) GetApiAccount() apiconfig.ApiAccount { return apiconfig.ApiAccount{} }
func (m *mockTxManager) Status(context.Context) (*ctypes.ResultStatus, error) {
	return nil, nil
}
func (m *mockTxManager) BankBalances(context.Context, string) ([]sdk.Coin, error) {
	return nil, nil
}
func (m *mockTxManager) GetJetStream() nats.JetStreamContext      { return nil }
func (m *mockTxManager) RefreshFeeTree(*inferencetypes.FeeParams) {}
func (m *mockTxManager) SetStoreCommitPrev(map[string]uint32)     {}
func (m *mockTxManager) SetHardwarePrev([]*inferencetypes.HardwareNode) {
}

func startTestNatsServer(t *testing.T) (*server.Server, nats.JetStreamContext) {
	opts := &server.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random port
		JetStream: true,
		StoreDir:  t.TempDir(),
	}

	ns, err := server.NewServer(opts)
	require.NoError(t, err)

	go ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second))

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)

	js, err := nc.JetStream()
	require.NoError(t, err)

	// Create test streams
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "txs_batch_poc_v2",
		Subjects: []string{"txs_batch_poc_v2"},
		Storage:  nats.MemoryStorage,
	})
	require.NoError(t, err)

	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "txs_batch_validation_v2",
		Subjects: []string{"txs_batch_validation_v2"},
		Storage:  nats.MemoryStorage,
	})
	require.NoError(t, err)

	// V1 PoC streams
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "txs_batch_poc_batch",
		Subjects: []string{"txs_batch_poc_batch"},
		Storage:  nats.MemoryStorage,
	})
	require.NoError(t, err)

	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "txs_batch_poc_validation",
		Subjects: []string{"txs_batch_poc_validation"},
		Storage:  nats.MemoryStorage,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		nc.Close()
		ns.Shutdown()
	})

	return ns, js
}

func getTestCodec(t *testing.T) codec.Codec {
	const (
		network     = "cosmos"
		accountName = "cosmosaccount"
		mnemonic    = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
		passphrase  = "testpass"
	)

	rpc := mocks.NewRPCClient(t)
	client := testutil.NewMockClient(t, rpc, network, accountName, mnemonic, passphrase)
	return client.Context().Codec
}

func TestBatchConsumer_ValidationV2Batching(t *testing.T) {
	_, js := startTestNatsServer(t)
	cdc := getTestCodec(t)

	mockMgr := &mockTxManager{}

	config := BatchConfig{
		ValidationV2FlushSize: 10,
	}

	consumer := NewBatchConsumer(js, cdc, mockMgr, config)
	err := consumer.Start()
	require.NoError(t, err)

	// Publish 10 validation V2 messages (validationV2FlushSize = 10)
	for i := 0; i < 10; i++ {
		msg := &inferencetypes.MsgSubmitPocValidationsV2{
			Creator:                  "creator",
			PocStageStartBlockHeight: int64(i),
			Validations: []*inferencetypes.PoCValidationEntryV2{
				{
					ParticipantAddress: "cosmos1abc",
					ValidatedWeight:    100,
				},
			},
		}
		err := consumer.PublishPocValidationV2(msg)
		require.NoError(t, err)
	}

	// Wait for processing
	time.Sleep(500 * time.Millisecond)

	calls := mockMgr.getBatchCalls()
	require.Len(t, calls, 1)
	assert.Len(t, calls[0], 10)
}
