package heights

import (
	"context"
	"testing"
	"time"

	"common/chainoracle/blocks"

	"devshard/state"
)

type headerAt struct {
	blocks.BlockOracle
	header *blocks.Header
}

func (h headerAt) At(context.Context, int64) (*blocks.Header, error) { return h.header, nil }

// Test flow:
//  1. Build the header-time source with no Comet RPC and a follower that serves one block.
//  2. Await the time of that block's height and hash within a 5 s bound.
//  3. Assert the source returns the block's time in milliseconds.
func TestTheHeaderTimeSourceReadsBlockTimeFromTheFollowerWhenThereIsNoCometRPC(t *testing.T) {
	blockTime := time.Unix(1_700_000_000, 0)
	follower := &Oracle{BlockOracle: headerAt{header: &blocks.Header{Height: 7, Time: blockTime, BlockHash: []byte{0xaa}}}}

	source := NewHeaderTimes("", follower, nil)

	awaiter, ok := source.(state.HeaderTimeAwaiter)
	if !ok {
		t.Fatalf("NewHeaderTimes() = %T, want a source that awaits a block", source)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := awaiter.AwaitHeaderTimeMs(ctx, 7, []byte{0xaa})
	if err != nil {
		t.Fatalf("AwaitHeaderTimeMs(7) = %v", err)
	}
	if want := blockTime.UnixMilli(); got != want {
		t.Fatalf("AwaitHeaderTimeMs(7) = %d, want %d", got, want)
	}
}

// Test flow:
//  1. Build the header-time source with no Comet RPC, no follower and no chain client.
//  2. Assert it is nil, so a session is not handed a source that can never answer.
func TestNoHeaderTimeSourceWithoutAnyChainReading(t *testing.T) {
	if source := NewHeaderTimes("", nil, nil); source != nil {
		t.Fatalf("NewHeaderTimes() = %T, want nil", source)
	}
}
