package public

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

// NewBridgeTransactionQueue creates a new queue for bridge transactions
func NewBridgeTransactionQueue() *BridgeTransactionQueue {
	queue := &BridgeTransactionQueue{
		pendingTransactions: make(map[string]*PendingBridgeTransaction),
		finalizationCh:      make(chan BlockFinalizationData, 100),
	}

	// Start the queue processor
	go queue.processQueue()

	return queue
}

// Add adds a transaction to the pending queue
func (q *BridgeTransactionQueue) Add(tx BridgeTransaction) string {
	txIndex := fmt.Sprintf("%s-%s", tx.BlockNumber, tx.ReceiptIndex)

	q.lock.Lock()
	defer q.lock.Unlock()

	// Check if already exists
	if _, exists := q.pendingTransactions[txIndex]; exists {
		slog.Info("Transaction already in queue", "txIndex", txIndex)
		return txIndex
	}

	q.pendingTransactions[txIndex] = &PendingBridgeTransaction{
		Transaction:    tx,
		SubmittedAt:    time.Now(),
		BlockFinalized: false,
	}

	slog.Info("Added transaction to pending queue", "txIndex", txIndex)
	return txIndex
}

// MarkAsFinalized marks transactions from a specific block as finalized if receipts root matches
func (q *BridgeTransactionQueue) MarkAsFinalized(blockNumber string, receiptsRoot string) int {
	q.finalizationCh <- BlockFinalizationData{
		BlockNumber:  blockNumber,
		ReceiptsRoot: receiptsRoot,
	}
	return 0 // Async operation, actual count will be logged in processQueue
}

// GetPendingTransactions returns all pending transactions
func (q *BridgeTransactionQueue) GetPendingTransactions() []PendingBridgeTransaction {
	q.lock.RLock()
	defer q.lock.RUnlock()

	result := make([]PendingBridgeTransaction, 0, len(q.pendingTransactions))
	for _, tx := range q.pendingTransactions {
		result = append(result, *tx)
	}

	return result
}

// ProcessQueue continuously processes the queue and handles finalization data
func (q *BridgeTransactionQueue) processQueue() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case finData := <-q.finalizationCh:
			count := q.handleFinalizationData(finData)
			slog.Info("Processed block finalization",
				"blockNumber", finData.BlockNumber,
				"transactionsFinalized", count)

			// After processing finalization data, try to process any finalized transactions
			q.processFinalizedTransactions()

		case <-ticker.C:
			// Clean up old transactions
			q.cleanupOldTransactions()
		}
	}
}

// handleFinalizationData processes block finalization data and returns count of affected transactions
func (q *BridgeTransactionQueue) handleFinalizationData(data BlockFinalizationData) int {
	q.lock.Lock()
	defer q.lock.Unlock()

	count := 0
	invalidCount := 0

	// First identify all transactions in this block
	blockTxs := make(map[string]*PendingBridgeTransaction)
	for txIndex, pendingTx := range q.pendingTransactions {
		if pendingTx.Transaction.BlockNumber == data.BlockNumber {
			blockTxs[txIndex] = pendingTx
		}
	}

	slog.Info("Processing finalization for block",
		"blockNumber", data.BlockNumber,
		"receiptsRoot", data.ReceiptsRoot,
		"transactionsInBlock", len(blockTxs))

	if len(blockTxs) == 0 {
		slog.Info("No transactions found for block",
			"blockNumber", data.BlockNumber)
		return 0
	}

	// Process transactions in this block
	for txIndex, pendingTx := range blockTxs {
		// If receipts root matches, mark as finalized
		if pendingTx.Transaction.ReceiptsRoot == data.ReceiptsRoot {
			pendingTx.BlockFinalized = true
			count++
			slog.Info("Marked transaction as finalized",
				"txIndex", txIndex,
				"blockNumber", data.BlockNumber)
		} else {
			// Receipts root doesn't match, this is an invalid receipt
			slog.Warn("Removing invalid transaction with receipts root mismatch",
				"txIndex", txIndex,
				"txReceiptsRoot", pendingTx.Transaction.ReceiptsRoot,
				"finalizedReceiptsRoot", data.ReceiptsRoot)

			// Remove it directly
			delete(q.pendingTransactions, txIndex)
			invalidCount++
		}
	}

	slog.Info("Finalization complete",
		"blockNumber", data.BlockNumber,
		"receiptsRoot", data.ReceiptsRoot,
		"validTransactions", count,
		"invalidTransactionsRemoved", invalidCount)

	return count
}

// processFinalizedTransactions processes any transactions that have been marked as finalized
func (q *BridgeTransactionQueue) processFinalizedTransactions() {
	for {
		tx, exists := q.GetNextFinalizedTransaction()
		if !exists {
			break
		}

		// Process the transaction (e.g., create Cosmos transaction)
		slog.Info("Processing finalized transaction",
			"chain", tx.OriginChain,
			"contract", tx.ContractAddress,
			"owner", tx.OwnerAddress,
			"amount", tx.Amount,
			"blockNumber", tx.BlockNumber,
			"receiptIndex", tx.ReceiptIndex)

		// TODO: Implement actual transaction processing logic here
		// This could involve creating and sending a Cosmos transaction
	}
}

// GetNextFinalizedTransaction gets the next finalized transaction ready for processing
func (q *BridgeTransactionQueue) GetNextFinalizedTransaction() (BridgeTransaction, bool) {
	q.lock.Lock()
	defer q.lock.Unlock()

	// Create a slice to hold all finalized transactions
	var finalizedTxs []struct {
		txIndex string
		tx      BridgeTransaction
	}

	// Collect all finalized transactions
	for txIndex, pendingTx := range q.pendingTransactions {
		if pendingTx.BlockFinalized {
			finalizedTxs = append(finalizedTxs, struct {
				txIndex string
				tx      BridgeTransaction
			}{
				txIndex: txIndex,
				tx:      pendingTx.Transaction,
			})
		}
	}

	if len(finalizedTxs) == 0 {
		return BridgeTransaction{}, false
	}

	// Sort transactions by block number and receipt index
	sort.Slice(finalizedTxs, func(i, j int) bool {
		// First compare block numbers
		blockNumI, errI := strconv.ParseUint(finalizedTxs[i].tx.BlockNumber, 10, 64)
		blockNumJ, errJ := strconv.ParseUint(finalizedTxs[j].tx.BlockNumber, 10, 64)

		// If block numbers are different, sort by block number
		if errI == nil && errJ == nil && blockNumI != blockNumJ {
			return blockNumI < blockNumJ
		}

		// If block numbers are the same, sort by receipt index
		indexI, errI := strconv.ParseUint(finalizedTxs[i].tx.ReceiptIndex, 10, 64)
		indexJ, errJ := strconv.ParseUint(finalizedTxs[j].tx.ReceiptIndex, 10, 64)

		if errI == nil && errJ == nil {
			return indexI < indexJ
		}

		// If parsing fails, fall back to string comparison
		return finalizedTxs[i].tx.ReceiptIndex < finalizedTxs[j].tx.ReceiptIndex
	})

	// Get the first transaction after sorting
	firstTx := finalizedTxs[0]

	// Remove it from the queue
	delete(q.pendingTransactions, firstTx.txIndex)

	slog.Info("Processing finalized transaction in order",
		"txIndex", firstTx.txIndex,
		"blockNumber", firstTx.tx.BlockNumber,
		"receiptIndex", firstTx.tx.ReceiptIndex)

	return firstTx.tx, true
}

// cleanupOldTransactions removes transactions that are too old or have too many failed verifications
func (q *BridgeTransactionQueue) cleanupOldTransactions() {
	q.lock.Lock()
	defer q.lock.Unlock()

	now := time.Now()
	maxAge := 24 * time.Hour // Max age is 24 hours

	for txIndex, pendingTx := range q.pendingTransactions {
		age := now.Sub(pendingTx.SubmittedAt)

		if age > maxAge {
			slog.Warn("Removing old transaction from queue",
				"txIndex", txIndex,
				"age", age.String())
			delete(q.pendingTransactions, txIndex)
		}
	}
}

// Create a global instance of the queue
var transactionQueue *BridgeTransactionQueue

// postBridge handles POST requests to submit new bridge transactions
func (s *Server) postBridge(c echo.Context) error {
	var bridgeTx BridgeTransaction
	if err := c.Bind(&bridgeTx); err != nil {
		slog.Error("Failed to decode bridge transaction", "error", err)
		return c.JSON(400, map[string]string{"error": "Invalid request body: " + err.Error()})
	}

	// Validate required fields
	if bridgeTx.OriginChain == "" || bridgeTx.ContractAddress == "" ||
		bridgeTx.OwnerAddress == "" || bridgeTx.Amount == "" || bridgeTx.BlockNumber == "" ||
		bridgeTx.ReceiptIndex == "" || bridgeTx.ReceiptsRoot == "" {
		return c.JSON(400, map[string]string{"error": "All fields are required: chain, contract, owner, amount, blockNumber, receiptIndex, receiptsRoot"})
	}

	slog.Info("Received bridge transaction",
		"chain", bridgeTx.OriginChain,
		"contract", bridgeTx.ContractAddress,
		"owner", bridgeTx.OwnerAddress,
		"amount", bridgeTx.Amount,
		"blockNumber", bridgeTx.BlockNumber,
		"receiptIndex", bridgeTx.ReceiptIndex,
		"receiptsRoot", bridgeTx.ReceiptsRoot)

	// Add to the queue
	txIndex := transactionQueue.Add(bridgeTx)

	// Return success response
	return c.JSON(200, map[string]string{
		"status":  "pending",
		"message": "Bridge transaction queued for processing",
		"txIndex": txIndex,
	})
}

// patchBridge handles PATCH requests to submit block finalization data
func (s *Server) patchBridge(c echo.Context) error {
	var finData BlockFinalizationData
	if err := c.Bind(&finData); err != nil {
		slog.Error("Failed to decode finalization data", "error", err)
		return c.JSON(400, map[string]string{"error": "Invalid request body: " + err.Error()})
	}

	// Validate required fields
	if finData.BlockNumber == "" || finData.ReceiptsRoot == "" {
		return c.JSON(400, map[string]string{"error": "All fields are required: blockNumber, receiptsRoot"})
	}

	slog.Info("Received block finalization data",
		"blockNumber", finData.BlockNumber,
		"receiptsRoot", finData.ReceiptsRoot)

	// Count transactions in the specified block before processing
	pendingTx := transactionQueue.GetPendingTransactions()
	blockTxCount := 0
	finalizedCount := 0
	for _, tx := range pendingTx {
		if tx.Transaction.BlockNumber == finData.BlockNumber {
			blockTxCount++
			if tx.BlockFinalized {
				finalizedCount++
			}
		}
	}

	// Mark matching transactions as finalized and remove invalid ones
	transactionQueue.MarkAsFinalized(finData.BlockNumber, finData.ReceiptsRoot)

	// Log information about the background processing
	slog.Info("Block finalization scheduled. Cosmos transactions will be processed in background.",
		"blockNumber", finData.BlockNumber,
		"transactionsInBlock", blockTxCount,
		"alreadyFinalized", finalizedCount,
		"backgroundProcessDelay", "30 seconds maximum")

	// Return success response with more details
	return c.JSON(200, map[string]interface{}{
		"status":            "success",
		"message":           "Block finalization data processed",
		"blockNumber":       finData.BlockNumber,
		"receiptRoot":       finData.ReceiptsRoot,
		"receiptsInBlock":   blockTxCount,
		"totalPendingCount": len(pendingTx),
	})
}

// getBridgeStatus returns information about the pending transactions queue
func (s *Server) getBridgeStatus(c echo.Context) error {
	pendingTx := transactionQueue.GetPendingTransactions()

	// Group transactions by block number and receipts root
	blockGroups := make(map[string]int)
	rootGroups := make(map[string]int)

	// For each block number, track unique receipts roots to identify potential conflicts
	blockRoots := make(map[string]map[string]int)

	for _, tx := range pendingTx {
		blockNum := tx.Transaction.BlockNumber
		receiptsRoot := tx.Transaction.ReceiptsRoot

		// Count by block number
		blockGroups[blockNum]++

		// Count by receipts root
		rootGroups[receiptsRoot]++

		// Track roots per block
		if _, exists := blockRoots[blockNum]; !exists {
			blockRoots[blockNum] = make(map[string]int)
		}
		blockRoots[blockNum][receiptsRoot]++
	}

	// Identify blocks with conflicting receipts
	conflictBlocks := make([]map[string]interface{}, 0)
	for blockNum, roots := range blockRoots {
		if len(roots) > 1 {
			// This block has multiple different receipts roots - potential conflict
			rootCounts := make(map[string]int)
			for root, count := range roots {
				rootCounts[root] = count
			}

			conflictBlocks = append(conflictBlocks, map[string]interface{}{
				"blockNumber": blockNum,
				"rootCounts":  rootCounts,
			})
		}
	}

	// Count finalized vs non-finalized transactions
	finalizedCount := 0
	for _, tx := range pendingTx {
		if tx.BlockFinalized {
			finalizedCount++
		}
	}

	response := map[string]interface{}{
		"total":             len(pendingTx),
		"finalized":         finalizedCount,
		"pending":           len(pendingTx) - finalizedCount,
		"blockGroups":       blockGroups,
		"receiptRootGroups": rootGroups,
		"conflictingBlocks": conflictBlocks,
		"oldestTransaction": time.Time{},
	}

	// Find oldest transaction
	if len(pendingTx) > 0 {
		oldest := pendingTx[0].SubmittedAt
		for _, tx := range pendingTx {
			if tx.SubmittedAt.Before(oldest) {
				oldest = tx.SubmittedAt
			}
		}
		response["oldestTransaction"] = oldest
	}

	return c.JSON(200, response)
}
