package state

import (
	"context"
	"devshard/heightsync"
	"devshard/types"
	"errors"
	"time"
)

type HeaderTimeSource interface {
	HeaderTimeMs(height uint64, hash []byte) (int64, error)
}

type HeaderTimeAwaiter interface {
	AwaitHeaderTimeMs(ctx context.Context, height uint64, hash []byte) (int64, error)
}

const EvidenceHeaderWait = 5 * time.Second

func (sm *StateMachine) awaitRaiseHeaders(txs []*types.DevshardTx) {
	awaiter, ok := sm.headerTimes.(HeaderTimeAwaiter)
	if !ok {
		return
	}
	sm.mu.RLock()
	cur, _ := sm.heightSyncFloor.Max()
	claims := floorClaims(sm.state, txs)
	sm.mu.RUnlock()
	ctx, cancel := context.WithTimeout(context.Background(), EvidenceHeaderWait)
	defer cancel()
	for _, c := range claims {
		if c.Signer == heightsync.SequencerSigner || c.Height <= cur || !heightsync.StampPresent(c.Hash) {
			continue
		}
		_, _ = awaiter.AwaitHeaderTimeMs(ctx, c.Height, c.Hash)
	}
}

var fallbackHeaderTimes HeaderTimeSource

func SetFallbackHeaderTimes(src HeaderTimeSource) {
	fallbackHeaderTimes = src
}

const (
	HeaderPendingRetryInterval = time.Second
	ReplayHeaderWait           = 2 * time.Minute
	ReplayHeaderAwait          = 10 * time.Second
)

func RetryWhileHeaderPending(ctx context.Context, maxWait time.Duration, apply func() error) error {
	err := apply()
	if !errors.Is(err, types.ErrHeaderPending) {
		return err
	}
	deadline := time.Now().Add(maxWait)
	for errors.Is(err, types.ErrHeaderPending) && time.Now().Before(deadline) {
		timer := time.NewTimer(HeaderPendingRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		err = apply()
	}
	return err
}
