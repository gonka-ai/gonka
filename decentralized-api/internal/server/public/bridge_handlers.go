package public

import (
	"context"
	"database/sql"
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	cosmos_client "decentralized-api/cosmosclient"

	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

// minBlocksToBootstrap is the number of blocks we buffer on a fresh (uninitialized)
// chain before sorting and choosing the minimum block number as the bootstrap origin.
// Buffering protects against network latency delivering a higher block before the
// lowest one (which would otherwise be permanently discarded as a duplicate).
const minBlocksToBootstrap = 6

// maxBufferedBlocksPerChain bounds the number of out-of-order blocks buffered per
// chain (M3). It prevents unbounded memory growth if latest+1 never arrives (deep
// gap or a withheld block). Sized well above the bootstrap threshold and the
// realistic in-flight segment depth.
const maxBufferedBlocksPerChain = 10000

// BridgeQueue manages an in-memory queue of blocks processed synchronously and in
// strict sequential order per chain. Blocks are committed to the Cosmos chain inside
// the HTTP request thread (no background workers); progress is mirrored to SQLite so
// the node bootstraps cleanly after restarts.
type BridgeQueue struct {
	pendingBlocks map[string]*BridgeBlock // Key: "chain:blockNumber"
	latestBlocks  map[string]uint64       // Key: "chain" -> latest processed block number (in memory)
	lock          sync.RWMutex            // Protects both maps (RWMutex: handshake GET reads under RLock)
	recorder      cosmosclient.CosmosMessageClient
	db            *sql.DB
}

// PostBlockResponse is returned by the admin POST handler on success.
type PostBlockResponse struct {
	Status        string `json:"status"`
	Message       string `json:"message"`
	BlockNumber   string `json:"blockNumber"`
	ReceiptsCount int    `json:"receiptsCount"`
	QueueSize     int    `json:"queueSize"`
}

// BridgeStatusResponse represents the current status of the bridge queue.
type BridgeStatusResponse struct {
	PendingBlocksCount   int            `json:"pendingBlocksCount"`
	PendingReceiptsCount int            `json:"pendingReceiptsCount"`
	BlockCountByNumber   map[string]int `json:"blockCountByNumber"`
	EarliestBlockNumber  uint64         `json:"earliestBlockNumber"`
	LatestBlockNumber    uint64         `json:"latestBlockNumber"`
}

// BridgeAddressesResponse returns bridge contract addresses for a chain.
type BridgeAddressesResponse struct {
	ChainName string   `json:"chain_name"`
	ChainID   string   `json:"chain_id"`
	Addresses []string `json:"addresses"`
}

// LatestBridgeBlockResponse is the handshake payload served by GET /v1/bridge/block/latest.
type LatestBridgeBlockResponse struct {
	ChainId     string `json:"chainId"`
	BlockNumber uint64 `json:"blockNumber"`
}

// NewBlockQueue creates a new queue for blocks with receipts. The latest processed
// block numbers per chain are loaded once from SQLite at startup into the in-memory
// latestBlocks map, so subsequent duplicate/out-of-order checks never touch disk.
func NewBlockQueue(recorder cosmosclient.CosmosMessageClient, db *sql.DB) *BridgeQueue {
	q := &BridgeQueue{
		pendingBlocks: make(map[string]*BridgeBlock),
		latestBlocks:  make(map[string]uint64),
		recorder:      recorder,
		db:            db,
	}

	// Load persisted bridge states from SQLite once on startup.
	if db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		states, err := apiconfig.LoadAllBridgeLatestBlocks(ctx, db)
		if err != nil {
			slog.Error("Failed to load bridge states at startup", "err", err)
		} else {
			q.latestBlocks = states
			slog.Info("Loaded bridge states at startup", "states", states)
		}
	}

	return q
}

// GetLatestBlock returns the latest processed block for a chain (handshake read
// path). It is the only safe way for the public GET handler to read latestBlocks,
// which the admin POST handler writes concurrently.
func (q *BridgeQueue) GetLatestBlock(chain string) (uint64, bool) {
	q.lock.RLock()
	defer q.lock.RUnlock()
	v, ok := q.latestBlocks[chain]
	return v, ok
}

// AddBlock adds a block and processes it synchronously if it is the next in
// sequence. It enforces a strict sequential invariant (latest+1) per chain.
// Returns the block number echoed back, or an error to signal a fail-closed state
// (the caller must surface this as HTTP 500 so Geth halts and retries).
func (q *BridgeQueue) AddBlock(block BridgeBlock) (string, error) {
	q.lock.Lock()
	defer q.lock.Unlock()

	blockNum, err := strconv.ParseUint(block.BlockNumber, 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid block number %q: %w", block.BlockNumber, err)
	}

	latest, exists := q.latestBlocks[block.OriginChain]
	justBootstrapped := false

	// Case A: Uninitialized chain (bootstrapping / first run).
	if !exists {
		key := fmt.Sprintf("%s:%d", block.OriginChain, blockNum)
		q.pendingBlocks[key] = &block
		slog.Info("Buffered block during bootstrapping", "chain", block.OriginChain, "block", blockNum)

		// Count buffered blocks for this chain.
		var chainBlocks []uint64
		for _, pBlock := range q.pendingBlocks {
			if pBlock.OriginChain == block.OriginChain {
				if val, err := strconv.ParseUint(pBlock.BlockNumber, 10, 64); err == nil {
					chainBlocks = append(chainBlocks, val)
				}
			}
		}

		// Wait until we have enough blocks to establish correct ordering.
		if len(chainBlocks) < minBlocksToBootstrap {
			slog.Info("Awaiting more blocks to bootstrap bridge state",
				"chain", block.OriginChain, "count", len(chainBlocks), "target", minBlocksToBootstrap)
			return block.BlockNumber, nil
		}

		// Sort to find the minimum block number.
		sort.Slice(chainBlocks, func(i, j int) bool { return chainBlocks[i] < chainBlocks[j] })
		minBlock := chainBlocks[0]

		// Bootstrap latest to minBlock - 1 (or 0 if minBlock == 0).
		bootstrapVal := uint64(0)
		if minBlock > 0 {
			bootstrapVal = minBlock - 1
		}
		slog.Info("Bootstrapping bridge state from buffered blocks", "chain", block.OriginChain, "val", bootstrapVal)

		// Commit the bootstrapped origin immediately, BEFORE processing any block.
		// If the first block (latest+1) later fails on Cosmos, the chain is already
		// marked initialized at `latest`, so Geth's retry is recognized as latest+1
		// rather than re-entering the bootstrapping buffer loop.
		if err := apiconfig.SetBridgeLatestBlock(context.Background(), q.db, block.OriginChain, bootstrapVal); err != nil {
			return "", fmt.Errorf("failed to bootstrap SQLite state: %w", err)
		}
		q.latestBlocks[block.OriginChain] = bootstrapVal
		latest = bootstrapVal
		justBootstrapped = true
	}

	// Case B: Initialized-chain guards.
	// NOTE (C3): skip these when we just bootstrapped. minBlock = latest+1 is already
	// buffered, so we go straight to the drain loop below. Re-applying the out-of-order
	// guard to the (arbitrary) triggering block would stall the queue at `latest`.
	if !justBootstrapped {
		if blockNum <= latest {
			slog.Info("Discarding duplicate block", "block", blockNum, "latest", latest)
			return block.BlockNumber, nil
		}
		if blockNum > latest+1 {
			// Out-of-order: buffer (enforce per-chain cap, M3) and wait.
			if q.countPendingForChain(block.OriginChain) >= maxBufferedBlocksPerChain {
				slog.Warn("Dropping out-of-order block: per-chain buffer cap reached",
					"chain", block.OriginChain, "block", blockNum, "cap", maxBufferedBlocksPerChain)
				return block.BlockNumber, nil
			}
			key := fmt.Sprintf("%s:%d", block.OriginChain, blockNum)
			q.pendingBlocks[key] = &block
			slog.Info("Buffered out-of-order block", "block", blockNum, "expected", latest+1)
			return block.BlockNumber, nil
		}
		// Sequential: ensure the incoming block is buffered for the unified drain.
		q.pendingBlocks[fmt.Sprintf("%s:%d", block.OriginChain, blockNum)] = &block
	}

	// Drain consecutive blocks starting at latest+1 (NOT at the incoming blockNum).
	currBlockNum := latest + 1
	for {
		key := fmt.Sprintf("%s:%d", block.OriginChain, currBlockNum)
		nextBlock, ok := q.pendingBlocks[key]
		if !ok {
			break
		}

		slog.Info("Processing sequential block", "chain", nextBlock.OriginChain, "block", nextBlock.BlockNumber)
		if err := q.processBlockReceipts(nextBlock); err != nil {
			// Fail-closed: halt pipeline and propagate error, leaving the queue
			// unmodified and uncommitted so the block is retried in the same state.
			return "", err
		}

		// Commit progress to SQLite and update in-memory state.
		if err := apiconfig.SetBridgeLatestBlock(context.Background(), q.db, block.OriginChain, currBlockNum); err != nil {
			return "", fmt.Errorf("failed to save latest block: %w", err)
		}
		q.latestBlocks[block.OriginChain] = currBlockNum

		delete(q.pendingBlocks, key)
		currBlockNum++
	}

	return block.BlockNumber, nil
}

// countPendingForChain returns the number of buffered blocks for a chain.
// Caller must hold q.lock.
func (q *BridgeQueue) countPendingForChain(chain string) int {
	count := 0
	for _, pBlock := range q.pendingBlocks {
		if pBlock.OriginChain == chain {
			count++
		}
	}
	return count
}

// GetPendingBlocks returns all pending blocks.
func (q *BridgeQueue) GetPendingBlocks() []BridgeBlock {
	q.lock.RLock()
	defer q.lock.RUnlock()

	result := make([]BridgeBlock, 0, len(q.pendingBlocks))
	for _, block := range q.pendingBlocks {
		result = append(result, *block)
	}

	return result
}

func (q *BridgeQueue) GetQueueSize() int {
	q.lock.RLock()
	defer q.lock.RUnlock()
	return len(q.pendingBlocks)
}

// processBlockReceipts processes every receipt in a block, returning the first error.
func (q *BridgeQueue) processBlockReceipts(block *BridgeBlock) error {
	for _, receipt := range block.Receipts {
		if err := q.processReceipt(receipt, *block); err != nil {
			return err
		}
	}
	return nil
}

// processReceipt handles an individual receipt by submitting a MsgBridgeExchange to
// Cosmos. It returns any error so the synchronous pipeline can fail closed.
func (q *BridgeQueue) processReceipt(receipt BridgeReceipt, block BridgeBlock) error {
	slog.Info("Processing receipt",
		"chain", block.OriginChain,
		"contract", receipt.ContractAddress,
		"owner", receipt.OwnerAddress,
		"publicKey", receipt.OwnerPubKey,
		"amount", receipt.Amount,
		"blockNumber", block.BlockNumber,
		"receiptIndex", receipt.ReceiptIndex)

	cosmosAddress, err := cosmos_client.PubKeyToAddress(receipt.OwnerPubKey)
	if err != nil {
		return fmt.Errorf("failed to derive Cosmos address from public key %q: %w", receipt.OwnerPubKey, err)
	}

	ownerPubKey := receipt.OwnerPubKey
	if !strings.HasPrefix(ownerPubKey, "0x") {
		ownerPubKey = "0x" + ownerPubKey
	}

	msg := &types.MsgBridgeExchange{
		Validator:       q.recorder.GetAccountAddress(),
		OriginChain:     block.OriginChain,
		ContractAddress: receipt.ContractAddress,
		OwnerAddress:    cosmosAddress,
		OwnerPubKey:     ownerPubKey,
		Amount:          receipt.Amount,
		BlockNumber:     block.BlockNumber,
		ReceiptIndex:    receipt.ReceiptIndex,
		ReceiptsRoot:    block.ReceiptsRoot,
	}

	if err := q.recorder.BridgeExchange(msg); err != nil {
		return fmt.Errorf("cosmos transaction failed: %w", err)
	}
	return nil
}

// getLatestBridgeBlock serves the Geth startup/continuity handshake. It returns ONLY
// the unified `blockNumber` key (plus `chainId`). When a chain is uninitialized the
// blockNumber is omitted so Geth's tri-state parser treats it as "uninitialized" and
// falls back to legacy behavior.
func (s *Server) getLatestBridgeBlock(c echo.Context) error {
	chain := c.QueryParam("chain")
	if chain == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Chain parameter is required (e.g., 'ethereum')")
	}
	latest, ok := s.blockQueue.GetLatestBlock(chain)
	if !ok {
		// Uninitialized: omit blockNumber so Geth falls back to legacy.
		return c.JSON(http.StatusOK, map[string]string{"chainId": chain})
	}
	return c.JSON(http.StatusOK, &LatestBridgeBlockResponse{ChainId: chain, BlockNumber: latest})
}

// getBridgeStatus returns information about the queue status.
func (s *Server) getBridgeStatus(c echo.Context) error {
	pendingBlocks := s.blockQueue.GetPendingBlocks()

	// Group blocks by number.
	blockCountByNumber := make(map[string]int)

	// Track earliest and latest block numbers.
	var blockNumbers []uint64

	for _, block := range pendingBlocks {
		blockNum := block.BlockNumber
		blockCountByNumber[blockNum]++

		if parsed, err := strconv.ParseUint(block.BlockNumber, 10, 64); err == nil {
			blockNumbers = append(blockNumbers, parsed)
		}
	}

	var earliestBlock, latestBlock uint64
	if len(blockNumbers) > 0 {
		sort.Slice(blockNumbers, func(i, j int) bool { return blockNumbers[i] < blockNumbers[j] })
		earliestBlock = blockNumbers[0]
		latestBlock = blockNumbers[len(blockNumbers)-1]
	}

	totalReceipts := 0
	for _, block := range pendingBlocks {
		totalReceipts += len(block.Receipts)
	}

	response := &BridgeStatusResponse{
		PendingBlocksCount:   len(pendingBlocks),
		PendingReceiptsCount: totalReceipts,
		BlockCountByNumber:   blockCountByNumber,
		EarliestBlockNumber:  earliestBlock,
		LatestBlockNumber:    latestBlock,
	}

	return c.JSON(http.StatusOK, response)
}

// getBridgeAddresses returns bridge addresses for a specific chain by name.
func (s *Server) getBridgeAddresses(c echo.Context) error {
	chainName := c.QueryParam("chain")

	if chainName == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Chain parameter is required (e.g., 'ethereum', 'polygon')")
	}

	// Use chainName directly as chainId.
	chainId := chainName

	addresses, err := s.recorder.GetBridgeAddresses(c.Request().Context(), chainId)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, fmt.Sprintf("Failed to get addresses for chain '%s': %v", chainName, err))
	}

	var addressList []string
	for _, item := range addresses {
		addressList = append(addressList, item.Address)
	}

	return c.JSON(http.StatusOK, &BridgeAddressesResponse{
		ChainName: chainName,
		ChainID:   chainId,
		Addresses: addressList,
	})
}
