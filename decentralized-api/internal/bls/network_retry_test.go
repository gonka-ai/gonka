package bls

import (
	"errors"
	"fmt"
	"testing"

	"decentralized-api/cosmosclient/tx_manager"
)

func TestIsQueuedForRetry_MatchesQueuedSentinelOnly(t *testing.T) {
	t.Run("queued sentinel", func(t *testing.T) {
		err := fmt.Errorf("wrapped: %w", tx_manager.ErrTxQueuedForRetry)
		if !isQueuedForRetry(err) {
			t.Fatalf("expected queued sentinel to match")
		}
	})

	t.Run("enqueue failed sentinel", func(t *testing.T) {
		err := fmt.Errorf("%w: %w", tx_manager.ErrTxRetryEnqueueFailed, errors.New("jetstream unavailable"))
		if isQueuedForRetry(err) {
			t.Fatalf("did not expect enqueue-failed sentinel to match queued status")
		}
	})
}
