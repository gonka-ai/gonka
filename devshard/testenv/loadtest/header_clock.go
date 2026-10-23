package loadtest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
)

// chainRPCURL is mock-chain's CometBFT RPC as the host sees it.
func chainRPCURL(cfg *config.File) string {
	return fmt.Sprintf("http://127.0.0.1:%d", cfg.MockChain.RPCPort)
}

// headerClock dates block heights by the time of their header on the chain, the clock devshard runs
// timeouts on: the gateway's debug records name the heights their timeouts are anchored at, not times.
// A height's time does not change, so it is asked once. A nil clock dates nothing.
type headerClock struct {
	rpcURL string
	mu     sync.Mutex
	ms     map[uint64]int64
}

func newHeaderClock(rpcURL string) *headerClock {
	return &headerClock{rpcURL: rpcURL, ms: map[uint64]int64{}}
}

// timeMs is the time of the block at height in unix milliseconds, 0 for height 0 (no anchor).
func (c *headerClock) timeMs(ctx context.Context, height uint64) (int64, error) {
	if c == nil || height == 0 {
		return 0, nil
	}
	c.mu.Lock()
	ms, ok := c.ms[height]
	c.mu.Unlock()
	if ok {
		return ms, nil
	}
	var doc struct {
		Result struct {
			Header struct {
				Time time.Time `json:"time"`
			} `json:"header"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
			Data    string `json:"data"`
		} `json:"error"`
	}
	if err := getJSON(ctx, fmt.Sprintf("%s/header?height=%d", c.rpcURL, height), "", &doc); err != nil {
		return 0, fmt.Errorf("header %d: %w", height, err)
	}
	if doc.Error != nil {
		return 0, fmt.Errorf("header %d: %s %s", height, doc.Error.Message, doc.Error.Data)
	}
	if doc.Result.Header.Time.IsZero() {
		return 0, fmt.Errorf("header %d has no time", height)
	}
	ms = doc.Result.Header.Time.UnixMilli()
	c.mu.Lock()
	c.ms[height] = ms
	c.mu.Unlock()
	return ms, nil
}

// unix is the time of the block at height in unix seconds, 0 for height 0.
func (c *headerClock) unix(ctx context.Context, height uint64) (int64, error) {
	ms, err := c.timeMs(ctx, height)
	return ms / 1000, err
}

// times reads the clock as harness.HeaderTimes.
func (c *headerClock) times(ctx context.Context) harness.HeaderTimes {
	return func(height uint64) (int64, bool) {
		ms, err := c.timeMs(ctx, height)
		return ms, err == nil && ms > 0
	}
}
