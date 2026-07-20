package public

import (
	"context"
	"database/sql"
	"decentralized-api/apiconfig"
	"common/utils"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal"
	"errors"

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

// ErrBridgeQueueFull is the single sentinel that signals back-pressure: the
// per-chain out-of-order buffer is full, so the block cannot be accepted right
// now. The admin POST handler maps ONLY this error to HTTP 503 so Geth's
// sendRangeDirectly stops and resends later; no block is ever dropped while
// reporting success.
var ErrBridgeQueueFull = errors.New("Bridge: Queue is full")

// Bridge commit-confirmation tuning. Confirmation is an index-independent
// module-state poll via the already-deployed BridgeTransaction query (no chain
// upgrade required), bounded so the drain never blocks forever on a receipt that
// is not (yet) recorded; on deadline the block is left in the queue and retried
// on the next POST.
const (
	bridgeConfirmTimeout      = 60 * time.Second
	bridgeConfirmPollInterval = 2 * time.Second
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
	drainMu       sync.Mutex              // Serializes drains; held WITHOUT the data lock during network I/O
	recorder      cosmosclient.CosmosMessageClient
	db            *sql.DB
	epochCache    *internal.EpochGroupDataCache
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
// The epoch cache is created internally from the recorder; it provides O(1) cached
// active-participant checks so the drain skips receipts while the validator is not
// in the active set (preventing queue stalls and unnecessary Cosmos broadcasts).
func NewBlockQueue(recorder cosmosclient.CosmosMessageClient, db *sql.DB) *BridgeQueue {
	q := &BridgeQueue{
		pendingBlocks: make(map[string]*BridgeBlock),
		latestBlocks:  make(map[string]uint64),
		recorder:      recorder,
		db:            db,
		epochCache:    internal.NewEpochGroupDataCache(recorder),
	}

	// Load persisted bridge states from SQLite once on startup.
	if db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		states, err := apiconfig.LoadAllBridgeLatestBlocks(ctx, db)
		if err != nil {
			slog.Error("Bridge: Failed to load bridge states at startup", "err", err)
		} else {
			q.latestBlocks = states
			slog.Info("Bridge: Loaded bridge states at startup", "states", states)
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

// AddBlock is a receipt-ACK: it accepts a block into the queue (ACCEPT phase,
// under the data lock) and then triggers the drain (DRAIN phase, under drainMu
// with NO data lock held during network I/O). The returned error means ONLY that
// the block could not be accepted:
//   - malformed input (block-number parse / bootstrap persistence failure), or
//   - back-pressure (ErrBridgeQueueFull, buffer full).
//
// Every received outcome (buffered, duplicate, bootstrapping) returns
// (blockNumber, nil). Commit success/failure is NOT reported through this call;
// it is owned by the drain, which advances `latest` only after every receipt of a
// block is confirmed recorded on-chain.
func (q *BridgeQueue) AddBlock(block BridgeBlock) (string, error) {
	if err := q.accept(block); err != nil {
		return "", err
	}
	q.drain(block.OriginChain)
	return block.BlockNumber, nil
}

// accept performs the ACCEPT phase under the data lock only: parse/validate,
// bootstrap, dedup, buffer, and back-pressure. It performs NO network I/O. The
// only quick disk write here is the bootstrap origin commit (SQLite), which is
// not network I/O and must be consistent with the in-memory bootstrap value.
func (q *BridgeQueue) accept(block BridgeBlock) error {
	q.lock.Lock()
	defer q.lock.Unlock()

	blockNum, err := strconv.ParseUint(block.BlockNumber, 10, 64)
	if err != nil {
		return fmt.Errorf("Bridge: Invalid block number %q: %w", block.BlockNumber, err)
	}

	latest, exists := q.latestBlocks[block.OriginChain]

	// Case A: Uninitialized chain (bootstrapping / first run).
	if !exists {
		key := fmt.Sprintf("%s:%d", block.OriginChain, blockNum)
		q.pendingBlocks[key] = &block
		slog.Info("Bridge: Buffered block during bootstrapping", "chain", block.OriginChain, "block", blockNum)

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
			slog.Info("Bridge: Awaiting more blocks to bootstrap bridge state",
				"chain", block.OriginChain, "count", len(chainBlocks), "target", minBlocksToBootstrap)
			return nil
		}

		// Sort to find the minimum block number.
		sort.Slice(chainBlocks, func(i, j int) bool { return chainBlocks[i] < chainBlocks[j] })
		minBlock := chainBlocks[0]

		// Bootstrap latest to minBlock - 1 (or 0 if minBlock == 0).
		bootstrapVal := uint64(0)
		if minBlock > 0 {
			bootstrapVal = minBlock - 1
		}
		slog.Info("Bridge: Bootstrapping bridge state from buffered blocks", "chain", block.OriginChain, "val", bootstrapVal)

		// Commit the bootstrapped origin immediately, BEFORE draining any block.
		// If the first block (latest+1) later fails to commit, the chain is already
		// marked initialized at `latest`, so Geth's retry is recognized as latest+1
		// rather than re-entering the bootstrapping buffer loop.
		if err := apiconfig.SetBridgeLatestBlock(context.Background(), q.db, block.OriginChain, bootstrapVal); err != nil {
			return fmt.Errorf("Bridge: failed to bootstrap SQLite state: %w", err)
		}
		q.latestBlocks[block.OriginChain] = bootstrapVal
		// minBlock = latest+1 is already buffered; the drain (started by AddBlock)
		// will pick it up at latest+1.
		return nil
	}

	// Case B: Initialized-chain guards.
	if blockNum <= latest {
		slog.Info("Bridge: Discarding duplicate block", "block", blockNum, "latest", latest)
		return nil
	}
	if blockNum > latest+1 {
		// Out-of-order: enforce the per-chain cap as BACK-PRESSURE (never drop).
		// A full buffer returns ErrBridgeQueueFull -> 503, so Geth stops and resends
		// later; back-pressure self-clears as the drain confirms blocks and frees
		// space. This is the ONLY place this sentinel is returned.
		if q.countPendingForChain(block.OriginChain) >= maxBufferedBlocksPerChain {
			return fmt.Errorf("%w: chain=%s pending=%d cap=%d",
				ErrBridgeQueueFull, block.OriginChain,
				q.countPendingForChain(block.OriginChain), maxBufferedBlocksPerChain)
		}
		key := fmt.Sprintf("%s:%d", block.OriginChain, blockNum)
		q.pendingBlocks[key] = &block
		slog.Info("Bridge: Buffered out-of-order block", "block", blockNum, "expected", latest+1)
		return nil
	}
	// Sequential: buffer the incoming block for the unified drain.
	q.pendingBlocks[fmt.Sprintf("%s:%d", block.OriginChain, blockNum)] = &block
	return nil
}

// drain commits buffered blocks from latest+1 in strict sequential order. It is
// serialized by drainMu (one drain at a time) and never holds the data lock
// across network I/O: the data lock is taken only for quick in-memory reads
// (peek latest+1) and the advance/delete write. A block whose receipts are not
// all confirmed is left in the queue and retried on the next POST's drain.
func (q *BridgeQueue) drain(chain string) {
	q.drainMu.Lock()
	defer q.drainMu.Unlock()

	ctx := context.Background()
	for {
		// Quick read under the data lock: copy the next block pointer out.
		q.lock.RLock()
		latest := q.latestBlocks[chain]
		key := fmt.Sprintf("%s:%d", chain, latest+1)
		nextBlock, ok := q.pendingBlocks[key]
		q.lock.RUnlock()
		if !ok {
			return
		}

		slog.Info("Bridge: Processing sequential block", "chain", chain, "block", nextBlock.BlockNumber)

		// Network I/O with NO lock held: broadcast + confirm each receipt.
		if err := q.processBlockReceipts(ctx, nextBlock); err != nil {
			// Not (yet) confirmed on-chain: leave the block buffered, do NOT advance
			// latest, do NOT surface to the POST. Retried on the next POST's drain.
			slog.Warn("Bridge: Block not yet committed; leaving in queue",
				"chain", chain, "block", latest+1, "err", err)
			return
		}

		// Quick write under the data lock: advance latest (SQLite + memory) and
		// delete the committed block.
		q.lock.Lock()
		if err := apiconfig.SetBridgeLatestBlock(ctx, q.db, chain, latest+1); err != nil {
			q.lock.Unlock()
			slog.Error("Bridge: Failed to persist latest block; not advancing latest pointer",
				"chain", chain, "block", latest+1, "err", err)
			return
		}
		q.latestBlocks[chain] = latest + 1
		delete(q.pendingBlocks, key)
		q.lock.Unlock()
	}
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

// checkValidatorActive checks whether the local validator is in the active set
// for the current epoch. Returns (active, err): active=true means broadcast,
// active=false+nil means skip & advance, non-nil error means the node can't
// reach the chain so the drain should pause and retry later.
func (q *BridgeQueue) checkValidatorActive(ctx context.Context) (bool, error) {
	queryClient := q.recorder.NewInferenceQueryClient()
	resp, err := queryClient.GetCurrentEpoch(ctx, &types.QueryGetCurrentEpochRequest{})
	if err != nil {
		return false, fmt.Errorf("failed to resolve current epoch: %w", err)
	}
	return q.epochCache.IsActiveParticipant(ctx, resp.Epoch, q.recorder.GetAccountAddress())
}

// processBlockReceipts commits every receipt in a block. Three outcomes:
//   - active validator: broadcast + confirm each receipt (normal path).
//   - not active: skip receipts and advance latest — other validators handle them.
//   - epoch query error: pause the drain (return error), retry on next POST.
func (q *BridgeQueue) processBlockReceipts(ctx context.Context, block *BridgeBlock) error {
	if len(block.Receipts) > 0 {
		active, err := q.checkValidatorActive(ctx)
		if err != nil {
			return fmt.Errorf("Bridge: Cannot determine active status, pausing drain: %w", err)
		}
		if !active {
			slog.Info("Bridge: Validator not active; skipping receipts and advancing",
				"block", block.BlockNumber,
				"receipts", len(block.Receipts))
			return nil
		}
	}
	for _, receipt := range block.Receipts {
		if err := q.commitReceipt(ctx, receipt, *block); err != nil {
			return err
		}
	}
	return nil
}

// commitReceipt sends a receipt optimistically (no pre-check) and then treats an
// index-independent module-state read as the AUTHORITY for whether it is
// committed. The chain's "already validated" dedup response is treated as success
// (so re-sent blocks make forward progress instead of stalling on dedup errors).
func (q *BridgeQueue) commitReceipt(ctx context.Context, receipt BridgeReceipt, block BridgeBlock) error {
	msg, err := q.buildBridgeExchangeMsg(receipt, block)
	if err != nil {
		return err
	}

	slog.Info("Bridge: Committing receipt",
		"chain", block.OriginChain,
		"contract", receipt.ContractAddress,
		"owner", msg.OwnerAddress,
		"amount", receipt.Amount,
		"blockNumber", block.BlockNumber,
		"receiptIndex", receipt.ReceiptIndex)

	// 1. Send optimistically. "already recorded by us" is success, not failure;
	// any other error is uncertain -> stop and retry on the next POST. We do NOT
	// rely on the broadcast's own nil (broadcastMessage can swallow Code != 0);
	// the confirmation read below is the authority.
	if err := q.recorder.BridgeExchange(msg); err != nil && !isAlreadyRecorded(err) {
		return fmt.Errorf("Bridge: Exchange submit not confirmable: %w", err)
	}

	// 2. Confirm via index-independent state read (bounded poll).
	return q.confirmReceiptRecorded(ctx, msg)
}

// buildBridgeExchangeMsg builds the MsgBridgeExchange for a receipt, deriving the
// owner's Cosmos address from its public key. The same content fields feed the
// confirmation query, so the on-chain content hash matches exactly.
func (q *BridgeQueue) buildBridgeExchangeMsg(receipt BridgeReceipt, block BridgeBlock) (*types.MsgBridgeExchange, error) {
	cosmosAddress, err := utils.PubKeyToAddress(receipt.OwnerPubKey)
	if err != nil {
		return nil, fmt.Errorf("Bridge: Failed to derive Cosmos address from public key %q: %w", receipt.OwnerPubKey, err)
	}

	ownerPubKey := receipt.OwnerPubKey
	if !strings.HasPrefix(ownerPubKey, "0x") {
		ownerPubKey = "0x" + ownerPubKey
	}

	return &types.MsgBridgeExchange{
		Validator:       q.recorder.GetAccountAddress(),
		OriginChain:     block.OriginChain,
		ContractAddress: receipt.ContractAddress,
		OwnerAddress:    cosmosAddress,
		OwnerPubKey:     ownerPubKey,
		Amount:          receipt.Amount,
		BlockNumber:     block.BlockNumber,
		ReceiptIndex:    receipt.ReceiptIndex,
		ReceiptsRoot:    block.ReceiptsRoot,
	}, nil
}

// confirmReceiptRecorded polls the chain (via the already-deployed, index-
// independent BridgeTransaction query) until our validator's confirmation for
// this exact receipt content is present, or a bounded deadline elapses. A
// deadline is "uncertain" -> the receipt is left uncommitted and retried on the
// next POST's drain.
func (q *BridgeQueue) confirmReceiptRecorded(ctx context.Context, msg *types.MsgBridgeExchange) error {
	deadline := time.Now().Add(bridgeConfirmTimeout)
	for {
		recorded, err := q.receiptRecorded(ctx, msg)
		if err == nil && recorded {
			return nil
		}
		if err != nil {
			slog.Warn("Bridge: Confirmation query failed; will retry",
				"blockNumber", msg.BlockNumber, "receiptIndex", msg.ReceiptIndex, "err", err)
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("Bridge: Receipt not confirmed within %s: chain=%s block=%s receiptIndex=%s",
				bridgeConfirmTimeout, msg.OriginChain, msg.BlockNumber, msg.ReceiptIndex)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(bridgeConfirmPollInterval):
		}
	}
}

// receiptRecorded reports whether our validator's confirmation for this exact
// receipt content is present in chain state. It uses the existing
// BridgeTransaction query (filtered by chain/block/receiptIndex) and then matches
// the remaining content fields, so a conflicting transaction (same receipt
// location, different content) cannot be mistaken for ours.
func (q *BridgeQueue) receiptRecorded(ctx context.Context, msg *types.MsgBridgeExchange) (bool, error) {
	txs, err := q.recorder.BridgeTransactionsByReceipt(ctx, msg.OriginChain, msg.BlockNumber, msg.ReceiptIndex)
	if err != nil {
		return false, err
	}
	for _, tx := range txs {
		if !bridgeTxMatchesMsg(tx, msg) {
			continue
		}
		for _, v := range tx.Validators {
			if v == msg.Validator {
				return true, nil
			}
		}
	}
	return false, nil
}

// bridgeTxMatchesMsg reports whether an on-chain bridge transaction has the same
// content as our message. The query already filters by chain/block/receiptIndex;
// matching the remaining content fields guards against confirming on a conflicting
// entry. ContractAddress is compared case-insensitively because the chain
// normalizes it to lowercase (see msg_server_bridge_exchange.go).
func bridgeTxMatchesMsg(tx types.BridgeTransaction, msg *types.MsgBridgeExchange) bool {
	return tx.ChainId == msg.OriginChain &&
		strings.EqualFold(tx.ContractAddress, msg.ContractAddress) &&
		tx.OwnerAddress == msg.OwnerAddress &&
		tx.Amount == msg.Amount &&
		tx.BlockNumber == msg.BlockNumber &&
		tx.ReceiptIndex == msg.ReceiptIndex &&
		tx.ReceiptsRoot == msg.ReceiptsRoot
}

// isAlreadyRecorded reports whether the broadcast error is the chain's dedup
// signal that our validator already recorded this exact receipt. This is treated
// as success (§1.3). The string match is fragile and only an optimization to skip
// a redundant poll; the confirmation read remains the authority.
func isAlreadyRecorded(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "Bridge: Validator has already validated this transaction")
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
