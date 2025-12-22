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
	"github.com/google/uuid"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient/mocks"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/api/inference/inference"
	testutil "github.com/productscience/inference/testutil/cosmoclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"decentralized-api/apiconfig"
)

type mockTxManager struct {
	sendBatchCalls [][]sdk.Msg
	mu             sync.Mutex
}

func (m *mockTxManager) SendBatchAsyncWithRetry(msgs []sdk.Msg) error {
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

func (m *mockTxManager) SendTransactionAsyncWithRetry(sdk.Msg) (*sdk.TxResponse, error) {
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
func (m *mockTxManager) GetJetStream() nats.JetStreamContext { return nil }

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
		Name:     "txs_batch_start",
		Subjects: []string{"txs_batch_start"},
		Storage:  nats.MemoryStorage,
	})
	require.NoError(t, err)

	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "txs_batch_finish",
		Subjects: []string{"txs_batch_finish"},
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

func TestBatchConsumer_FlushOnSize(t *testing.T) {
	_, js := startTestNatsServer(t)
	cdc := getTestCodec(t)

	mockMgr := &mockTxManager{}

	config := BatchConfig{
		FlushSize:    5,
		FlushTimeout: 10 * time.Second,
	}

	consumer := NewBatchConsumer(js, cdc, mockMgr, config)
	err := consumer.Start()
	require.NoError(t, err)

	// Publish 5 start inference messages (should trigger flush)
	for i := 0; i < 5; i++ {
		msg := &inference.MsgStartInference{
			Creator:     "creator",
			InferenceId: uuid.New().String(),
			Model:       "test-model",
		}
		err := consumer.PublishStartInference(msg)
		require.NoError(t, err)
	}

	// Wait for processing
	time.Sleep(500 * time.Millisecond)

	calls := mockMgr.getBatchCalls()
	require.Len(t, calls, 1)
	assert.Len(t, calls[0], 5)
}

func TestBatchConsumer_FlushOnTimeout(t *testing.T) {
	_, js := startTestNatsServer(t)
	cdc := getTestCodec(t)

	mockMgr := &mockTxManager{}

	config := BatchConfig{
		FlushSize:    100, // high threshold
		FlushTimeout: 2 * time.Second,
	}

	consumer := NewBatchConsumer(js, cdc, mockMgr, config)
	err := consumer.Start()
	require.NoError(t, err)

	// Publish only 2 messages (below threshold)
	for i := 0; i < 2; i++ {
		msg := &inference.MsgStartInference{
			Creator:     "creator",
			InferenceId: uuid.New().String(),
		}
		err := consumer.PublishStartInference(msg)
		require.NoError(t, err)
	}

	// Wait for messages to be consumed
	time.Sleep(500 * time.Millisecond)
	assert.Len(t, mockMgr.getBatchCalls(), 0)

	// Wait for timeout flush (ticker checks every second, timeout is 2s)
	time.Sleep(3 * time.Second)
	assert.Len(t, mockMgr.getBatchCalls(), 1)
}

func TestBatchConsumer_SeparateQueues(t *testing.T) {
	_, js := startTestNatsServer(t)
	cdc := getTestCodec(t)

	mockMgr := &mockTxManager{}

	config := BatchConfig{
		FlushSize:    3,
		FlushTimeout: 10 * time.Second,
	}

	consumer := NewBatchConsumer(js, cdc, mockMgr, config)
	err := consumer.Start()
	require.NoError(t, err)

	// Publish 3 start messages
	for i := 0; i < 3; i++ {
		msg := &inference.MsgStartInference{
			Creator:     "creator",
			InferenceId: uuid.New().String(),
		}
		err := consumer.PublishStartInference(msg)
		require.NoError(t, err)
	}

	// Publish 3 finish messages
	for i := 0; i < 3; i++ {
		msg := &inference.MsgFinishInference{
			Creator:     "creator",
			InferenceId: uuid.New().String(),
		}
		err := consumer.PublishFinishInference(msg)
		require.NoError(t, err)
	}

	time.Sleep(500 * time.Millisecond)

	// Should have 2 batch calls (one for start, one for finish)
	calls := mockMgr.getBatchCalls()
	assert.Len(t, calls, 2)
}

func TestBatchConsumer_Persistence(t *testing.T) {
	_, js := startTestNatsServer(t)
	cdc := getTestCodec(t)

	mockMgr := &mockTxManager{}

	config := BatchConfig{
		FlushSize:    10,
		FlushTimeout: 2 * time.Second,
	}

	// Publish messages before consumer starts (simulating restart)
	for i := 0; i < 3; i++ {
		msg := &inference.MsgStartInference{
			Creator:     "creator",
			InferenceId: uuid.New().String(),
		}
		data, err := cdc.MarshalInterfaceJSON(msg)
		require.NoError(t, err)
		_, err = js.Publish("txs_batch_start", data)
		require.NoError(t, err)
	}

	// Now start consumer (simulating restart recovery)
	consumer := NewBatchConsumer(js, cdc, mockMgr, config)
	err := consumer.Start()
	require.NoError(t, err)

	// Wait for messages to be consumed and timeout flush
	time.Sleep(3 * time.Second)

	// Messages should be recovered and broadcast
	assert.Len(t, mockMgr.getBatchCalls(), 1)
}
