package event_listener

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
)

// mockTmHTTPClient implements TmHTTPClient for tests.
type mockTmHTTPClient struct {
	mu          sync.Mutex
	calls       []int64
	txsPerBlock int
}

func newMockTmHTTPClient(txsPerBlock int) *mockTmHTTPClient {
	return &mockTmHTTPClient{txsPerBlock: txsPerBlock}
}

func (m *mockTmHTTPClient) BlockResults(ctx context.Context, height *int64) (*coretypes.ResultBlockResults, error) {
	m.mu.Lock()
	m.calls = append(m.calls, *height)
	m.mu.Unlock()

	// Build deterministic tx results for the requested height
	txs := make([]*abcitypes.ExecTxResult, m.txsPerBlock)
	for i := 0; i < m.txsPerBlock; i++ {
		txs[i] = &abcitypes.ExecTxResult{
			Events: []abcitypes.Event{
				{
					Type: "inference_finished",
					Attributes: []abcitypes.EventAttribute{
						{Key: "inference_id", Value: "id-", Index: true},
					},
				},
			},
		}
	}
	return &coretypes.ResultBlockResults{TxsResults: txs}, nil
}

func (m *mockTmHTTPClient) Block(ctx context.Context, height *int64) (*coretypes.ResultBlock, error) {
	return nil, nil
}

func (m *mockTmHTTPClient) Status(ctx context.Context) (*coretypes.ResultStatus, error) {
	// Return a mock status with earliest block at 1 (full history available)
	return &coretypes.ResultStatus{
		SyncInfo: coretypes.SyncInfo{
			EarliestBlockHeight: 1,
		},
	}, nil
}

// Test that BlockObserver handles a large backlog without deadlocking when the consumer is slow.
func TestBlockObserver_StressBackpressure(t *testing.T) {
	// Arrange
	manager := &apiconfig.ConfigManager{}
	bo := NewBlockObserverWithClient(manager, newMockTmHTTPClient(10))
	// Inject mock RPC client
	const (
		totalBlocks = 200
		txsPerBlock = 10
	)
	bo.tmClient = newMockTmHTTPClient(txsPerBlock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bo.Process(ctx)

	// Act: set caughtUp and jump height forward to create a backlog
	bo.updateStatus(totalBlocks, true)

	// Simulate slow consumer: delay before starting reads
	time.Sleep(100 * time.Millisecond)

	// Consume events slowly but ensure we eventually read them all (including barrier per block)
	expectedTotal := totalBlocks * (txsPerBlock + 1)
	received := 0
	deadline := time.After(5 * time.Second)
	for received < expectedTotal {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for events: got %d, want %d", received, expectedTotal)
		case ev, ok := <-bo.Queue.Out:
			if !ok {
				t.Fatalf("queue closed prematurely after %d events", received)
			}
			if ev == nil {
				t.Fatalf("nil event received at count=%d", received)
			}
			received++
			// Slow down the consumer a bit to exercise backpressure
			if received%200 == 0 {
				time.Sleep(5 * time.Millisecond)
			}
		}
	}

	// Assert: queried up to the target height
	if got := bo.lastQueriedBlockHeight.Load(); got != totalBlocks {
		t.Fatalf("lastQueriedBlockHeight=%d, want %d", got, totalBlocks)
	}
}

// Test that repeated status updates without changes do not re-trigger processing.
func TestBlockObserver_NoSpuriousWakeups(t *testing.T) {
	manager := &apiconfig.ConfigManager{}
	bo := NewBlockObserverWithClient(manager, newMockTmHTTPClient(1))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go bo.Process(ctx)

	// First update triggers processing of height 1 (1 tx + 1 barrier)
	bo.updateStatus(1, true)

	// Drain until barrier for height 1 is received; count tx events
	txCount := 0
	barrierSeen := false
	drainDeadline := time.After(2 * time.Second)
	for !barrierSeen {
		select {
		case <-drainDeadline:
			t.Fatalf("timeout waiting for barrier for height 1")
		case ev := <-bo.Queue.Out:
			if ev == nil {
				t.Fatalf("nil event while draining")
			}
			if ev.Result.Data.Type == systemBarrierEventType {
				heights := ev.Result.Events["barrier.height"]
				if len(heights) > 0 && heights[0] == "1" {
					barrierSeen = true
				}
				continue
			}
			if ev.Result.Data.Type == "tendermint/event/Tx" {
				txCount++
			}
		}
	}
	if txCount != 1 {
		t.Fatalf("expected 1 tx event before barrier, got %d", txCount)
	}

	// Extra duplicate updates should not produce more events
	for i := 0; i < 5; i++ {
		bo.updateStatus(1, true)
	}

	select {
	case <-time.After(200 * time.Millisecond):
		// ok, no new events
	case <-bo.Queue.Out:
		t.Fatalf("received unexpected extra event after duplicate updates")
	}
}

// TestProcessBlock_ParsesEvents validates that processBlock enqueues one message per tx
// and includes flattened keys with "tx.height".
func TestProcessBlock_ParsesEvents(t *testing.T) {
	manager := &apiconfig.ConfigManager{}
	mock := newMockTmHTTPClient(3)
	bo := NewBlockObserverWithClient(manager, mock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	height := int64(42)
	if ok := bo.processBlock(ctx, height); !ok {
		t.Fatalf("processBlock returned false")
	}

	// Expect 3 messages (one per tx)
	eventTypeToAttrCount := make(map[string]int)
	eventTypeToAttrNames := make(map[string]map[string]int)
	for i := 0; i < 3; i++ {
		select {
		case <-time.After(1 * time.Second):
			t.Fatalf("timeout waiting for event %d", i)
		case ev := <-bo.Queue.Out:
			if ev == nil {
				t.Fatalf("nil event")
			}
			if ev.Result.Data.Type != "tendermint/event/Tx" {
				t.Fatalf("unexpected type: %s", ev.Result.Data.Type)
			}
			if ev.Result.Events["tx.height"][0] != strconv.FormatInt(height, 10) {
				t.Fatalf("tx.height mismatch: %v", ev.Result.Events["tx.height"])
			}
			// Our mock emits inference_finished.inference_id
			if len(ev.Result.Events["inference_finished.inference_id"]) == 0 {
				t.Fatalf("expected inference_finished.inference_id in events")
			}

			// accumulate stats by event type
			for k, vals := range ev.Result.Events {
				parts := strings.SplitN(k, ".", 2)
				etype := parts[0]
				eventTypeToAttrCount[etype] += len(vals)
				if _, ok := eventTypeToAttrNames[etype]; !ok {
					eventTypeToAttrNames[etype] = make(map[string]int)
				}
				aname := ""
				if len(parts) > 1 {
					aname = parts[1]
				}
				eventTypeToAttrNames[etype][aname] += len(vals)
			}
		}
	}

	// Log stats for mock
	types := make([]string, 0, len(eventTypeToAttrCount))
	for k := range eventTypeToAttrCount {
		types = append(types, k)
	}
	sort.Strings(types)
	for _, et := range types {
		attrMap := eventTypeToAttrNames[et]
		attrNames := make([]string, 0, len(attrMap))
		for n := range attrMap {
			attrNames = append(attrNames, n)
		}
		sort.Strings(attrNames)
		// print top-level count and a compact attribute list
		t.Logf("mock stats: type=%s total_attrs=%d distinct_attrs=%d", et, eventTypeToAttrCount[et], len(attrNames))
	}
}

// TestProcessBlock_RealNodeParse hits a real node if env vars are set.
// Env: DAPI_TEST_RPC_URL, DAPI_TEST_BLOCK_HEIGHT
func TestProcessBlock_RealNodeParse(t *testing.T) {
	url := os.Getenv("DAPI_TEST_RPC_URL")
	heightStr := os.Getenv("DAPI_TEST_BLOCK_HEIGHT")
	if url == "" || heightStr == "" {
		t.Skip("set DAPI_TEST_RPC_URL and DAPI_TEST_BLOCK_HEIGHT to run this test")
	}

	h, err := strconv.ParseInt(heightStr, 10, 64)
	if err != nil {
		t.Fatalf("invalid DAPI_TEST_BLOCK_HEIGHT: %v", err)
	}

	client, err := cosmosclient.NewRpcClient(url)
	if err != nil {
		t.Fatalf("failed to create rpc client: %v", err)
	}

	// Probe expected tx count first
	ctx := context.Background()
	res, err := client.BlockResults(ctx, &h)
	if err != nil || res == nil {
		t.Fatalf("failed BlockResults probe: %v", err)
	}
	expected := len(res.TxsResults)

	manager := &apiconfig.ConfigManager{}
	bo := NewBlockObserverWithClient(manager, client)

	if ok := bo.processBlock(ctx, h); !ok {
		t.Fatalf("processBlock returned false")
	}

	received := 0
	eventTypeToAttrCount := make(map[string]int)
	eventTypeToAttrNames := make(map[string]map[string]int)
	deadline := time.After(5 * time.Second)
	for received < expected {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting events: got %d, want %d", received, expected)
		case ev := <-bo.Queue.Out:
			if ev == nil {
				t.Fatalf("nil event")
			}
			received++
			// Log parsed event keys for manual inspection
			t.Logf("event %d: id=%s keys=%d", received, ev.ID, len(ev.Result.Events))

			// accumulate stats by event type
			for k, vals := range ev.Result.Events {
				parts := strings.SplitN(k, ".", 2)
				etype := parts[0]
				eventTypeToAttrCount[etype] += len(vals)
				if _, ok := eventTypeToAttrNames[etype]; !ok {
					eventTypeToAttrNames[etype] = make(map[string]int)
				}
				aname := ""
				if len(parts) > 1 {
					aname = parts[1]
				}
				eventTypeToAttrNames[etype][aname] += len(vals)
			}
		}
	}

	// Print statistics by event type
	types := make([]string, 0, len(eventTypeToAttrCount))
	for k := range eventTypeToAttrCount {
		types = append(types, k)
	}
	sort.Strings(types)
	for _, et := range types {
		attrMap := eventTypeToAttrNames[et]
		attrNames := make([]string, 0, len(attrMap))
		for n := range attrMap {
			attrNames = append(attrNames, n)
		}
		sort.Strings(attrNames)
		t.Logf("stats: type=%s total_attrs=%d distinct_attrs=%d", et, eventTypeToAttrCount[et], len(attrNames))
		// Optionally list a few attributes
		for i, n := range attrNames {
			if i >= 10 {
				break
			}
			t.Logf("  attr=%s count=%d", n, attrMap[n])
		}
	}
}

// Note: we rely on zero-value apiconfig.ConfigManager methods that read/write
// in-memory fields and no-op writes when WriterProvider is nil.
