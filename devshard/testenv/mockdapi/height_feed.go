package mockdapi

import (
	"context"
	"sync"
	"time"

	"common/chainoracle/blocks"
	"common/nodemanager/gen"
	"devshard/chainoracle/blocks/headertime"
	"devshard/cmd/devshardd/events"
	"devshard/testenv/mockheights"
)

// HeightFeed is the mock chain's BLOCK log. While the dapi client is up,
// progress appends one BLOCK and the production poller writes the map.
// While the client is stopped, progress delivers that same triple on the
// chain's NewBlock subscription and ServeChain writes the map.
type HeightFeed struct {
	mu       sync.Mutex
	dapiDown bool
	chain    *mockheights.Chain
	ring     *hostEventRing
	srv      *nodeManagerServer
	blocks   []func(context.Context, events.NewBlockEvent)
}

// NewHeightFeed serves GetHostEvents for heights the chain has progressed.
func NewHeightFeed() *HeightFeed {
	chain := mockheights.New()
	ring := newHostEventRing()
	f := &HeightFeed{
		chain: chain,
		ring:  ring,
		srv:   newNodeManagerServer(nil, ring, nil, false),
	}
	chain.Watch(f.onProgress)
	return f
}

// Chain is the in-process chain this feed reads.
func (f *HeightFeed) Chain() *mockheights.Chain { return f.chain }

// Progress records the height on the chain and appends its BLOCK.
func (f *HeightFeed) Progress(height int64) (mockheights.Block, bool) {
	return f.chain.Progress(height)
}

// Server is the in-process NodeManager the poller dials.
func (f *HeightFeed) Server() gen.NodeManagerServer { return f.srv }

// StopClient makes GetHostEvents fail. Heights progressed after this are not
// appended to the dapi log. The NewBlock subscription keeps running.
func (f *HeightFeed) StopClient() {
	f.mu.Lock()
	f.dapiDown = true
	f.mu.Unlock()
	f.ring.SetStalled(true)
}

// StartClient lets GetHostEvents serve later BLOCKs again.
func (f *HeightFeed) StartClient() {
	f.mu.Lock()
	f.dapiDown = false
	f.mu.Unlock()
	f.ring.SetStalled(false)
}

// ServeChain subscribes src to the mock chain's NewBlock feed. Each new
// height is stored with origin chain and the stamp recorded at progress.
// Stopping the dapi client does not remove this subscription.
func (f *HeightFeed) ServeChain(src *headertime.Source) {
	if src == nil {
		return
	}
	f.mu.Lock()
	f.blocks = append(f.blocks, func(_ context.Context, e events.NewBlockEvent) {
		src.Observe(blocks.HashOnlyHeader(e.BlockHeight, e.Time, e.ChainID, e.BlockHash), headertime.OriginChain, false)
	})
	f.mu.Unlock()
}

func (f *HeightFeed) onProgress(b mockheights.Block) {
	ev := events.NewBlockEvent{
		BlockHeight: b.Height,
		BlockHash:   append([]byte(nil), b.Hash...),
		Time:        time.UnixMilli(b.TimeMs).UTC(),
		ChainID:     "mockchain",
	}
	f.mu.Lock()
	down := f.dapiDown
	handlers := make([]func(context.Context, events.NewBlockEvent), len(f.blocks))
	copy(handlers, f.blocks)
	f.mu.Unlock()
	if down {
		for _, h := range handlers {
			h(context.Background(), ev)
		}
		return
	}
	f.ring.AppendBlock(&gen.BlockPayload{
		Height:           b.Height,
		Hash:             append([]byte(nil), b.Hash...),
		HeaderTimeUnixMs: b.TimeMs,
		Header:           append([]byte(nil), b.Hash...),
	})
}
