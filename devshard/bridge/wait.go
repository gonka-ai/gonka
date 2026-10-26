package bridge

import (
	"context"
	"errors"
	"time"
)

var (
	EscrowReadyTimeout      = 30 * time.Second
	EscrowReadyPollInterval = time.Second
)

func WaitForEscrow(ctx context.Context, b MainnetBridge, escrowID string, timeout time.Duration) (*EscrowInfo, error) {
	deadline := time.Now().Add(timeout)
	for {
		info, err := b.GetEscrow(escrowID)
		if !errors.Is(err, ErrEscrowNotFound) || !time.Now().Before(deadline) {
			return info, err
		}
		timer := time.NewTimer(EscrowReadyPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
