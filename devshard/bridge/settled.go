package bridge

import (
	"errors"
	"fmt"
	"time"
)

// ErrEscrowQueryTimeout means GetEscrow did not answer within the caller's budget.
var ErrEscrowQueryTimeout = errors.New("escrow query timed out")

// SettledWithin reports whether the chain says escrowID is already settled,
// giving up after timeout.
//
// MainnetBridge.GetEscrow takes no context and the gRPC implementations call it
// with context.Background(), so a chain node that accepts the connection but
// never answers would block the caller forever. Callers on paths that must not
// hang (host startup recovery, the host-events dispatch loop) use this instead
// of calling GetEscrow directly. On timeout the stray goroutine writes to a
// buffered channel and exits on its own.
//
// It returns true only for an unambiguous positive. A nil bridge, a nil info, a
// query failure, or a timeout all return false with a non-nil error, so callers
// fail open and keep serving work they have already bound.
func SettledWithin(b MainnetBridge, escrowID string, timeout time.Duration) (bool, error) {
	if b == nil {
		return false, errors.New("no chain bridge configured")
	}
	if timeout <= 0 {
		return false, fmt.Errorf("%w: escrow %s budget exhausted", ErrEscrowQueryTimeout, escrowID)
	}

	type result struct {
		info *EscrowInfo
		err  error
	}
	done := make(chan result, 1)
	go func() {
		info, err := b.GetEscrow(escrowID)
		done <- result{info: info, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-done:
		if res.err != nil {
			return false, res.err
		}
		if res.info == nil {
			return false, fmt.Errorf("%w: escrow %s", ErrEscrowNotFound, escrowID)
		}
		return res.info.Settled, nil
	case <-timer.C:
		return false, fmt.Errorf("%w: escrow %s after %s", ErrEscrowQueryTimeout, escrowID, timeout)
	}
}
