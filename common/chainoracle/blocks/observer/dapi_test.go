package observer

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/chainoracle/blocks"

	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
)

type publishLog struct {
	mu   sync.Mutex
	dapi map[int64]bool
	hash map[int64][]byte
}

func watchPublish(o *Oracle) *publishLog {
	p := &publishLog{dapi: make(map[int64]bool), hash: make(map[int64][]byte)}
	o.SetPublish(func(h *blocks.Header, fromDAPI bool) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.dapi[h.Height] = fromDAPI
		p.hash[h.Height] = append([]byte(nil), h.BlockHash...)
	})
	return p
}

func (p *publishLog) has(height int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.dapi[height]
	return ok
}

func (p *publishLog) fromDAPI(height int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dapi[height]
}

func (p *publishLog) hashAt(height int64) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hash[height]
}

func (p *publishLog) all(from, to int64) bool {
	for h := from; h <= to; h++ {
		if !p.has(h) {
			return false
		}
	}
	return true
}

func blockOf(t *testing.T, hdr *cmttypes.Header) DAPIBlock {
	t.Helper()
	return BlockOf(hdr)
}

func (c *signedChain) page(t *testing.T, from, to int64, skip ...int64) []DAPIBlock {
	t.Helper()
	return c.Page(from, to, skip...)
}

func forged(c *signedChain, height int64) *cmttypes.Header {
	return c.Forged(height)
}

func trustedOracle(t *testing.T, c *signedChain) (*Oracle, *publishLog, *atomic.Int32) {
	t.Helper()
	o := verifyingOracle(c, nil)
	pub := watchPublish(o)
	var failures atomic.Int32
	o.SetOnDAPIFailure(func(error) { failures.Add(1) })
	_, err := o.Trusted(context.Background())
	require.NoError(t, err)
	return o, pub, &failures
}

func TestOfferDAPICatchUpLinksDownFromTheTrustedTip(t *testing.T) {
	c := newSignedChain(t, 200)
	o, pub, failures := trustedOracle(t, c)
	o.SetPendingFloor(func() int64 { return 150 })
	commits, batches := c.commitCalls.Load(), c.batchCalls.Load()

	require.NoError(t, o.OfferDAPI(c.page(t, 140, 169)))
	require.Equal(t, 20, o.pendingLen(), "heights below the floor are not held")
	require.NoError(t, o.OfferDAPI(c.page(t, 170, 189)))
	for h := int64(140); h <= 189; h++ {
		require.Falsef(t, pub.has(h), "height %d reached the map before the buffer connected", h)
	}
	_, err := o.At(context.Background(), 160)
	require.ErrorIs(t, err, ErrHeaderPendingLink)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)

	require.NoError(t, o.OfferDAPI(c.page(t, 190, 199)))
	require.Eventually(t, func() bool {
		if o.pendingLen() != 0 {
			return false
		}
		for h := int64(150); h <= 199; h++ {
			if !pub.fromDAPI(h) {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond)
	for h := int64(150); h <= 199; h++ {
		require.Equal(t, c.header(h).Hash().Bytes(), pub.hashAt(h))
	}
	require.Equal(t, commits, c.commitCalls.Load(), "no commit check below the trusted header")
	require.Equal(t, batches, c.batchCalls.Load(), "no BlockchainInfo for heights dapi supplied")
	require.Zero(t, failures.Load())
	require.True(t, o.FromDAPI(150))
	require.True(t, o.FromDAPI(199))
	require.False(t, o.FromDAPI(200), "the trusted header is not a dapi BLOCK")
}

func TestOfferDAPILivePageCostsOneCommitCheck(t *testing.T) {
	c := newSignedChain(t, 100)
	o, pub, _ := trustedOracle(t, c)
	c.grow(110)
	commits, batches := c.commitCalls.Load(), c.batchCalls.Load()

	require.NoError(t, o.OfferDAPI(c.page(t, 101, 110)))
	require.Eventually(t, func() bool { return pub.all(101, 110) }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, commits+1, c.commitCalls.Load())
	require.Equal(t, batches, c.batchCalls.Load())
	for h := int64(101); h <= 109; h++ {
		require.Truef(t, pub.fromDAPI(h), "height %d links down from the checked page top", h)
	}
}

func TestOfferDAPIMatchingCometHeaderPublishesFromDAPI(t *testing.T) {
	c := newSignedChain(t, 100)
	o, pub, failures := trustedOracle(t, c)
	c.grow(101)
	hdr := c.header(101)
	o.Observe(blocks.HashOnlyHeader(hdr.Height, hdr.Time, hdr.ChainID, hdr.Hash().Bytes()))
	require.Eventually(t, func() bool { return pub.has(101) }, 5*time.Second, 10*time.Millisecond)
	require.False(t, pub.fromDAPI(101), "comet verify is not a dapi BLOCK")

	require.NoError(t, o.OfferDAPI(c.page(t, 101, 101)))
	require.Eventually(t, func() bool { return pub.fromDAPI(101) }, 5*time.Second, 10*time.Millisecond)
	require.Zero(t, failures.Load())
	require.True(t, o.FromDAPI(101))
}

func TestOfferDAPISelfCheckRejects(t *testing.T) {
	cases := map[string]func(c *signedChain, b DAPIBlock) DAPIBlock{
		"hash": func(c *signedChain, b DAPIBlock) DAPIBlock {
			b.Hash = c.header(98).Hash()
			return b
		},
		"height": func(_ *signedChain, b DAPIBlock) DAPIBlock {
			b.Height = 98
			return b
		},
		"time": func(_ *signedChain, b DAPIBlock) DAPIBlock {
			b.TimeMs++
			return b
		},
		"chain id": func(c *signedChain, _ DAPIBlock) DAPIBlock {
			h := c.header(99)
			h.ChainID = "another-chain"
			return DAPIBlock{Height: 99, Hash: h.Hash(), TimeMs: h.Time.UnixMilli(), Header: mustMarshal(h)}
		},
		"empty header": func(_ *signedChain, b DAPIBlock) DAPIBlock {
			b.Header = nil
			return b
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := newSignedChain(t, 100)
			o, pub, failures := trustedOracle(t, c)
			b := mutate(c, blockOf(t, c.header(99)))
			err := o.OfferDAPI([]DAPIBlock{b})
			require.ErrorIs(t, err, ErrDAPIHeader)
			require.Zero(t, o.pendingLen())
			require.False(t, pub.has(99))
			require.False(t, pub.has(98))
			require.Equal(t, int32(1), failures.Load())
			require.Equal(t, uint64(1), o.DAPIFailures())
		})
	}
}

func mustMarshal(h *cmttypes.Header) []byte {
	pb := h.ToProto()
	raw, err := pb.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

func TestOfferDAPIForgedHeaderBreaksTheLink(t *testing.T) {
	c := newSignedChain(t, 100)
	o, pub, failures := trustedOracle(t, c)
	page := c.page(t, 95, 99)
	page[2] = blockOf(t, forged(c, 97))

	require.NoError(t, o.OfferDAPI(page), "a forged header passes its own self-check")
	require.Eventually(t, func() bool {
		return pub.fromDAPI(99) && pub.fromDAPI(98) && failures.Load() == 1 && o.pendingLen() == 0
	}, 5*time.Second, 10*time.Millisecond, "verified heights above the break stay")

	require.Eventually(t, func() bool { return pub.all(95, 97) }, 5*time.Second, 10*time.Millisecond)
	for h := int64(95); h <= 97; h++ {
		require.False(t, pub.fromDAPI(h), "the RPC fills the hole")
		require.Equal(t, c.header(h).Hash().Bytes(), pub.hashAt(h))
	}
	require.Equal(t, [][2]int64{{95, 97}}, c.ranges(), "only the dropped heights are read")
}

func TestOfferDAPIAnchorWithADifferentHash(t *testing.T) {
	c := newSignedChain(t, 100)
	o, pub, failures := trustedOracle(t, c)
	c.grow(105)
	page := c.page(t, 101, 105)
	page[4] = blockOf(t, forged(c, 105))

	require.NoError(t, o.OfferDAPI(page))
	require.Eventually(t, func() bool { return failures.Load() == 1 }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return pub.all(101, 105) }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, c.header(105).Hash().Bytes(), pub.hashAt(105), "the commit-checked header wins")
	for h := int64(101); h <= 104; h++ {
		require.False(t, pub.fromDAPI(h), "dapi's headers below the conflict are dropped")
	}
	require.Equal(t, [][2]int64{{101, 104}}, c.ranges())
}

func TestOfferDAPIOneMissingHeightReadsOnlyThatHeight(t *testing.T) {
	c := newSignedChain(t, 100)
	o, pub, _ := trustedOracle(t, c)

	require.NoError(t, o.OfferDAPI(c.page(t, 80, 99, 90)))
	require.Eventually(t, func() bool { return pub.all(91, 99) }, 5*time.Second, 10*time.Millisecond)
	require.False(t, pub.has(89), "heights below the hole stay pending")
	_, err := o.At(context.Background(), 85)
	require.ErrorIs(t, err, ErrHeaderPendingLink)

	h, err := o.At(context.Background(), 90)
	require.NoError(t, err)
	require.Equal(t, c.header(90).Hash().Bytes(), h.BlockHash)
	require.Equal(t, [][2]int64{{90, 90}}, c.ranges())
	require.False(t, pub.fromDAPI(90))
	require.Eventually(t, func() bool {
		if o.pendingLen() != 0 {
			return false
		}
		for height := int64(80); height <= 89; height++ {
			if !pub.fromDAPI(height) {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond)
}

func TestFillDownTakesBufferedHeightsBeforeTheRPC(t *testing.T) {
	c := newSignedChain(t, 100)
	o, pub, _ := trustedOracle(t, c)
	require.NoError(t, o.OfferDAPI(c.page(t, 80, 99, 90)))

	h, err := o.At(context.Background(), 70)
	require.NoError(t, err)
	require.Equal(t, c.header(70).Hash().Bytes(), h.BlockHash)
	require.Equal(t, [][2]int64{{90, 90}, {70, 79}}, c.ranges(), "the RPC reads only heights the buffer does not have")
	for h := int64(80); h <= 89; h++ {
		require.Truef(t, pub.fromDAPI(h), "height %d", h)
	}
}

func TestCometTipAndDAPIBlockShareOneCommitCheck(t *testing.T) {
	c := newSignedChain(t, 100)
	o, pub, _ := trustedOracle(t, c)
	c.grow(101)
	commits := c.commitCalls.Load()
	hdr := c.header(101)

	o.Observe(blocks.HashOnlyHeader(101, hdr.Time, hdr.ChainID, hdr.Hash()))
	require.NoError(t, o.OfferDAPI(c.page(t, 101, 101)))
	require.Eventually(t, func() bool { return pub.has(101) && o.pendingLen() == 0 }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, commits+1, c.commitCalls.Load())
}

func TestOfferDAPIBufferHoldsTheFloorWindow(t *testing.T) {
	c := newSignedChain(t, 200)
	o := verifyingOracle(c, nil)
	var floor atomic.Int64
	floor.Store(95)
	o.SetPendingFloor(func() int64 { return floor.Load() })

	require.NoError(t, o.OfferDAPI(c.page(t, 90, 99)))
	require.Eventually(t, func() bool {
		return o.verifier.trustedHeight() == 200 && o.pendingLen() == 5
	}, 5*time.Second, 10*time.Millisecond, "the floor applies once the trusted header is known")
	require.Zero(t, c.batchCalls.Load())

	floor.Store(97)
	require.NoError(t, o.OfferDAPI(c.page(t, 96, 96)))
	require.Equal(t, 3, o.pendingLen(), "headers below a raised floor are dropped")
}

func TestVerifyingBlockchainInfoDoesNotTrustRows(t *testing.T) {
	c := newSignedChain(t, 100)
	o := verifyingOracle(c, nil)
	got, err := o.BlockchainInfo(context.Background(), 1, 20)
	require.NoError(t, err)
	require.Len(t, got, 20)
	_, ok := o.history.Get(5)
	require.False(t, ok)
}

func TestOfferDAPIReturnsWhileCommitIsHeld(t *testing.T) {
	c := newSignedChain(t, 100)
	c.blockCommit()
	t.Cleanup(c.releaseCommit)
	o := verifyingOracle(c, nil)

	start := time.Now()
	err := o.OfferDAPI(c.page(t, 90, 99))
	require.NoError(t, err)
	require.Less(t, time.Since(start), 10*time.Millisecond, "OfferDAPI must not wait on Commit")
	require.Equal(t, 10, o.pendingLen())
}

func TestOfferDAPIReturnsWhileFillDownHoldsFillMu(t *testing.T) {
	c := newSignedChain(t, 2500)
	o, pub, _ := trustedOracle(t, c)
	c.blockBatch()
	t.Cleanup(c.releaseBatch)

	fillErr := make(chan error, 1)
	go func() {
		_, err := o.At(context.Background(), 500)
		fillErr <- err
	}()
	select {
	case <-c.batchStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("fillDown did not start BlockchainInfo")
	}

	start := time.Now()
	require.NoError(t, o.OfferDAPI(c.page(t, 2400, 2419)))
	require.Less(t, time.Since(start), 10*time.Millisecond, "OfferDAPI must not wait on fillMu")
	require.False(t, pub.has(2419), "the page stays pending until fillDown releases fillMu")
	require.Positive(t, o.pendingLen())

	c.releaseBatch()
	require.Eventually(t, func() bool {
		return pub.fromDAPI(2400) && pub.fromDAPI(2419)
	}, 5*time.Second, 10*time.Millisecond, "the page links after the fill releases fillMu")
	select {
	case err := <-fillErr:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("fillDown did not finish")
	}
}

func TestAtFillsPendingAfterTheLinkBudget(t *testing.T) {
	c := newSignedChain(t, 200)
	o, pub, _ := trustedOracle(t, c)
	o.SetPendingFloor(func() int64 { return 150 })
	var elapsed atomic.Int64
	base := time.Now()
	o.now = func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
	o.linkBudget = 5 * time.Second

	require.NoError(t, o.OfferDAPI(c.page(t, 150, 189)))
	_, err := o.At(context.Background(), 160)
	require.ErrorIs(t, err, ErrHeaderPendingLink)

	elapsed.Store(int64(5*time.Second + time.Millisecond))
	h, err := o.At(context.Background(), 160)
	require.NoError(t, err)
	require.Equal(t, c.header(160).Hash().Bytes(), h.BlockHash)
	require.Equal(t, [][2]int64{{190, 199}}, c.ranges())
	require.Eventually(t, func() bool {
		for height := int64(150); height <= 189; height++ {
			if !pub.fromDAPI(height) {
				return false
			}
		}
		return true
	}, 5*time.Second, 10*time.Millisecond)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestOfferDAPIAboveRPCTipLogsOncePerHeight(t *testing.T) {
	c := newSignedChain(t, 100)
	o, _, _ := trustedOracle(t, c)
	c.grow(105)
	c.capCommit(100)

	var buf syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	require.NoError(t, o.OfferDAPI(c.page(t, 101, 105)))
	require.Eventually(t, func() bool {
		return strings.Count(buf.String(), "dapi ahead of the RPC") == 1
	}, 5*time.Second, 10*time.Millisecond)
	commits := c.commitCalls.Load()
	require.NoError(t, o.OfferDAPI(c.page(t, 101, 105)))
	require.Eventually(t, func() bool { return c.commitCalls.Load() > commits }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, 1, strings.Count(buf.String(), "dapi ahead of the RPC"), "at most one line per height")
}

func TestLinkBufferedFillsPendingFromTheTrustedHeader(t *testing.T) {
	c := newSignedChain(t, 200)
	o, pub, _ := trustedOracle(t, c)
	o.SetPendingFloor(func() int64 { return 150 })
	require.NoError(t, o.OfferDAPI(c.page(t, 150, 189)))
	_, err := o.At(context.Background(), 160)
	require.ErrorIs(t, err, ErrHeaderPendingLink)

	o.LinkBuffered()
	require.Eventually(t, func() bool {
		h, err := o.At(context.Background(), 160)
		return err == nil && bytes.Equal(h.BlockHash, c.header(160).Hash().Bytes()) && pub.fromDAPI(150) && pub.fromDAPI(189)
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, [][2]int64{{190, 199}}, c.ranges())
}
