package event_listener

import (
	"context"
	"strconv"
	"testing"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/internal/event_listener/chainevents"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/stretchr/testify/require"
)

type finalizeBLSClient struct {
	mockTmHTTPClient
	result *coretypes.ResultBlockResults
}

func (c *finalizeBLSClient) BlockResults(context.Context, *int64) (*coretypes.ResultBlockResults, error) {
	return c.result, nil
}

func finalizeSigningRequest(index int) abcitypes.Event {
	value := strconv.Itoa(index)
	return abcitypes.Event{
		Type: blsThresholdSigningRequestedEvent,
		Attributes: []abcitypes.EventAttribute{
			{Key: "request_id", Value: `"request-` + value + `"`},
			{Key: "current_epoch_id", Value: `"50` + value + `"`},
			{Key: "message_hash", Value: `"hash-` + value + `"`},
			{Key: "deadline_block_height", Value: `"10` + value + `"`},
		},
	}
}

func newFinalizeBLSObserver(t *testing.T, result *coretypes.ResultBlockResults) *BlockObserver {
	t.Helper()
	bo := NewBlockObserverWithClient(&apiconfig.ConfigManager{}, &finalizeBLSClient{result: result})
	t.Cleanup(bo.Queue.Close)
	return bo
}

func receiveFinalizeBLSMessage(t *testing.T, messages <-chan *chainevents.JSONRPCResponse) *chainevents.JSONRPCResponse {
	t.Helper()
	select {
	case msg := <-messages:
		require.NotNil(t, msg)
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for block event")
		return nil
	}
}

func TestProcessBlock_FinalizeBLSRequests(t *testing.T) {
	for _, count := range []int{1, 3} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			result := &coretypes.ResultBlockResults{Height: 42}
			want := map[string][]string{"tx.height": {"42"}}
			for i := 0; i < count; i++ {
				event := finalizeSigningRequest(i)
				result.FinalizeBlockEvents = append(result.FinalizeBlockEvents, event)
				for _, attr := range event.Attributes {
					key := blsThresholdSigningRequestedEvent + "." + attr.Key
					want[key] = append(want[key], attr.Value)
				}
				// Interleaving other block events must not mix their attributes
				// into the parallel arrays consumed by the signing handler.
				result.FinalizeBlockEvents = append(result.FinalizeBlockEvents, abcitypes.Event{
					Type: blsKeyGenerationInitiatedEvent,
					Attributes: []abcitypes.EventAttribute{
						{Key: "request_id", Value: "unrelated"},
					},
				})
			}
			bo := newFinalizeBLSObserver(t, result)
			handler := &BlsTransactionEventHandler{}
			bo.SetRelevanceFilter(handler.CanHandle)
			require.True(t, bo.processBlock(context.Background(), 42))

			msg := receiveFinalizeBLSMessage(t, bo.Queue.Out)
			require.Equal(t, "block-42-finalize-bls", msg.ID)
			require.Equal(t, "block_monitor/Tx", msg.Result.Query)
			require.Equal(t, txEventType, msg.Result.Data.Type)
			require.Equal(t, want, msg.Result.Events)
			require.True(t, handler.CanHandle(msg))
			// A forwarded request still uses the existing handler's sync gate;
			// an unsynced listener must not touch its (nil) BLS manager.
			require.NoError(t, handler.Handle(msg, &EventListener{}))

			barrier := receiveFinalizeBLSMessage(t, bo.Queue.Out)
			require.Equal(t, systemBarrierEventType, barrier.Result.Data.Type)
			require.Equal(t, []string{"42"}, barrier.Result.Events["barrier.height"])
		})
	}
}

func TestProcessBlock_FinalizeBLSFiltersBeforeEnqueue(t *testing.T) {
	for _, withRequest := range []bool{false, true} {
		t.Run(strconv.FormatBool(withRequest), func(t *testing.T) {
			result := &coretypes.ResultBlockResults{FinalizeBlockEvents: []abcitypes.Event{
				{Type: blsKeyGenerationInitiatedEvent, Attributes: []abcitypes.EventAttribute{{Key: "epoch_id", Value: "502"}}},
				{Type: "inference_finished", Attributes: []abcitypes.EventAttribute{{Key: "inference_id", Value: "other"}}},
				{Type: blsThresholdSigningRequestedEvent + "Extra", Attributes: []abcitypes.EventAttribute{{Key: "request_id", Value: "other"}}},
			}}
			if withRequest {
				result.FinalizeBlockEvents = append(result.FinalizeBlockEvents, finalizeSigningRequest(0))
			}
			bo := newFinalizeBLSObserver(t, result)
			filterCalls := 0
			bo.SetRelevanceFilter(func(msg *chainevents.JSONRPCResponse) bool {
				filterCalls++
				require.Equal(t, txEventType, msg.Result.Data.Type)
				require.True(t, (&BlsTransactionEventHandler{}).CanHandle(msg))
				return false
			})
			require.True(t, bo.processBlock(context.Background(), 42))
			if withRequest {
				require.Equal(t, 1, filterCalls)
			} else {
				require.Zero(t, filterCalls)
			}
			// Dropping all block events must still let the barrier advance.
			barrier := receiveFinalizeBLSMessage(t, bo.Queue.Out)
			require.Equal(t, "block-42-barrier", barrier.ID)
		})
	}
}

func TestProcessBlock_FinalizeBLSBackpressureOrder(t *testing.T) {
	bo := newFinalizeBLSObserver(t, &coretypes.ResultBlockResults{
		TxsResults:          []*abcitypes.ExecTxResult{{Events: []abcitypes.Event{finalizeSigningRequest(0)}}},
		FinalizeBlockEvents: []abcitypes.Event{finalizeSigningRequest(1), finalizeSigningRequest(2)},
	})
	// An unbuffered queue forces every enqueue to wait for the consumer,
	// making ordering and the barrier's backpressure deterministic.
	bo.Queue.Close()
	messages := make(chan *chainevents.JSONRPCResponse)
	bo.Queue = &UnboundedQueue[*chainevents.JSONRPCResponse]{In: messages, Out: messages}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- bo.processBlock(ctx, 42) }()

	tx := receiveFinalizeBLSMessage(t, messages)
	require.Equal(t, "block-42-tx-0", tx.ID)
	require.Equal(t, []string{`"request-0"`}, tx.Result.Events[blsThresholdSigningRequestedEvent+".request_id"])
	finalize := receiveFinalizeBLSMessage(t, messages)
	require.Equal(t, "block-42-finalize-bls", finalize.ID)
	require.Equal(t, []string{`"request-1"`, `"request-2"`}, finalize.Result.Events[blsThresholdSigningRequestedEvent+".request_id"])
	select {
	case <-done:
		t.Fatal("block finished before its barrier was enqueued")
	default:
	}
	barrier := receiveFinalizeBLSMessage(t, messages)
	require.Equal(t, "block-42-barrier", barrier.ID)
	select {
	case ok := <-done:
		require.True(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("block did not finish after the barrier was consumed")
	}
}

func TestBlockObserver_FinalizeBLSCancellationDoesNotAdvance(t *testing.T) {
	bo := newFinalizeBLSObserver(t, &coretypes.ResultBlockResults{
		FinalizeBlockEvents: []abcitypes.Event{finalizeSigningRequest(0)},
	})
	bo.Queue.Close()
	messages := make(chan *chainevents.JSONRPCResponse)
	bo.Queue = &UnboundedQueue[*chainevents.JSONRPCResponse]{In: messages, Out: messages}
	bo.lastQueriedBlockHeight.Store(41)
	reachedFinalize := make(chan struct{})
	bo.SetRelevanceFilter(func(msg *chainevents.JSONRPCResponse) bool {
		if msg.ID == "block-42-finalize-bls" {
			close(reachedFinalize)
		}
		return true
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		bo.Process(ctx)
	}()
	bo.updateStatus(42, true)
	select {
	case <-reachedFinalize:
	case <-time.After(5 * time.Second):
		t.Fatal("signing retry never reached the producer filter")
	}
	// The retry cannot enter a full queue; cancellation must exit without
	// enqueuing the barrier or recording progress past the omitted request.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled observer remained blocked on the retry enqueue")
	}
	require.Equal(t, int64(41), bo.lastQueriedBlockHeight.Load())
	require.Zero(t, bo.lastProcessedBlockHeight.Load())
	select {
	case msg := <-messages:
		t.Fatalf("unexpected message after cancellation: %v", msg)
	default:
	}
}
