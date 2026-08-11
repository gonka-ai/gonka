package event_listener

import (
	"common/logging"
	"decentralized-api/apiconfig"
	"decentralized-api/internal/event_listener/chainevents"
	"strconv"

	"context"
	"decentralized-api/cosmosclient"

	"sync/atomic"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/productscience/inference/x/inference/types"
)

// txEventQueueCapacity bounds the in-memory backlog of synthetic tx events
// produced by the BlockObserver. When the queue is full, processBlock blocks
// (applying backpressure to block fetching) instead of letting the backing
// slice grow without bound and eventually OOM the process. It is sized to
// comfortably hold several blocks' worth of *relevant* events (irrelevant ones
// are filtered out before enqueue) while capping worst-case memory.
const txEventQueueCapacity = 20000

type BlockObserver struct {
	lastProcessedBlockHeight atomic.Int64
	lastQueriedBlockHeight   atomic.Int64
	currentBlockHeight       atomic.Int64
	ConfigManager            *apiconfig.ConfigManager
	Queue                    *UnboundedQueue[*chainevents.JSONRPCResponse]
	caughtUp                 atomic.Bool
	tmClient                 TmHTTPClient
	notify                   chan struct{}
	// relevanceFilter, when set, is consulted before enqueuing a synthetic tx
	// event. Only events for which it returns true are enqueued; this keeps
	// chain traffic the DAPI has no handler for from consuming queue capacity
	// and worker time. Barrier events are always enqueued regardless, so block
	// progress (lastProcessedBlockHeight) still advances for every block.
	// A nil filter preserves the legacy behavior of enqueuing every event.
	relevanceFilter func(*chainevents.JSONRPCResponse) bool
}

// SetRelevanceFilter installs a predicate used to drop irrelevant tx events at
// the producer before they enter the queue. It must be safe for concurrent use
// and is expected to be set once during wiring, before Process starts.
func (bo *BlockObserver) SetRelevanceFilter(filter func(*chainevents.JSONRPCResponse) bool) {
	bo.relevanceFilter = filter
}

// TmHTTPClient abstracts the subset of RPC methods we need
type TmHTTPClient interface {
	BlockResults(ctx context.Context, height *int64) (*coretypes.ResultBlockResults, error)
	Status(ctx context.Context) (*coretypes.ResultStatus, error)
}

func NewBlockObserver(manager *apiconfig.ConfigManager) *BlockObserver {
	queue := NewBoundedQueue[*chainevents.JSONRPCResponse](txEventQueueCapacity)
	// Initialize Tendermint RPC client
	httpClient, err := cosmosclient.NewRpcClient(manager.GetChainNodeConfig().Url)
	if err != nil {
		logging.Error("Failed to create Tendermint RPC client for BlockObserver", types.EventProcessing, "error", err)
	}

	bo := &BlockObserver{
		ConfigManager: manager,
		Queue:         queue,
		tmClient:      httpClient,
		notify:        make(chan struct{}, 1),
	}

	bo.lastProcessedBlockHeight.Store(manager.GetLastProcessedHeight())
	// Start querying from last processed height
	bo.lastQueriedBlockHeight.Store(bo.lastProcessedBlockHeight.Load())
	bo.currentBlockHeight.Store(manager.GetHeight())
	bo.caughtUp.Store(false)

	// If first run and we have a current height but no last processed, start from current-1
	if bo.lastProcessedBlockHeight.Load() == 0 && bo.currentBlockHeight.Load() > 0 {
		bo.lastProcessedBlockHeight.Store(bo.currentBlockHeight.Load() - 1)
		bo.lastQueriedBlockHeight.Store(bo.lastProcessedBlockHeight.Load())
	}

	return bo
}

// NewBlockObserverWithClient allows injecting a custom Tendermint RPC client (used in tests)
func NewBlockObserverWithClient(manager *apiconfig.ConfigManager, client TmHTTPClient) *BlockObserver {
	queue := NewBoundedQueue[*chainevents.JSONRPCResponse](txEventQueueCapacity)

	bo := &BlockObserver{
		ConfigManager: manager,
		Queue:         queue,
		tmClient:      client,
		notify:        make(chan struct{}, 1),
	}

	bo.lastProcessedBlockHeight.Store(manager.GetLastProcessedHeight())
	bo.currentBlockHeight.Store(manager.GetHeight())
	bo.caughtUp.Store(false)

	if bo.lastProcessedBlockHeight.Load() == 0 && bo.currentBlockHeight.Load() > 0 {
		bo.lastProcessedBlockHeight.Store(bo.currentBlockHeight.Load() - 1)
	}
	return bo
}

// UpdateStatus sets both height and caughtUp atomically and signals processing only if changed
func (bo *BlockObserver) updateStatus(newHeight int64, caughtUp bool) {
	prevHeight := bo.currentBlockHeight.Load()
	prevCaught := bo.caughtUp.Load()
	changed := (newHeight != prevHeight) || (caughtUp != prevCaught)
	if !changed {
		return
	}
	bo.currentBlockHeight.Store(newHeight)
	bo.caughtUp.Store(caughtUp)
	select {
	case bo.notify <- struct{}{}:
	default:
		// already notified; coalesce
	}
}

// getStartProcessingBlock determines the correct starting block height for processing
// Returns max(currentBlock - 500, firstAvailableBlock) to handle snapshot nodes
func (bo *BlockObserver) getStartProcessingBlock(ctx context.Context, currentBlock int64) int64 {
	if bo.tmClient == nil {
		logging.Warn("tmClient is nil, starting from recent block to avoid unavailable blocks", types.EventProcessing)
		return currentBlock - 1
	}

	status, err := bo.tmClient.Status(ctx)
	if err != nil || status == nil {
		logging.Warn("Failed to fetch chain status, starting from recent block to avoid unavailable blocks", types.EventProcessing, "error", err)
		return currentBlock - 1
	}

	firstAvailable := status.SyncInfo.EarliestBlockHeight
	targetStart := currentBlock - 500

	if targetStart < firstAvailable {
		logging.Info("Adjusting start block for snapshot node", types.EventProcessing,
			"targetStart", targetStart,
			"firstAvailable", firstAvailable,
			"usingBlock", firstAvailable)
		return firstAvailable
	}

	logging.Debug("Using target start block", types.EventProcessing,
		"startBlock", targetStart,
		"firstAvailable", firstAvailable)
	return targetStart
}

func (bo *BlockObserver) Process(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-bo.notify:
			// Drain extra signals to coalesce bursts
		drain:
			for {
				select {
				case <-bo.notify:
					continue
				default:
					break drain
				}
			}
			if !bo.caughtUp.Load() {
				continue
			}

			currentHeight := bo.currentBlockHeight.Load()
			lastQueried := bo.lastQueriedBlockHeight.Load()

			// Check if lastQueried is too far behind (more than 500 blocks) or invalid
			// This handles snapshot nodes where old blocks are unavailable
			if lastQueried < (currentHeight-500) || lastQueried <= 0 {
				startBlock := bo.getStartProcessingBlock(ctx, currentHeight)
				logging.Info("Resetting lastQueriedBlockHeight for block availability", types.EventProcessing,
					"oldLastQueried", lastQueried,
					"currentHeight", currentHeight,
					"newStartBlock", startBlock)
				bo.lastQueriedBlockHeight.Store(startBlock - 1)
			}

			// Process as many contiguous blocks as available (based on lastQueried)
			for {
				nextHeight := bo.lastQueriedBlockHeight.Load() + 1
				if nextHeight > bo.currentBlockHeight.Load() || nextHeight <= 0 {
					break
				}
				if !bo.processBlock(ctx, nextHeight) {
					// stop on fetch error; next status change will retry
					break
				}
				// Successfully enqueued events for nextHeight; advance lastQueried
				bo.lastQueriedBlockHeight.Store(nextHeight)
			}
		}
	}
}

func (bo *BlockObserver) processBlock(ctx context.Context, height int64) bool {
	if bo.tmClient == nil {
		logging.Warn("BlockObserver tmClient is nil, skipping", types.EventProcessing)
		return false
	}
	res, err := bo.tmClient.BlockResults(ctx, &height)
	if err != nil || res == nil {
		logging.Warn("Failed to fetch BlockResults", types.EventProcessing, "height", height, "error", err)
		return false
	}

	// For each tx in the block, flatten events and enqueue as synthetic Tx events
	for txIdx, txRes := range res.TxsResults {
		events := make(map[string][]string)
		// Include tx.height to satisfy waitForEventHeight
		events["tx.height"] = []string{strconv.FormatInt(height, 10)}

		for _, ev := range txRes.Events {
			evType := ev.Type
			for _, attr := range ev.Attributes {
				key := evType + "." + attr.Key
				val := attr.Value
				events[key] = append(events[key], val)
			}
		}

		msg := &chainevents.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      "block-" + strconv.FormatInt(height, 10) + "-tx-" + strconv.Itoa(txIdx),
			Result: chainevents.Result{
				Query:  "block_monitor/Tx",
				Data:   chainevents.Data{Type: "tendermint/event/Tx", Value: map[string]interface{}{}},
				Events: events,
			},
		}
		// Drop events the DAPI has no handler for before they consume queue
		// capacity. The consumer applies the same gate (hasHandler) after
		// dequeue, so this is behavior-preserving for events that matter.
		if bo.relevanceFilter != nil && !bo.relevanceFilter(msg) {
			continue
		}
		// Enqueue for processing (blocks under backpressure when the queue is full)
		if !bo.enqueue(ctx, msg) {
			return false
		}
	}
	// Always enqueue a barrier event to signal block completion when consumed,
	// even if every tx event in this block was filtered out. The barrier is how
	// lastProcessedBlockHeight advances, so it must flow for every observed block.
	barrier := &chainevents.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      "block-" + strconv.FormatInt(height, 10) + "-barrier",
		Result: chainevents.Result{
			Query:  "block_monitor/Barrier",
			Data:   chainevents.Data{Type: systemBarrierEventType, Value: map[string]interface{}{}},
			Events: map[string][]string{"barrier.height": {strconv.FormatInt(height, 10)}},
		},
	}
	if !bo.enqueue(ctx, barrier) {
		return false
	}
	return true
}

// enqueue sends an item to the (possibly bounded) queue while honoring context
// cancellation. Under backpressure the send blocks until a consumer drains an
// item; selecting on ctx.Done() ensures a backpressured producer can still shut
// down cleanly instead of deadlocking on a full queue during teardown.
//
// Returning false means the item was not enqueued (context cancelled). Callers
// (processBlock) treat this like a fetch failure: lastQueriedBlockHeight is not
// advanced, so the block is retried on the next run and no progress is recorded
// for a partially-enqueued block.
func (bo *BlockObserver) enqueue(ctx context.Context, msg *chainevents.JSONRPCResponse) bool {
	select {
	case bo.Queue.In <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

// signalAllEventsRead is called once the barrier event for a block
// has been consumed by a worker, meaning all prior events for that block
// were dequeued. We can now safely advance lastProcessed height.
func (bo *BlockObserver) signalAllEventsRead(height int64) {
	// Future improvement: check contiguity here
	//  and roll back the lastQueried if some timeout/block difference is exceeded
	if height < bo.lastProcessedBlockHeight.Load() {
		logging.Warn("BlockObserver: signalAllEventsRead called for out-of-order block", types.EventProcessing, "height", height)
	} else if height == bo.lastProcessedBlockHeight.Load() {
		// Already processed
		logging.Warn("BlockObserver: signalAllEventsRead called for already processed block", types.EventProcessing, "height", height)
	} else {
		bo.lastProcessedBlockHeight.Store(height)
		if err := bo.ConfigManager.SetLastProcessedHeight(height); err != nil {
			logging.Warn("BlockObserver: Failed to persist last processed height", types.Config, "error", err)
		}
	}
}
