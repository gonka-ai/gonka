package server

import (
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"
)

type BridgeTransaction struct {
	OriginChain     string `json:"chain"`        // Name of the origin chain (e.g., "ethereum")
	ContractAddress string `json:"contract"`     // Address of the smart contract on the origin chain
	OwnerAddress    string `json:"owner"`        // Address of the token owner on the origin chain
	Amount          string `json:"amount"`       // Amount of tokens to be bridged
	BlockNumber     string `json:"blockNumber"`  // Block number where the transaction occurred
	ReceiptIndex    string `json:"receiptIndex"` // Index of the transaction receipt in the block
	ReceiptsRoot    string `json:"receiptsRoot"` // Merkle root of receipts trie for transaction verification
}

// PendingBridgeTransaction represents a bridge transaction waiting for block finalization
type PendingBridgeTransaction struct {
	Transaction    BridgeTransaction `json:"transaction"`
	SubmittedAt    time.Time         `json:"submittedAt"`
	BlockFinalized bool              `json:"blockFinalized"`
}

// BlockFinalizationData represents data received to confirm that a block has been finalized
type BlockFinalizationData struct {
	BlockNumber  string `json:"blockNumber"`
	ReceiptsRoot string `json:"receiptsRoot"`
}

// BridgeTransactionQueue manages pending bridge transactions
type BridgeTransactionQueue struct {
	pendingTransactions map[string]*PendingBridgeTransaction // Key is blockNumber-receiptIndex
	lock                sync.RWMutex
	// Channel to notify about new finalization data
	finalizationCh chan BlockFinalizationData
}

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
