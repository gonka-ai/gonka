package chain

import (
	"context"
	"fmt"
)

// Client provides blockchain queries for common.
// It is used directly (not wrapped in an interface) — consumers that need mocking
// define their own narrow interfaces with only the methods they call.
type Client struct {
	rpcURL string
}

// New creates a chain client. cfg is assumed valid — config.Load guarantees this.
func New(rpcURL string) *Client {
	return &Client{rpcURL}
}

// GetInference returns a minimal inference record by ID.
// Satisfies engine.chainQuerier.
func (c *Client) GetInference(ctx context.Context, id string) (*Inference, error) {
	return nil, fmt.Errorf("chain: GetInference not implemented")
}

// GetRandomExecutor selects an executor for the given model.
func (c *Client) GetRandomExecutor(ctx context.Context, model string) (*ExecutorDestination, error) {
	return nil, fmt.Errorf("chain: GetRandomExecutor not implemented")
}

// GetEpochSeed returns the BLS seed bytes for the given epoch index.
func (c *Client) GetEpochSeed(ctx context.Context, epochIndex uint64) ([]byte, error) {
	return nil, fmt.Errorf("chain: GetEpochSeed not implemented")
}

// GetEpochInfo returns the current epoch index and block height from the chain.
func (c *Client) GetEpochInfo(ctx context.Context) (epochID uint64, blockHeight int64, err error) {
	return 0, 0, fmt.Errorf("chain: GetEpochInfo not implemented")
}
