package public

import (
	"decentralized-api/cosmosclient"
	cosmos_client "decentralized-api/cosmosclient"

	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

// BlockQueue manages a queue of blocks to be processed
type BridgeQueue struct {
	pendingBlocks             map[string]*BridgeBlock // Key is blockNumber
	lock                      sync.RWMutex
	minBlocksBeforeProcessing int // Minimum number of blocks needed before starting processing
	recorder                  cosmosclient.CosmosMessageClient
	processCh                 chan struct{} // Channel to signal processing is needed
}

// NewBlockQueue creates a new queue for blocks with receipts
func NewBlockQueue(recorder cosmosclient.CosmosMessageClient) *BridgeQueue {
	queue := &BridgeQueue{
		pendingBlocks:             make(map[string]*BridgeBlock),
		minBlocksBeforeProcessing: 6,
		recorder:                  recorder,
		processCh:                 make(chan struct{}, 1), // Buffered channel to prevent blocking
	}

	// Start the queue processor
	go queue.init()

	return queue
}

// AddBlock adds a block to the queue
func (q *BridgeQueue) AddBlock(block BridgeBlock) string {
	q.lock.Lock()
	defer q.lock.Unlock()

	// Check if block already exists
	if _, exists := q.pendingBlocks[block.BlockNumber]; exists {
		slog.Info("Block already in queue", "blockNumber", block.BlockNumber)
		return block.BlockNumber
	}

	q.pendingBlocks[block.BlockNumber] = &block

	slog.Info("Bridge queue: Added block as pending",
		"blockNumber", block.BlockNumber,
		"originChain", block.OriginChain,
		"receiptsCount", len(block.Receipts),
		"queueLength", len(q.pendingBlocks))

	// Signal processing if we have enough blocks
	if len(q.pendingBlocks) >= q.minBlocksBeforeProcessing {
		select {
		case q.processCh <- struct{}{}:
			// Signal sent successfully
		default:
			// Channel is full, processing is already queued
		}
	}

	return block.BlockNumber
}

// GetPendingBlocks returns all pending blocks
func (q *BridgeQueue) GetPendingBlocks() []BridgeBlock {
	q.lock.RLock()
	defer q.lock.RUnlock()

	result := make([]BridgeBlock, 0, len(q.pendingBlocks))
	for _, block := range q.pendingBlocks {
		result = append(result, *block)
	}

	return result
}

// Init sets up the queue processing
func (q *BridgeQueue) init() {
	ticker := time.NewTicker(5 * time.Minute) // Process every 5 minutes regardless
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			slog.Info("Bridge queue: Processing blocks due to timeout")
			q.processBlocks()
		case <-q.processCh:
			// Process blocks when minimum threshold is reached
			slog.Info("Bridge queue: Processing blocks due to minimum threshold reached")
			q.processBlocks()
		}
	}
}

// processBlocks processes queued blocks in order starting from the earliest
func (q *BridgeQueue) processBlocks() {
	for {
		block, exists := q.getNextBlock()
		if !exists {
			break
		}

		// Process the block and its receipts
		slog.Info("Processing block",
			"blockNumber", block.BlockNumber,
			"originChain", block.OriginChain,
			"receiptsRoot", block.ReceiptsRoot,
			"receiptsCount", len(block.Receipts))

		// Process each receipt in the block
		for _, receipt := range block.Receipts {
			// Process the receipt with block information
			q.processReceipt(receipt, block)
		}
	}
}

// getNextBlock retrieves and removes the earliest block from the queue
func (q *BridgeQueue) getNextBlock() (BridgeBlock, bool) {
	q.lock.Lock()
	defer q.lock.Unlock()

	if len(q.pendingBlocks) == 0 {
		return BridgeBlock{}, false
	}

	// Create a slice of all blocks
	var blocks []struct {
		blockNumber string
		block       BridgeBlock
	}

	for blockNum, pendingBlock := range q.pendingBlocks {
		blocks = append(blocks, struct {
			blockNumber string
			block       BridgeBlock
		}{
			blockNumber: blockNum,
			block:       *pendingBlock,
		})
	}

	// Sort blocks by block number (ascending)
	sort.Slice(blocks, func(i, j int) bool {
		blockNumI, errI := strconv.ParseUint(blocks[i].blockNumber, 10, 64)
		blockNumJ, errJ := strconv.ParseUint(blocks[j].blockNumber, 10, 64)

		// If parsing fails, fall back to string comparison
		if errI != nil || errJ != nil {
			return blocks[i].blockNumber < blocks[j].blockNumber
		}

		return blockNumI < blockNumJ
	})

	// Get the earliest block
	earliestBlock := blocks[0]

	// Remove it from the queue
	delete(q.pendingBlocks, earliestBlock.blockNumber)

	slog.Info("Retrieved next block for processing",
		"blockNumber", earliestBlock.blockNumber,
		"remainingBlocks", len(q.pendingBlocks))

	return earliestBlock.block, true
}

// processReceipt handles an individual receipt (similar to previous cosmos processing)
func (q *BridgeQueue) processReceipt(receipt BridgeReceipt, block BridgeBlock) {
	// Process the transaction (e.g., create Cosmos transaction)
	slog.Info("Processing receipt",
		"chain", block.OriginChain,
		"contract", receipt.ContractAddress,
		"owner", receipt.OwnerAddress,
		"publicKey", receipt.OwnerPubKey,
		"amount", receipt.Amount,
		"blockNumber", block.BlockNumber,
		"receiptIndex", receipt.ReceiptIndex)

	// Derive Cosmos address from public key
	cosmosAddress, err := cosmos_client.PubKeyToAddress(receipt.OwnerPubKey)
	if err != nil {
		slog.Error("Failed to derive Cosmos address from public key",
			"error", err,
			"publicKey", receipt.OwnerPubKey)
		return
	}

	// Format the public key with 0x prefix if it doesn't already have it
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

	err = q.recorder.BridgeExchange(msg)
	if err != nil {
		slog.Error("Error processing bridge exchange",
			"error", err,
			"blockNumber", block.BlockNumber,
			"receiptIndex", receipt.ReceiptIndex)
	}
}

// postBlock handles POST requests to submit finalized blocks with optional receipts
func (s *Server) postBlock(c echo.Context) error {
	// Debug: Log raw request body
	rawBody := c.Request().Body
	bodyBytes, err := io.ReadAll(rawBody)
	if err != nil {
		slog.Error("Failed to read request body", "error", err)
		return c.JSON(400, map[string]string{"error": "Failed to read request body"})
	}

	// Log the raw JSON for debugging
	slog.Info("Received raw request body", "body", string(bodyBytes))

	// Reset the body for binding
	c.Request().Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

	var blockData BridgeBlock
	if err := c.Bind(&blockData); err != nil {
		slog.Error("Failed to decode block data", "error", err)
		return c.JSON(400, map[string]string{"error": "Invalid request body: " + err.Error()})
	}

	// Validate required fields
	if blockData.BlockNumber == "" || blockData.ReceiptsRoot == "" || blockData.OriginChain == "" {
		return c.JSON(400, map[string]string{"error": "Required fields missing: blockNumber, receiptsRoot, originChain"})
	}

	slog.Info("Received finalized block",
		"blockNumber", blockData.BlockNumber,
		"originChain", blockData.OriginChain,
		"receiptsRoot", blockData.ReceiptsRoot,
		"receiptsCount", len(blockData.Receipts))

	// Debug: Log each receipt to see what we're actually receiving
	for i, receipt := range blockData.Receipts {
		slog.Info("Received receipt details",
			"receiptIndex", i,
			"contract", receipt.ContractAddress,
			"owner", receipt.OwnerAddress,
			"publicKey", receipt.OwnerPubKey,
			"publicKeyLength", len(receipt.OwnerPubKey),
			"amount", receipt.Amount,
			"receiptIndex", receipt.ReceiptIndex)
	}

	// Add the block to the queue
	blockNumber := s.blockQueue.AddBlock(blockData)

	// Return success response
	return c.JSON(200, map[string]interface{}{
		"status":        "success",
		"message":       "Block queued for processing",
		"blockNumber":   blockNumber,
		"receiptsCount": len(blockData.Receipts),
		"queueSize":     len(s.blockQueue.pendingBlocks),
	})
}

// getBridgeStatus returns information about the queue status
func (s *Server) getBridgeStatus(c echo.Context) error {
	pendingBlocks := s.blockQueue.GetPendingBlocks()

	// Group blocks by number
	blockCountByNumber := make(map[string]int)

	// Track earliest and latest block numbers
	var blockNumbers []uint64

	for _, block := range pendingBlocks {
		blockNum := block.BlockNumber
		blockCountByNumber[blockNum]++

		// Parse block number for sorting
		if blockNum, err := strconv.ParseUint(block.BlockNumber, 10, 64); err == nil {
			blockNumbers = append(blockNumbers, blockNum)
		}
	}

	var earliestBlock, latestBlock uint64
	var readyToProcess bool

	if len(blockNumbers) > 0 {
		// Sort the block numbers
		sort.Slice(blockNumbers, func(i, j int) bool {
			return blockNumbers[i] < blockNumbers[j]
		})

		earliestBlock = blockNumbers[0]
		latestBlock = blockNumbers[len(blockNumbers)-1]
		readyToProcess = len(blockNumbers) >= s.blockQueue.minBlocksBeforeProcessing
	}

	// Count total receipts in all blocks
	totalReceipts := 0
	for _, block := range pendingBlocks {
		totalReceipts += len(block.Receipts)
	}

	response := map[string]interface{}{
		"pendingBlocksCount":        len(pendingBlocks),
		"pendingReceiptsCount":      totalReceipts,
		"blockCountByNumber":        blockCountByNumber,
		"earliestBlockNumber":       earliestBlock,
		"latestBlockNumber":         latestBlock,
		"readyToProcess":            readyToProcess,
		"minBlocksBeforeProcessing": s.blockQueue.minBlocksBeforeProcessing,
	}

	return c.JSON(200, response)
}
