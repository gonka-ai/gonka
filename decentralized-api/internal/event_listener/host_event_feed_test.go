package event_listener

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"decentralized-api/apiconfig"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestHostEventFeed_OrdersHeightAndCarriesHeader(t *testing.T) {
	const height int64 = 40
	rpc := newFeedRPC()
	rpc.results[height] = &coretypes.ResultBlockResults{
		FinalizeBlockEvents: []abcitypes.Event{
			abciEvent(maintenanceCanceledEvent, blockModeEnd,
				"reservation_id", "2", "participant", "host-a", "credit_restored", "1"),
			abciEvent(maintenanceCanceledEvent, blockModeBegin,
				"reservation_id", "11", "participant", "host-a", "credit_refunded", "20", "reason", "maintenance_disabled"),
			abciEvent(maintenanceCanceledEvent, blockModeBegin,
				"reservation_id", "99", "participant", "someone-else", "credit_refunded", "1"),
		},
		TxsResults: []*abcitypes.ExecTxResult{
			{Events: []abcitypes.Event{abciEvent(escrowCreatedEvent, "", "escrow_id", "not-a-number")}},
			{Events: []abcitypes.Event{abciEvent(escrowCreatedEvent, "",
				"escrow_id", "42", "epoch_index", "7", "model_id", "m1", "creator", "creator", "amount", "100")}},
			{Events: []abcitypes.Event{abciEvent(maintenanceScheduledEvent, "",
				"reservation_id", "9", "participant", "host-a", "start_height", "100", "duration_blocks", "50")}},
			{Events: []abcitypes.Event{abciEvent(warmKeyGrantedEvent, "",
				"granter", "gonka1cold", "grantee", "gonka1old", "grant_id", "2", "height", "40", "expires_at", "1700000000")}},
		},
	}
	rpc.results[height].FinalizeBlockEvents = append(rpc.results[height].FinalizeBlockEvents,
		abciEvent(warmKeyRevokedEvent, blockModeEnd,
			"granter", "gonka1cold", "grantee", "gonka1old", "grant_id", "2", "height", "40", "end", "WARM_KEY_LOG_END_EXPIRED"))

	epochs := mapEpochs{latestIdx: 3, latestStart: 30, ok: true}
	escrow := &scriptEscrow{ours: map[string]bool{"42": true}}
	f, _, ring := newTestFeed(t, rpc, epochs, escrow)
	f.bo.updateStatus(height, true)
	f.walk(t.Context())

	got := ringEvents(t, ring)
	require.Equal(t, []apiconfig.HostEventKind{
		apiconfig.HostEventKindMaintenanceCanceled,
		apiconfig.HostEventKindEscrowCreated,
		apiconfig.HostEventKindMaintenanceScheduled,
		apiconfig.HostEventKindWarmKeyGranted,
		apiconfig.HostEventKindMaintenanceCanceled,
		apiconfig.HostEventKindWarmKeyRevoked,
		apiconfig.HostEventKindBlock,
	}, kindsOf(got))
	require.Equal(t, "maintenance_disabled", got[0].Maintenance.Reason)
	require.Equal(t, uint64(11), got[0].Maintenance.ReservationID)
	require.Equal(t, uint64(7), got[1].Escrow.EpochIndex)
	require.Equal(t, "m1", got[1].Escrow.ModelID)
	require.Equal(t, uint64(50), got[2].Maintenance.DurationBlocks)
	require.Equal(t, int64(1700000000), got[3].WarmKey.ExpiresAtUnix)
	require.Equal(t, uint64(2), got[4].Maintenance.ReservationID)
	require.Equal(t, "WARM_KEY_LOG_END_EXPIRED", got[5].WarmKey.End)

	block := got[len(got)-1].Block
	hdr := rpc.headers[height]
	require.Equal(t, height, block.Height)
	require.Equal(t, []byte(hdr.Hash()), block.Hash)
	require.Equal(t, hdr.Time.UnixMilli(), block.HeaderTimeUnixMs)
	require.Equal(t, uint64(3), block.EpochIndex)
	var protoHdr cmtproto.Header
	require.NoError(t, gogoproto.Unmarshal(block.Header, &protoHdr))
	back, err := cmttypes.HeaderFromProto(&protoHdr)
	require.NoError(t, err)
	require.Equal(t, hdr.Hash(), back.Hash())
	require.Equal(t, height, f.emitted.Load())
}

func TestHostEventFeed_EpochLabelsAndFailedLookup(t *testing.T) {
	epochs := mapEpochs{
		latestIdx:   3,
		latestStart: 300,
		ok:          true,
		starts:      map[uint64]int64{1: 100, 2: 200},
	}
	rpc := newFeedRPC()
	f, _, ring := newTestFeed(t, rpc, epochs, nil)
	f.bo.updateStatus(301, true)
	f.emitted.Store(298)
	before := ring.Generation()
	f.walk(t.Context())
	require.Equal(t, before, ring.Generation())
	got := ringEvents(t, ring)
	require.Equal(t, []int64{299, 300, 301}, heightsOf(got))
	require.Equal(t, uint64(2), got[0].Block.EpochIndex)
	require.Equal(t, uint64(3), got[1].Block.EpochIndex)
	require.Equal(t, uint64(3), got[2].Block.EpochIndex)

	rpc = newFeedRPC()
	f, _, ring = newTestFeed(t, rpc, epochs, nil)
	f.bo.updateStatus(350, true)
	f.emitted.Store(149)
	before = ring.Generation()
	f.walk(t.Context())
	require.Equal(t, before+1, ring.Generation())
	got = ringEvents(t, ring)
	require.Equal(t, []int64{350}, heightsOf(got))
	require.Equal(t, uint64(3), got[0].Block.EpochIndex)
	require.Zero(t, rpc.blockResultCalls(150))
	require.Equal(t, 1, rpc.blockResultCalls(350))

	errOn := map[uint64]error{0: errors.New("epoch down")}
	starts := map[uint64]int64{}
	f, _, ring = newTestFeed(t, newFeedRPC(), mapEpochs{
		latestIdx: 1, latestStart: 100, ok: true, starts: starts, errOn: errOn,
	}, nil)
	f.bo.updateStatus(51, true)
	f.emitted.Store(49)
	f.walk(t.Context())
	require.Empty(t, ringEvents(t, ring))
	require.Equal(t, int64(49), f.emitted.Load())

	delete(errOn, 0)
	starts[0] = 1
	f.walk(t.Context())
	require.Equal(t, []int64{50, 51}, heightsOf(ringEvents(t, ring)))
	require.Equal(t, int64(51), f.emitted.Load())
}

func TestHostEventFeed_FailedReadRetriesAndHandlersAdvance(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		rpc := newFeedRPC()
		rpc.headerFailLeft[10] = 1
		f, bo, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
		f.bo.updateStatus(11, true)
		f.emitted.Store(9)
		f.walk(t.Context())
		require.Empty(t, ringEvents(t, ring))
		require.Equal(t, int64(9), f.emitted.Load())

		require.True(t, bo.processBlock(t.Context(), 10))
		ev := <-bo.Queue.Out
		require.Equal(t, systemBarrierEventType, ev.Result.Data.Type)
		bo.signalAllEventsRead(10)
		require.Equal(t, int64(10), bo.lastProcessedBlockHeight.Load())
		require.Equal(t, int64(9), f.emitted.Load())

		f.walk(t.Context())
		got := ringEvents(t, ring)
		require.Equal(t, []int64{10, 11}, heightsOf(got))
		require.Equal(t, int64(11), f.emitted.Load())
	})

	t.Run("block results", func(t *testing.T) {
		rpc := newFeedRPC()
		rpc.resultsFailLeft[10] = 5
		f, bo, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
		f.bo.updateStatus(11, true)
		f.emitted.Store(9)
		bo.lastQueriedBlockHeight.Store(9)
		f.walk(t.Context())
		require.Empty(t, ringEvents(t, ring))
		require.Equal(t, int64(9), f.emitted.Load())
		require.False(t, bo.processBlock(t.Context(), 10))
		require.Equal(t, int64(0), bo.lastProcessedBlockHeight.Load())
		require.Equal(t, int64(9), bo.lastQueriedBlockHeight.Load())

		rpc.mu.Lock()
		rpc.resultsFailLeft[10] = 0
		rpc.mu.Unlock()
		f.walk(t.Context())
		require.Equal(t, []int64{10, 11}, heightsOf(ringEvents(t, ring)))
	})
}

func TestHostEventFeed_ChainFailuresStallAfterRetries(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		rpc := newFeedRPC()
		rpc.statusErr = errors.New("status down")
		f, _, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
		f.bo.updateStatus(10, true)
		recoverChain(t, f, func() {
			rpc.mu.Lock()
			rpc.statusErr = nil
			rpc.mu.Unlock()
		})
		require.Equal(t, int64(10), f.emitted.Load())
		require.Equal(t, []int64{10}, heightsOf(ringEvents(t, ring)))
	})

	t.Run("block results", func(t *testing.T) {
		rpc := newFeedRPC()
		rpc.resultsFailLeft[10] = 100
		f, _, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
		f.bo.updateStatus(10, true)
		f.emitted.Store(9)
		recoverChain(t, f, func() {
			rpc.mu.Lock()
			rpc.resultsFailLeft[10] = 0
			rpc.mu.Unlock()
		})
		require.Equal(t, int64(10), f.emitted.Load())
		require.Equal(t, []int64{10}, heightsOf(ringEvents(t, ring)))
	})

	t.Run("header", func(t *testing.T) {
		rpc := newFeedRPC()
		rpc.headerFailLeft[10] = 100
		f, _, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
		f.bo.updateStatus(10, true)
		f.emitted.Store(9)
		recoverChain(t, f, func() {
			rpc.mu.Lock()
			rpc.headerFailLeft[10] = 0
			rpc.mu.Unlock()
		})
		require.Equal(t, int64(10), f.emitted.Load())
		require.Equal(t, []int64{10}, heightsOf(ringEvents(t, ring)))
	})

	t.Run("epoch", func(t *testing.T) {
		errOn := map[uint64]error{0: errors.New("epoch down")}
		starts := map[uint64]int64{}
		f, _, ring := newTestFeed(t, newFeedRPC(), mapEpochs{
			latestIdx: 1, latestStart: 100, ok: true, starts: starts, errOn: errOn,
		}, nil)
		f.bo.updateStatus(50, true)
		f.emitted.Store(49)
		recoverChain(t, f, func() {
			delete(errOn, 0)
			starts[0] = 1
		})
		require.Equal(t, int64(50), f.emitted.Load())
		require.Equal(t, []int64{50}, heightsOf(ringEvents(t, ring)))
	})
}

func recoverChain(t *testing.T, f *HostEventFeed, recover func()) {
	t.Helper()
	for i := 1; i < hostEventChainFailRetries; i++ {
		require.True(t, f.walk(t.Context()))
		require.False(t, f.Stalled(), "failure %d", i)
	}
	require.True(t, f.walk(t.Context()))
	require.True(t, f.Stalled())
	recover()
	require.False(t, f.walk(t.Context()))
	require.False(t, f.Stalled())
}

func TestHostEventFeed_ReusesEarliestStatus(t *testing.T) {
	rpc := newFeedRPC()
	f, bo, _ := newTestFeed(t, rpc, defaultEpochs(), nil)
	bo.updateStatus(1, true)
	require.False(t, f.walk(t.Context()))
	require.False(t, f.walk(t.Context()))
	require.Equal(t, 1, rpc.statusCalls)

	bo.earliestUnixNano.Store(time.Now().Add(-time.Hour).UnixNano())
	require.False(t, f.walk(t.Context()))
	require.Equal(t, 2, rpc.statusCalls)
}

func TestHostEventFeed_CatchUpBatchesHeaders(t *testing.T) {
	rpc := newFeedRPC()
	f, bo, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
	bo.updateStatus(25, true)
	f.emitted.Store(1)
	require.False(t, f.walk(t.Context()))
	require.Equal(t, int64(25), f.emitted.Load())
	require.Equal(t, 2, rpc.biCalls)
	require.Equal(t, 0, rpc.headerCalls)
	got := ringEvents(t, ring)
	require.Equal(t, int64(2), got[0].Block.Height)
	require.Equal(t, int64(25), got[len(got)-1].Block.Height)
}

func TestHostEventFeed_RetryCommitSkipsChain(t *testing.T) {
	rpc := newFeedRPC()
	f, bo, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
	s, _ := openFeedSQLite(t, f, "writer-retry-commit")
	s.failOnce = true
	bo.updateStatus(1, true)
	require.True(t, f.walk(t.Context()))
	require.Equal(t, uint64(0), ring.Head())
	require.Equal(t, 1, rpc.blockResultCalls(1))
	require.Equal(t, 1, rpc.headerCalls)

	require.False(t, f.walk(t.Context()))
	require.Equal(t, uint64(1), ring.Head())
	require.Equal(t, 1, rpc.blockResultCalls(1))
	require.Equal(t, 1, rpc.headerCalls)
}

func TestHostEventFeed_EscrowBudgetCancelsLookups(t *testing.T) {
	escrow := &scriptEscrow{delay: 10 * time.Second}
	rpc := newFeedRPC()
	rpc.results[5] = escrowBlock(1, 2, 3, 4, 5)
	f, _, ring := newTestFeed(t, rpc, defaultEpochs(), escrow)
	f.bo.updateStatus(5, true)
	f.emitted.Store(4)

	before := runtime.NumGoroutine()
	start := time.Now()
	f.walk(t.Context())
	elapsed := time.Since(start)
	require.Less(t, elapsed, 4*time.Second)
	require.Greater(t, elapsed, time.Second)

	got := ringEvents(t, ring)
	require.Len(t, got, 6)
	for i := 0; i < 5; i++ {
		require.Equal(t, apiconfig.HostEventKindEscrowCreated, got[i].Kind)
		require.Equal(t, uint64(i+1), got[i].Escrow.EscrowID)
	}
	require.Equal(t, apiconfig.HostEventKindBlock, got[5].Kind)
	require.Equal(t, int32(5), escrow.started.Load())
	require.Equal(t, int32(5), escrow.canceled.Load())
	waitGoroutines(t, before)
}

func TestHostEventFeed_EscrowLookupParallelism(t *testing.T) {
	escrow := &scriptEscrow{delay: 10 * time.Second}
	ids := make([]uint64, 20)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	rpc := newFeedRPC()
	rpc.results[8] = escrowBlock(ids...)
	f, _, ring := newTestFeed(t, rpc, defaultEpochs(), escrow)
	f.bo.updateStatus(8, true)
	f.emitted.Store(7)
	f.walk(t.Context())

	got := ringEvents(t, ring)
	require.Len(t, got, 21)
	require.LessOrEqual(t, escrow.max.Load(), int32(escrowLookupParallel))
	require.LessOrEqual(t, escrow.started.Load(), int32(escrowLookupParallel))
	require.Less(t, escrow.started.Load(), int32(20))
	require.Equal(t, escrow.started.Load(), escrow.canceled.Load())
	for i := 0; i < 20; i++ {
		require.Equal(t, uint64(i+1), got[i].Escrow.EscrowID)
	}
}

func TestChainEscrowQuerier_PassesContext(t *testing.T) {
	rpc := &recordingEscrowRPC{}
	q := &ChainEscrowQuerier{rpc: func() escrowRPC { return rpc }}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	_, err := q.GetEscrow(ctx, "7")
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 200*time.Millisecond)
	rpc.mu.Lock()
	require.Equal(t, ctx, rpc.seen)
	rpc.mu.Unlock()

	live, stop := context.WithCancel(t.Context())
	defer stop()
	info, err := q.GetEscrow(live, "7")
	require.NoError(t, err)
	require.Equal(t, []string{"host-a"}, info.Slots)
	rpc.mu.Lock()
	require.Equal(t, live, rpc.seen)
	rpc.mu.Unlock()
}

func TestHostEventFeed_DropsEscrowThatIsNotOurs(t *testing.T) {
	escrow := &scriptEscrow{ours: map[string]bool{"1": true, "2": false}, resolved: map[string]bool{"1": true, "2": true}}
	rpc := newFeedRPC()
	rpc.results[4] = &coretypes.ResultBlockResults{TxsResults: []*abcitypes.ExecTxResult{
		{Events: []abcitypes.Event{abciEvent(escrowCreatedEvent, "", "escrow_id", "1", "epoch_index", "1")}},
		{Events: []abcitypes.Event{abciEvent(escrowSettledEvent, "", "escrow_id", "2", "total_payout", "10")}},
		{Events: []abcitypes.Event{abciEvent(escrowSettledEvent, "", "escrow_id", "1", "total_payout", "3")}},
	}}
	f, _, ring := newTestFeed(t, rpc, defaultEpochs(), escrow)
	f.bo.updateStatus(4, true)
	f.emitted.Store(3)
	f.walk(t.Context())

	got := ringEvents(t, ring)
	require.Equal(t, []apiconfig.HostEventKind{
		apiconfig.HostEventKindEscrowCreated,
		apiconfig.HostEventKindEscrowSettled,
		apiconfig.HostEventKindBlock,
	}, kindsOf(got))
	require.Equal(t, uint64(1), got[0].Escrow.EscrowID)
	require.Equal(t, uint64(1), got[1].Escrow.EscrowID)
	require.Equal(t, int32(1), escrow.calls["1"])
	require.Equal(t, int32(1), escrow.calls["2"])
}

func TestHostEventFeed_NoJumpExceptPrunedHeights(t *testing.T) {
	const tip int64 = 1000
	rpc := newFeedRPC()
	f, bo, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
	bo.lastQueriedBlockHeight.Store(200)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go bo.Process(ctx)
	bo.updateStatus(tip, true)

	deadline := time.Now().Add(15 * time.Second)
	for bo.lastQueriedBlockHeight.Load() < tip && time.Now().Before(deadline) {
		select {
		case ev := <-bo.Queue.Out:
			if ev.Result.Data.Type == systemBarrierEventType {
				h, err := strconv.ParseInt(ev.Result.Events["barrier.height"][0], 10, 64)
				require.NoError(t, err)
				bo.signalAllEventsRead(h)
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	require.Equal(t, tip, bo.lastQueriedBlockHeight.Load())
	queried := rpc.blockResultHeights()
	require.NotEmpty(t, queried)
	for _, h := range queried {
		require.GreaterOrEqual(t, h, tip-500)
	}
	gen := ring.Generation()

	f.emitted.Store(tip - 800)
	f.walk(t.Context())
	got := ringEvents(t, ring)
	require.Len(t, got, 800)
	require.Equal(t, tip-799, got[0].Block.Height)
	require.Equal(t, tip, got[len(got)-1].Block.Height)
	for i := 1; i < len(got); i++ {
		require.Equal(t, got[i-1].Block.Height+1, got[i].Block.Height)
	}
	require.Equal(t, gen, ring.Generation())
	require.Equal(t, tip, bo.lastQueriedBlockHeight.Load())

	prunedRPC := newFeedRPC()
	prunedRPC.earliest = 400
	f, _, ring = newTestFeed(t, prunedRPC, defaultEpochs(), nil)
	f.bo.updateStatus(tip, true)
	f.emitted.Store(100)
	before := ring.Generation()
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindBlock, Block: &apiconfig.BlockPayload{Height: 1}})
	f.walk(t.Context())
	require.Equal(t, before+1, ring.Generation())
	got = ringEvents(t, ring)
	require.Len(t, got, 1)
	require.Equal(t, tip, got[0].Block.Height)
	old := ring.Since(1, before, []apiconfig.HostEventKind{apiconfig.HostEventKindBlock})
	require.True(t, old.Reset)

	statusRPC := newFeedRPC()
	statusRPC.statusErr = errors.New("status down")
	f, _, ring = newTestFeed(t, statusRPC, defaultEpochs(), nil)
	f.bo.updateStatus(20, true)
	f.emitted.Store(10)
	gen = ring.Generation()
	f.walk(t.Context())
	require.Equal(t, gen, ring.Generation())
	require.Equal(t, int64(10), f.emitted.Load())
	require.Empty(t, ringEvents(t, ring))
}

func TestHostEventFeed_SharesBlockResults(t *testing.T) {
	rpc := newFeedRPC()
	release := make(chan struct{})
	var entered atomic.Int32
	rpc.hook = func(height int64) {
		if height != 7 {
			return
		}
		if entered.Add(1) == 1 {
			<-release
		}
	}
	f, bo, ring := newTestFeed(t, rpc, defaultEpochs(), nil)
	f.bo.updateStatus(7, true)
	cache := f.results

	errc := make(chan error, 2)
	go func() {
		_, err := cache.Get(t.Context(), 7)
		errc <- err
	}()
	go func() {
		_, err := cache.Get(t.Context(), 7)
		errc <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for rpc.blockResultCalls(7) < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	require.NoError(t, <-errc)
	require.NoError(t, <-errc)
	require.Equal(t, 1, rpc.blockResultCalls(7))

	f.emitted.Store(6)
	f.walk(t.Context())
	require.True(t, bo.processBlock(t.Context(), 7))
	require.Equal(t, 1, rpc.blockResultCalls(7))
	require.Equal(t, int64(7), ringEvents(t, ring)[0].Block.Height)
}

func TestHostEventFeed_DoesNotWalkWhileCatchingUp(t *testing.T) {
	f, bo, ring := newTestFeed(t, newFeedRPC(), defaultEpochs(), nil)
	bo.currentBlockHeight.Store(80)
	bo.caughtUp.Store(false)
	f.walk(t.Context())
	require.Empty(t, ringEvents(t, ring))
	require.Equal(t, int64(0), f.emitted.Load())

	bo.updateStatus(80, true)
	f.walk(t.Context())
	got := ringEvents(t, ring)
	require.Len(t, got, 1)
	require.Equal(t, int64(80), got[0].Block.Height)
}

type feedRPC struct {
	mu              sync.Mutex
	results         map[int64]*coretypes.ResultBlockResults
	headers         map[int64]*cmttypes.Header
	resultsFailLeft map[int64]int
	headerFailLeft  map[int64]int
	headerCalls     int
	statusCalls     int
	biCalls         int
	biErr           error
	brCalls         map[int64]int
	brOrder         []int64
	earliest        int64
	statusErr       error
	hook            func(height int64)
}

func newFeedRPC() *feedRPC {
	return &feedRPC{
		results:         map[int64]*coretypes.ResultBlockResults{},
		headers:         map[int64]*cmttypes.Header{},
		resultsFailLeft: map[int64]int{},
		headerFailLeft:  map[int64]int{},
		brCalls:         map[int64]int{},
		earliest:        1,
	}
}

func (c *feedRPC) BlockResults(ctx context.Context, height *int64) (*coretypes.ResultBlockResults, error) {
	h := *height
	c.mu.Lock()
	c.brCalls[h]++
	c.brOrder = append(c.brOrder, h)
	left := c.resultsFailLeft[h]
	if left > 0 {
		c.resultsFailLeft[h] = left - 1
	}
	res := c.results[h]
	hook := c.hook
	c.mu.Unlock()
	if hook != nil {
		hook(h)
	}
	if left > 0 {
		return nil, errors.New("block results down")
	}
	if res == nil {
		res = &coretypes.ResultBlockResults{}
	}
	return res, nil
}

func (c *feedRPC) BlockchainInfo(ctx context.Context, minHeight, maxHeight int64) (*coretypes.ResultBlockchainInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.biCalls++
	if c.biErr != nil {
		return nil, c.biErr
	}
	if c.headerFailLeft[minHeight] > 0 {
		return nil, errors.New("header down")
	}
	metas := make([]*cmttypes.BlockMeta, 0, maxHeight-minHeight+1)
	for h := minHeight; h <= maxHeight; h++ {
		hdr := testHeader(h)
		c.headers[h] = hdr
		metas = append(metas, &cmttypes.BlockMeta{Header: *hdr})
	}
	return &coretypes.ResultBlockchainInfo{LastHeight: maxHeight, BlockMetas: metas}, nil
}

func (c *feedRPC) Header(ctx context.Context, height *int64) (*coretypes.ResultHeader, error) {
	h := *height
	c.mu.Lock()
	c.headerCalls++
	left := c.headerFailLeft[h]
	if left > 0 {
		c.headerFailLeft[h] = left - 1
	}
	hdr := c.headers[h]
	if hdr == nil && left == 0 {
		hdr = testHeader(h)
		c.headers[h] = hdr
	}
	c.mu.Unlock()
	if left > 0 {
		return nil, errors.New("header down")
	}
	return &coretypes.ResultHeader{Header: hdr}, nil
}

func (c *feedRPC) Status(ctx context.Context) (*coretypes.ResultStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statusCalls++
	if c.statusErr != nil {
		return nil, c.statusErr
	}
	return &coretypes.ResultStatus{SyncInfo: coretypes.SyncInfo{EarliestBlockHeight: c.earliest}}, nil
}

func (c *feedRPC) blockResultCalls(height int64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.brCalls[height]
}

func (c *feedRPC) blockResultHeights() []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]int64, len(c.brOrder))
	copy(out, c.brOrder)
	return out
}

func testHeader(height int64) *cmttypes.Header {
	var app [8]byte
	binary.BigEndian.PutUint64(app[:], uint64(height))
	return &cmttypes.Header{
		Version: cmtversion.Consensus{Block: 11},
		ChainID: "gonka-test",
		Height:  height,
		Time:    time.UnixMilli(1_700_000_000_000 + height).UTC(),
		LastBlockID: cmttypes.BlockID{
			Hash: bytesRepeat(1, tmhash.Size),
			PartSetHeader: cmttypes.PartSetHeader{
				Total: 1,
				Hash:  bytesRepeat(2, tmhash.Size),
			},
		},
		LastCommitHash:     bytesRepeat(3, tmhash.Size),
		DataHash:           bytesRepeat(4, tmhash.Size),
		ValidatorsHash:     bytesRepeat(5, tmhash.Size),
		NextValidatorsHash: bytesRepeat(6, tmhash.Size),
		ConsensusHash:      bytesRepeat(7, tmhash.Size),
		AppHash:            append([]byte(nil), app[:]...),
		LastResultsHash:    bytesRepeat(8, tmhash.Size),
		EvidenceHash:       bytesRepeat(10, tmhash.Size),
		ProposerAddress:    bytesRepeat(11, 20),
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func abciEvent(typ, mode string, kv ...string) abcitypes.Event {
	attrs := make([]abcitypes.EventAttribute, 0, len(kv)/2+1)
	for i := 0; i < len(kv); i += 2 {
		attrs = append(attrs, abcitypes.EventAttribute{Key: kv[i], Value: kv[i+1]})
	}
	if mode != "" {
		attrs = append(attrs, abcitypes.EventAttribute{Key: "mode", Value: mode})
	}
	return abcitypes.Event{Type: typ, Attributes: attrs}
}

func escrowBlock(ids ...uint64) *coretypes.ResultBlockResults {
	txs := make([]*abcitypes.ExecTxResult, len(ids))
	for i, id := range ids {
		txs[i] = &abcitypes.ExecTxResult{Events: []abcitypes.Event{abciEvent(escrowCreatedEvent, "",
			"escrow_id", strconv.FormatUint(id, 10), "epoch_index", "1")}}
	}
	return &coretypes.ResultBlockResults{TxsResults: txs}
}

type mapEpochs struct {
	latestIdx   uint64
	latestStart int64
	ok          bool
	starts      map[uint64]int64
	errOn       map[uint64]error
}

func defaultEpochs() mapEpochs {
	return mapEpochs{latestIdx: 1, latestStart: 1, ok: true}
}

func (m mapEpochs) LatestEpoch() (uint64, int64, bool) {
	return m.latestIdx, m.latestStart, m.ok
}

type recordingEpochs struct {
	inner  mapEpochs
	starts int
}

func (r *recordingEpochs) LatestEpoch() (uint64, int64, bool) {
	return r.inner.LatestEpoch()
}

func (r *recordingEpochs) EpochStart(ctx context.Context, index uint64) (int64, error) {
	r.starts++
	return r.inner.EpochStart(ctx, index)
}

func (m mapEpochs) EpochStart(ctx context.Context, index uint64) (int64, error) {
	if err, ok := m.errOn[index]; ok {
		return 0, err
	}
	st, ok := m.starts[index]
	if !ok {
		return 0, fmt.Errorf("missing epoch %d", index)
	}
	return st, nil
}

type scriptEscrow struct {
	delay    time.Duration
	ours     map[string]bool
	resolved map[string]bool
	inflight atomic.Int32
	max      atomic.Int32
	started  atomic.Int32
	canceled atomic.Int32
	mu       sync.Mutex
	calls    map[string]int32
}

func (s *scriptEscrow) GetEscrow(ctx context.Context, escrowID string) (*EscrowSlotInfo, error) {
	s.started.Add(1)
	cur := s.inflight.Add(1)
	for {
		max := s.max.Load()
		if cur <= max || s.max.CompareAndSwap(max, cur) {
			break
		}
	}
	defer s.inflight.Add(-1)
	s.mu.Lock()
	if s.calls == nil {
		s.calls = map[string]int32{}
	}
	s.calls[escrowID]++
	s.mu.Unlock()

	timer := time.NewTimer(s.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		s.canceled.Add(1)
		return nil, ctx.Err()
	case <-timer.C:
	}
	if s.resolved != nil && !s.resolved[escrowID] {
		return nil, errors.New("unresolved")
	}
	if s.ours != nil && !s.ours[escrowID] {
		return &EscrowSlotInfo{EscrowID: escrowID, Slots: []string{"other-host"}}, nil
	}
	return &EscrowSlotInfo{EscrowID: escrowID, Slots: []string{"host-a"}}, nil
}

type recordingEscrowRPC struct {
	mu   sync.Mutex
	seen context.Context
}

func (r *recordingEscrowRPC) DevshardEscrow(ctx context.Context, in *types.QueryGetDevshardEscrowRequest, opts ...grpc.CallOption) (*types.QueryGetDevshardEscrowResponse, error) {
	r.mu.Lock()
	r.seen = ctx
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	return &types.QueryGetDevshardEscrowResponse{
		Found:  true,
		Escrow: &types.DevshardEscrow{Id: in.Id, Slots: []string{"host-a"}},
	}, nil
}

func newTestFeed(t *testing.T, rpc *feedRPC, epochs epochSource, escrow escrowQuerier) (*HostEventFeed, *BlockObserver, *apiconfig.HostEventRing) {
	t.Helper()
	ring := apiconfig.NewHostEventRing(4096, 7)
	bo := NewBlockObserverWithClient(&apiconfig.ConfigManager{}, rpc)
	cache := newBlockResultsCache(rpc, blockResultsCacheSize)
	bo.results = cache
	f := &HostEventFeed{
		bo:        bo,
		ring:      ring,
		results:   cache,
		epochs:    &epochBook{src: epochs},
		escrow:    escrow,
		localAddr: func() string { return "host-a" },
		notify:    make(chan struct{}, 1),
		sem:       make(chan struct{}, escrowLookupParallel),
	}
	bo.feedNotify = f.notify
	return f, bo, ring
}

func ringEvents(t *testing.T, ring *apiconfig.HostEventRing) []apiconfig.HostEvent {
	t.Helper()
	got := ring.Since(0, ring.Generation(), []apiconfig.HostEventKind{
		apiconfig.HostEventKindEscrowCreated,
		apiconfig.HostEventKindEscrowSettled,
		apiconfig.HostEventKindMaintenanceScheduled,
		apiconfig.HostEventKindMaintenanceCanceled,
		apiconfig.HostEventKindWarmKeyGranted,
		apiconfig.HostEventKindWarmKeyRevoked,
		apiconfig.HostEventKindBlock,
	})
	require.False(t, got.Reset)
	return got.Events
}

func kindsOf(events []apiconfig.HostEvent) []apiconfig.HostEventKind {
	out := make([]apiconfig.HostEventKind, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}

func heightsOf(events []apiconfig.HostEvent) []int64 {
	out := make([]int64, 0, len(events))
	for _, ev := range events {
		if ev.Block != nil {
			out = append(out, ev.Block.Height)
		}
	}
	return out
}

func blocksByHeight(events []apiconfig.HostEvent) map[int64]*apiconfig.BlockPayload {
	out := make(map[int64]*apiconfig.BlockPayload, len(events))
	for _, ev := range events {
		if ev.Block != nil {
			out[ev.Block.Height] = ev.Block
		}
	}
	return out
}

func waitGoroutines(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.LessOrEqual(t, runtime.NumGoroutine(), before+1)
}
