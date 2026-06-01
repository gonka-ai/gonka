package bls

import (
	"errors"
	"fmt"

	"decentralized-api/cosmosclient/tx_manager"
)

var ErrOperationQueuedForRetry = errors.New("bls operation queued for async retry")

func isQueuedForRetry(err error) bool {
	return errors.Is(err, tx_manager.ErrTxQueuedForRetry)
}

func queuedForRetryError(operation string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrOperationQueuedForRetry, operation, err)
}
