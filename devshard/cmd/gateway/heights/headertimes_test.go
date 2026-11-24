package heights

import (
	"context"
	"errors"
	"testing"
	"time"

	"common/chainoracle/blocks"

	"devshard/types"
)

const (
	headerTimeHeight = 42
	headerTimeUnixMs = 1_700_000_000_000
)

type headerAtFixedTime struct {
	blocks.BlockOracle
	hash []byte
}

func (oracle headerAtFixedTime) At(_ context.Context, height int64) (*blocks.Header, error) {
	return blocks.DummyHeader(height), nil
}

func (oracle headerAtFixedTime) HeaderAtWait(_ context.Context, height int64, _ time.Duration) (*blocks.Header, error) {
	return &blocks.Header{Height: height, BlockHash: oracle.hash, Time: time.UnixMilli(headerTimeUnixMs)}, nil
}

// Test flow:
//  1. Build the header time source over a follower whose oracle answers a plain lookup with a dummy header and a waiting lookup with the real block time.
//  2. Wait for the source to resolve that height with the oracle's block hash.
//  3. Assert it returns the block time of the waiting lookup, so the follower's history reads are not hidden behind its embedded interface.
func TestHeaderTimesReadTheFollowerWhenItIsOn(t *testing.T) {
	hash := []byte{0xab}
	follower := &Oracle{BlockOracle: headerAtFixedTime{hash: hash}}

	got, err := NewHeaderTimes(follower, nil).AwaitHeaderTimeMs(t.Context(), headerTimeHeight, hash)

	if err != nil || got != headerTimeUnixMs {
		t.Fatalf("AwaitHeaderTimeMs() = %d, %v, want %d, nil", got, err, headerTimeUnixMs)
	}
}

// Test flow:
//  1. Build the header time source with no follower and no chain client.
//  2. Assert the source exists.
//  3. Ask it for a height and assert the answer is a pending header, not a panic.
func TestHeaderTimesExistWithoutAFollower(t *testing.T) {
	source := NewHeaderTimes(nil, nil)

	if source == nil {
		t.Fatal("NewHeaderTimes(nil, nil) = nil, want a source")
	}
	if _, err := source.HeaderTimeMs(headerTimeHeight, []byte{0xab}); !errors.Is(err, types.ErrHeaderPending) {
		t.Fatalf("HeaderTimeMs() error = %v, want %v", err, types.ErrHeaderPending)
	}
}
