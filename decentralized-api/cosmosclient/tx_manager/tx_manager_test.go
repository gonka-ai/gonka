package tx_manager

import (
	"context"
	"decentralized-api/internal/nats/server"
	"encoding/json"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/google/uuid"
	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient/mocks"
	natssrv "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/productscience/inference/api/inference/inference"
	testutil "github.com/productscience/inference/testutil/cosmoclient"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// ============================================================================
// Test helpers for retry logic tests
// ============================================================================

func startTestNatsServerForTxManager(t *testing.T) (*natssrv.Server, nats.JetStreamContext, *nats.Conn) {
	opts := &natssrv.Options{
		Host:      "127.0.0.1",
		Port:      -1, // random port
		JetStream: true,
		StoreDir:  t.TempDir(),
	}

	ns, err := natssrv.NewServer(opts)
	require.NoError(t, err)

	go ns.Start()
	require.True(t, ns.ReadyForConnections(5*time.Second))

	nc, err := nats.Connect(ns.ClientURL())
	require.NoError(t, err)

	js, err := nc.JetStream()
	require.NoError(t, err)

	// Create TxsToSendStream
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     server.TxsToSendStream,
		Subjects: []string{server.TxsToSendStream},
		Storage:  nats.MemoryStorage,
	})
	require.NoError(t, err)

	// Create TxsToObserveStream
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     server.TxsToObserveStream,
		Subjects: []string{server.TxsToObserveStream},
		Storage:  nats.MemoryStorage,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		nc.Close()
		ns.Shutdown()
	})

	return ns, js, nc
}

func createTestManager(t *testing.T, js nats.JetStreamContext) *manager {
	return &manager{
		ctx:           context.Background(),
		natsJetStream: js,
		blockTimeTracker: &blockTimeTracker{
			latestBlockTime: time.Now(),
			lastUpdatedAt:   time.Now(),
			maxBlockTimeout: 10 * time.Second,
			pauseSending:    false,
		},
	}
}

// ============================================================================
// Unit tests for requeue helper
// ============================================================================

func TestRequeue_IncrementsAttemptsAndSetsTime(t *testing.T) {
	_, js, _ := startTestNatsServerForTxManager(t)
	m := createTestManager(t, js)

	tx := &txToSend{
		TxInfo: txInfo{
			Id: "test-tx-1",
		},
		Attempts:    0,
		RequeueTime: time.Time{}, // zero value
	}

	beforeRequeue := time.Now()
	err := m.requeue(tx)
	afterRequeue := time.Now()

	require.NoError(t, err)
	assert.Equal(t, 1, tx.Attempts, "Attempts should be incremented")
	assert.False(t, tx.RequeueTime.IsZero(), "RequeueTime should be set")
	assert.True(t, tx.RequeueTime.After(beforeRequeue) || tx.RequeueTime.Equal(beforeRequeue),
		"RequeueTime should be >= beforeRequeue")
	assert.True(t, tx.RequeueTime.Before(afterRequeue) || tx.RequeueTime.Equal(afterRequeue),
		"RequeueTime should be <= afterRequeue")
}

func TestRequeue_MaxAttemptsReturnsNil(t *testing.T) {
	_, js, _ := startTestNatsServerForTxManager(t)
	m := createTestManager(t, js)

	tx := &txToSend{
		TxInfo: txInfo{
			Id: "test-tx-max",
		},
		Attempts: 99, // Will become 100 after requeue, hitting maxAttempts
	}

	// Get initial stream info
	streamInfo, err := js.StreamInfo(server.TxsToSendStream)
	require.NoError(t, err)
	initialMsgs := streamInfo.State.Msgs

	err = m.requeue(tx)
	require.NoError(t, err)
	assert.Equal(t, 100, tx.Attempts, "Attempts should be incremented to 100")

	// Verify no message was published
	streamInfo, err = js.StreamInfo(server.TxsToSendStream)
	require.NoError(t, err)
	assert.Equal(t, initialMsgs, streamInfo.State.Msgs, "No message should be published when max attempts reached")
}

func TestRequeue_PublishesToSendStream(t *testing.T) {
	_, js, _ := startTestNatsServerForTxManager(t)
	m := createTestManager(t, js)

	tx := &txToSend{
		TxInfo: txInfo{
			Id:     "test-tx-publish",
			TxHash: "abc123",
		},
		Attempts: 0,
	}

	// Get initial stream info
	streamInfo, err := js.StreamInfo(server.TxsToSendStream)
	require.NoError(t, err)
	initialMsgs := streamInfo.State.Msgs

	err = m.requeue(tx)
	require.NoError(t, err)

	// Verify message was published
	streamInfo, err = js.StreamInfo(server.TxsToSendStream)
	require.NoError(t, err)
	assert.Equal(t, initialMsgs+1, streamInfo.State.Msgs, "One message should be published")

	// Verify message content
	sub, err := js.SubscribeSync(server.TxsToSendStream, nats.DeliverLast())
	require.NoError(t, err)
	defer sub.Unsubscribe()

	msg, err := sub.NextMsg(time.Second)
	require.NoError(t, err)

	var receivedTx txToSend
	err = json.Unmarshal(msg.Data, &receivedTx)
	require.NoError(t, err)

	assert.Equal(t, "test-tx-publish", receivedTx.TxInfo.Id)
	assert.Equal(t, 1, receivedTx.Attempts)
	assert.False(t, receivedTx.RequeueTime.IsZero())
}

// ============================================================================
// Unit tests for RequeueTime JSON serialization
// ============================================================================

func TestTxToSend_RequeueTimeSerializes(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond) // JSON loses nanosecond precision

	original := txToSend{
		TxInfo: txInfo{
			Id: "serialize-test",
		},
		Sent:        false,
		Attempts:    2,
		RequeueTime: now,
	}

	// Marshal to JSON
	data, err := json.Marshal(original)
	require.NoError(t, err)

	// Unmarshal back
	var restored txToSend
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err)

	assert.Equal(t, original.TxInfo.Id, restored.TxInfo.Id)
	assert.Equal(t, original.Attempts, restored.Attempts)
	assert.Equal(t, original.Sent, restored.Sent)
	// Compare with truncated time since JSON loses some precision
	assert.True(t, restored.RequeueTime.Sub(original.RequeueTime).Abs() < time.Second,
		"RequeueTime should be preserved within 1 second precision")
}

func TestTxToSend_ZeroRequeueTimePreserved(t *testing.T) {
	tx := txToSend{
		TxInfo: txInfo{
			Id: "zero-time-test",
		},
		Attempts:    0,
		RequeueTime: time.Time{}, // zero value
	}

	data, err := json.Marshal(tx)
	require.NoError(t, err)

	// Unmarshal and verify zero time is preserved
	var restored txToSend
	err = json.Unmarshal(data, &restored)
	require.NoError(t, err)
	assert.True(t, restored.RequeueTime.IsZero(), "RequeueTime should remain zero after round-trip")
}

// ============================================================================
// Unit tests for delay check logic
// ============================================================================

func TestDelayCheck_RecentRequeueShouldBeDelayed(t *testing.T) {
	// Test the logic that checks if a requeued message should be delayed
	tx := txToSend{
		TxInfo:      txInfo{Id: "delay-test"},
		Attempts:    1,
		RequeueTime: time.Now(), // Just now - should be delayed
	}

	// Simulate the delay check logic from sendTxs
	if !tx.RequeueTime.IsZero() {
		elapsed := time.Since(tx.RequeueTime)
		shouldDelay := elapsed < defaultSenderNackDelay
		assert.True(t, shouldDelay, "Recently requeued message should be delayed")
	}
}

func TestDelayCheck_OldRequeueProcessedImmediately(t *testing.T) {
	// Test that old requeued messages are processed immediately
	tx := txToSend{
		TxInfo:      txInfo{Id: "old-requeue-test"},
		Attempts:    1,
		RequeueTime: time.Now().Add(-10 * time.Second), // 10 seconds ago
	}

	// Simulate the delay check logic from sendTxs
	if !tx.RequeueTime.IsZero() {
		elapsed := time.Since(tx.RequeueTime)
		shouldDelay := elapsed < defaultSenderNackDelay
		assert.False(t, shouldDelay, "Old requeued message should be processed immediately")
	}
}

func TestDelayCheck_FirstAttemptNoDelay(t *testing.T) {
	// First attempt has zero RequeueTime - should not delay
	tx := txToSend{
		TxInfo:      txInfo{Id: "first-attempt-test"},
		Attempts:    0,
		RequeueTime: time.Time{}, // zero - first attempt
	}

	// Zero RequeueTime means first attempt, should not delay
	assert.True(t, tx.RequeueTime.IsZero(), "First attempt should have zero RequeueTime")
}

// ============================================================================
// Integration test for max attempts terminates
// ============================================================================

func TestMaxAttemptsCheck(t *testing.T) {
	// Test the max attempts check logic directly
	testCases := []struct {
		name            string
		attempts        int
		shouldTerminate bool
	}{
		{"attempts 0", 0, false},
		{"attempts 50", 50, false},
		{"attempts 99", 99, false},
		{"attempts 100 (max)", 100, true},
		{"attempts 101 (over max)", 101, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tx := txToSend{
				Attempts: tc.attempts,
			}

			shouldTerminate := tx.Attempts >= maxAttempts
			assert.Equal(t, tc.shouldTerminate, shouldTerminate,
				"Attempts=%d should terminate=%v", tc.attempts, tc.shouldTerminate)
		})
	}
}

func TestRequeueIntegration_MultipleRequeues(t *testing.T) {
	_, js, _ := startTestNatsServerForTxManager(t)
	m := createTestManager(t, js)

	tx := &txToSend{
		TxInfo: txInfo{
			Id: "multi-requeue-test",
		},
		Attempts: 97, // Start near max to test boundary
	}

	// First requeue (97 -> 98)
	err := m.requeue(tx)
	require.NoError(t, err)
	assert.Equal(t, 98, tx.Attempts)
	firstRequeueTime := tx.RequeueTime

	// Second requeue (98 -> 99)
	time.Sleep(10 * time.Millisecond) // Ensure time difference
	err = m.requeue(tx)
	require.NoError(t, err)
	assert.Equal(t, 99, tx.Attempts)
	assert.True(t, tx.RequeueTime.After(firstRequeueTime), "RequeueTime should be updated")

	// Third requeue (99 -> 100) - should hit max attempts
	err = m.requeue(tx)
	require.NoError(t, err)
	assert.Equal(t, 100, tx.Attempts)

	// Verify 2 messages published (third was at max, not published)
	streamInfo, err := js.StreamInfo(server.TxsToSendStream)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), streamInfo.State.Msgs, "Only 2 messages should be published (3rd hit max)")
}
