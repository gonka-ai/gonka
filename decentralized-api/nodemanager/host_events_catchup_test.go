package nodemanager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"common/nodemanager/gen"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type memLog struct {
	rows         []apiconfig.HostEvent
	readErr      error
	headErr      error
	generation   uint64
	afterOldest  func()
	afterResolve func()
}

func (m *memLog) Read(_ context.Context, afterSeq uint64, kinds []apiconfig.HostEventKind, limit int) ([]apiconfig.HostEvent, uint64, error) {
	if m.readErr != nil {
		return nil, 0, m.readErr
	}
	want := map[apiconfig.HostEventKind]struct{}{}
	for _, k := range kinds {
		want[k] = struct{}{}
	}
	var out []apiconfig.HostEvent
	for _, ev := range m.rows {
		if ev.Seq <= afterSeq {
			continue
		}
		if _, ok := want[ev.Kind]; !ok {
			continue
		}
		out = append(out, ev)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, m.generation, nil
}

func (m *memLog) ResolveBlock(_ context.Context, height int64) (uint64, bool, uint64, error) {
	if m.readErr != nil {
		return 0, false, 0, m.readErr
	}
	seq, found := uint64(0), false
	for _, ev := range m.rows {
		if ev.Kind == apiconfig.HostEventKindBlock && ev.Block != nil && ev.Block.Height == height {
			seq, found = ev.Seq, true
			break
		}
	}
	if m.afterResolve != nil {
		h := m.afterResolve
		m.afterResolve = nil
		h()
	}
	return seq, found, m.generation, nil
}

func (m *memLog) FeedHead(context.Context) (uint64, uint64, bool, error) {
	if m.headErr != nil {
		return 0, 0, true, m.headErr
	}
	if len(m.rows) == 0 {
		return 0, m.generation, true, nil
	}
	head := m.rows[0].Seq
	for _, ev := range m.rows[1:] {
		if ev.Seq > head {
			head = ev.Seq
		}
	}
	return head, m.generation, false, nil
}

func (m *memLog) Oldest(context.Context) (uint64, bool, uint64, error) {
	if m.readErr != nil {
		return 0, true, 0, m.readErr
	}
	if len(m.rows) == 0 {
		return 0, true, m.generation, nil
	}
	oldest := m.rows[0].Seq
	for _, ev := range m.rows[1:] {
		if ev.Seq < oldest {
			oldest = ev.Seq
		}
	}
	if m.afterOldest != nil {
		h := m.afterOldest
		m.afterOldest = nil
		h()
	}
	return oldest, false, m.generation, nil
}

type memLive struct {
	mu         sync.Mutex
	stalled    bool
	emitted    int64
	tip        int64
	unrestored bool
}

func (m *memLive) Stalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stalled
}
func (m *memLive) EmittedHeight() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.emitted
}
func (m *memLive) TipHeight() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tip
}
func (m *memLive) RingRestored() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.unrestored
}
func (m *memLive) setStalled(v bool) {
	m.mu.Lock()
	m.stalled = v
	m.mu.Unlock()
}
func (m *memLive) setTip(v int64) {
	m.mu.Lock()
	m.tip = v
	m.mu.Unlock()
}
func (m *memLive) setEmitted(v int64) {
	m.mu.Lock()
	m.emitted = v
	m.mu.Unlock()
}

func seqEvent(seq uint64, kind apiconfig.HostEventKind, height int64) apiconfig.HostEvent {
	ev := apiconfig.HostEvent{Seq: seq, Kind: kind}
	if kind == apiconfig.HostEventKindBlock {
		ev.Block = &apiconfig.BlockPayload{Height: height}
	}
	return ev
}

func restoreRows(ring *apiconfig.HostEventRing, rows []apiconfig.HostEvent, gen uint64) {
	tail := rows
	const keep = 64
	if len(tail) > keep {
		tail = tail[len(tail)-keep:]
	}
	next := uint64(1)
	if n := len(rows); n > 0 {
		next = rows[n-1].Seq + 1
	}
	ring.Restore(gen, next, tail)
}

func allKinds() []gen.HostEventKind {
	return []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
		gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_SCHEDULED,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
	}
}

func TestNodeManager_GetHostEvents_SupportedKinds(t *testing.T) {
	ring := testHostEventRing(t)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring))
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:  escrowSubscribe(),
		Generation: 1,
	})
	require.NoError(t, err)
	require.Equal(t, []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED,
		gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED,
		gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_SCHEDULED,
		gen.HostEventKind_HOST_EVENT_KIND_MAINTENANCE_CANCELED,
	}, resp.SupportedKinds)
}

func TestNodeManager_GetHostEvents_AfterHeightPagesWithoutBlocking(t *testing.T) {
	const generation = 7
	rows := make([]apiconfig.HostEvent, 0, 2501)
	rows = append(rows, seqEvent(1, apiconfig.HostEventKindBlock, 10))
	for i := 0; i < 2500; i++ {
		kind := apiconfig.HostEventKindEscrowCreated
		if i%2 == 0 {
			kind = apiconfig.HostEventKindBlock
		}
		rows = append(rows, seqEvent(uint64(i+2), kind, int64(11+i)))
	}
	ring := apiconfig.NewHostEventRing(128, generation)
	restoreRows(ring, rows, generation)
	log := &memLog{rows: rows}
	live := &memLive{emitted: 3000, tip: 3000}
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventLog(log), WithHostEventStatus(live))

	start := time.Now()
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         0,
		AfterHeight:    10,
		MaxWaitSeconds: 30,
		Subscribe:      allKinds(),
		Generation:     generation,
	})
	require.NoError(t, err)
	require.Less(t, time.Since(start), time.Second)
	require.False(t, resp.NeedsReset)
	require.Len(t, resp.Events, apiconfig.MaxHostEventsPage)
	require.Equal(t, uint64(2), resp.Events[0].Seq)
	require.Equal(t, resp.Events[len(resp.Events)-1].Seq, resp.NextCursor)

	next, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:      resp.NextCursor,
		AfterHeight: 10,
		Subscribe:   allKinds(),
		Generation:  generation,
	})
	require.NoError(t, err)
	require.False(t, next.NeedsReset)
	require.Len(t, next.Events, apiconfig.MaxHostEventsPage)
	require.Equal(t, resp.NextCursor+1, next.Events[0].Seq)
	require.NotEqual(t, resp.Events[len(resp.Events)-1].Seq, next.Events[0].Seq)
}

func TestNodeManager_GetHostEvents_CursorIgnoresAfterHeightAndRewinds(t *testing.T) {
	ring := testHostEventRing(t)
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated, Escrow: &apiconfig.EscrowPayload{EscrowID: 4}})
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowSettled, Escrow: &apiconfig.EscrowPayload{EscrowID: 4}})
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventStatus(&memLive{emitted: 9, tip: 9}))

	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:      1,
		AfterHeight: 1,
		Subscribe:   escrowSubscribe(),
		Generation:  1,
	})
	require.NoError(t, err)
	require.False(t, resp.NeedsReset)
	require.Len(t, resp.Events, 1)
	require.Equal(t, uint64(2), resp.Events[0].Seq)

	again, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:      resp.Events[0].Seq - 1,
		AfterHeight: 99,
		Subscribe:   escrowSubscribe(),
		Generation:  1,
	})
	require.NoError(t, err)
	require.Len(t, again.Events, 1)
	require.Equal(t, resp.Events[0].Seq, again.Events[0].Seq)
}

func TestNodeManager_GetHostEvents_AfterHeightBeforeFirstCommitIsUnavailable(t *testing.T) {
	ring := testHostEventRing(t)
	live := &memLive{emitted: 0, tip: 0}
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventStatus(live))

	start := time.Now()
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		AfterHeight:    40,
		MaxWaitSeconds: 30,
		Subscribe:      allKinds(),
		Generation:     1,
	})
	require.Nil(t, resp)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Contains(t, err.Error(), "no height committed")
	require.NotContains(t, err.Error(), "store read")
	require.Less(t, time.Since(start), time.Second)

	zero, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:  allKinds(),
		Generation: 1,
	})
	require.NoError(t, err)
	require.True(t, zero.Unchanged)
	require.Equal(t, ring.Head(), zero.NextCursor)

	live.emitted = 40
	live.tip = 40
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindBlock, Block: &apiconfig.BlockPayload{Height: 40}})
	caughtUp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		AfterHeight: 40,
		Subscribe:   allKinds(),
		Generation:  1,
	})
	require.NoError(t, err)
	require.False(t, caughtUp.NeedsReset)
	require.True(t, caughtUp.Unchanged)
	require.Equal(t, ring.Head(), caughtUp.NextCursor)
}

func TestNodeManager_GetHostEvents_UnrestoredAfterHeightBeforeFirstCommitIsUnavailable(t *testing.T) {
	rows := []apiconfig.HostEvent{seqEvent(1, apiconfig.HostEventKindBlock, 40)}
	ring := apiconfig.NewHostEventRing(16, 999)
	srv := NewServer(&mockBroker{}, nil, nil,
		WithHostEventRing(ring),
		WithHostEventLog(&memLog{rows: rows, generation: 7}),
		WithHostEventStatus(&memLive{unrestored: true}),
	)
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		AfterHeight:    40,
		MaxWaitSeconds: 30,
		Subscribe:      allKinds(),
		Generation:     7,
	})
	require.Nil(t, resp)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Contains(t, err.Error(), "no height committed")
}

func TestNodeManager_GetHostEvents_AfterHeightAtEmittedReturnsNextHeight(t *testing.T) {
	rows := []apiconfig.HostEvent{
		seqEvent(1, apiconfig.HostEventKindBlock, 5),
		seqEvent(2, apiconfig.HostEventKindEscrowCreated, 6),
		seqEvent(3, apiconfig.HostEventKindBlock, 6),
	}
	ring := apiconfig.NewHostEventRing(16, 1)
	restoreRows(ring, rows, 1)
	srv := NewServer(&mockBroker{}, nil, nil,
		WithHostEventRing(ring),
		WithHostEventLog(&memLog{rows: rows}),
		WithHostEventStatus(&memLive{emitted: 5, tip: 6}),
	)
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		AfterHeight: 5,
		Subscribe:   allKinds(),
		Generation:  1,
	})
	require.NoError(t, err)
	require.False(t, resp.NeedsReset)
	require.Len(t, resp.Events, 2)
	require.Equal(t, uint64(2), resp.Events[0].Seq)
	require.Equal(t, uint64(3), resp.Events[1].Seq)
	require.Equal(t, gen.HostEventKind_HOST_EVENT_KIND_ESCROW_CREATED, resp.Events[0].Kind)
	require.Equal(t, int64(6), resp.Events[1].Block.Height)
}

func TestNodeManager_GetHostEvents_AfterHeightAboveEmittedStartsAtHead(t *testing.T) {
	ring := testHostEventRing(t)
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindBlock, Block: &apiconfig.BlockPayload{Height: 10}})
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventStatus(&memLive{emitted: 10, tip: 10}))
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		AfterHeight: 11,
		Subscribe:   allKinds(),
		Generation:  1,
	})
	require.NoError(t, err)
	require.False(t, resp.NeedsReset)
	require.True(t, resp.Unchanged)
	require.Empty(t, resp.Events)
	require.Equal(t, ring.Head(), resp.NextCursor)
}

func TestNodeManager_GetHostEvents_RingWrapFallsBackToStore(t *testing.T) {
	rows := []apiconfig.HostEvent{
		seqEvent(1, apiconfig.HostEventKindBlock, 1),
		seqEvent(2, apiconfig.HostEventKindBlock, 2),
		seqEvent(3, apiconfig.HostEventKindBlock, 3),
		seqEvent(4, apiconfig.HostEventKindBlock, 4),
	}
	ring := apiconfig.NewHostEventRing(4, 1)
	restoreRows(ring, rows, 1)
	log := &memLog{rows: append([]apiconfig.HostEvent(nil), rows...), generation: 1}
	log.afterResolve = func() {
		for h := 5; h <= 6; h++ {
			ev := seqEvent(uint64(h), apiconfig.HostEventKindBlock, int64(h))
			ring.Append(ev)
			log.rows = append(log.rows, ev)
		}
	}
	live := &memLive{emitted: 4, tip: 4}
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventLog(log), WithHostEventStatus(live))

	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		AfterHeight: 1,
		Subscribe:   []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_BLOCK},
		Generation:  1,
	})
	require.NoError(t, err)
	require.False(t, resp.NeedsReset)
	require.NotEmpty(t, resp.Events)
	require.Equal(t, uint64(2), resp.Events[0].Seq)
}

func TestNodeManager_GetHostEvents_AfterHeightAtFreshGenerationWaitsForTip(t *testing.T) {
	ring := testHostEventRing(t)
	live := &memLive{emitted: 9, tip: 10}
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventStatus(live))

	go func() {
		time.Sleep(200 * time.Millisecond)
		live.setEmitted(10)
		ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindBlock, Block: &apiconfig.BlockPayload{Height: 10}})
	}()
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		AfterHeight:    9,
		MaxWaitSeconds: 2,
		Subscribe:      []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_BLOCK},
		Generation:     1,
	})
	require.NoError(t, err)
	require.False(t, resp.NeedsReset)
	require.Len(t, resp.Events, 1)
	require.Equal(t, int64(10), resp.Events[0].Block.Height)
}

func TestNodeManager_GetHostEvents_AfterHeightBeforeGenerationFirstStartsAtOldest(t *testing.T) {
	rows := []apiconfig.HostEvent{
		seqEvent(1, apiconfig.HostEventKindBlock, 100),
		seqEvent(2, apiconfig.HostEventKindEscrowCreated, 0),
		seqEvent(3, apiconfig.HostEventKindBlock, 101),
	}
	ring := apiconfig.NewHostEventRing(16, 6)
	restoreRows(ring, rows, 6)
	srv := NewServer(&mockBroker{}, nil, nil,
		WithHostEventRing(ring),
		WithHostEventLog(&memLog{rows: rows, generation: 6}),
		WithHostEventStatus(&memLive{emitted: 101, tip: 101}),
	)
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		AfterHeight: 99,
		Subscribe:   allKinds(),
		Generation:  6,
	})
	require.NoError(t, err)
	require.False(t, resp.NeedsReset)
	require.Equal(t, uint64(1), resp.Events[0].Seq)
	require.Equal(t, int64(100), resp.Events[0].Block.Height)
}

func TestNodeManager_GetHostEvents_AfterHeightAtTipWaits(t *testing.T) {
	ring := testHostEventRing(t)
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindBlock, Block: &apiconfig.BlockPayload{Height: 10}})
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventStatus(&memLive{emitted: 10, tip: 10}))

	start := time.Now()
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		AfterHeight:    10,
		MaxWaitSeconds: 1,
		Subscribe:      allKinds(),
		Generation:     1,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond)
	require.True(t, resp.Unchanged)
	require.False(t, resp.NeedsReset)
	require.Empty(t, resp.Events)
	require.Equal(t, ring.Head(), resp.NextCursor)
}

func TestNodeManager_GetHostEvents_CursorZeroPagesFullRing(t *testing.T) {
	const n = apiconfig.DefaultHostEventRingCapacity
	ring := apiconfig.NewHostEventRing(n, 1)
	for i := 0; i < n; i++ {
		ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated})
	}
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring))
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:  escrowSubscribe(),
		Generation: 1,
	})
	require.NoError(t, err)
	require.Len(t, resp.Events, apiconfig.MaxHostEventsPage)
	require.Equal(t, uint64(1), resp.Events[0].Seq)
	require.Equal(t, uint64(apiconfig.MaxHostEventsPage), resp.NextCursor)

	next, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     resp.NextCursor,
		Subscribe:  escrowSubscribe(),
		Generation: 1,
	})
	require.NoError(t, err)
	require.Equal(t, resp.NextCursor+1, next.Events[0].Seq)
}

func TestNodeManager_GetHostEvents_PageLimitAcrossRingAndStore(t *testing.T) {
	rows := make([]apiconfig.HostEvent, 2000)
	for i := range rows {
		rows[i] = seqEvent(uint64(i+1), apiconfig.HostEventKindEscrowCreated, 0)
	}
	ring := apiconfig.NewHostEventRing(128, 3)
	restoreRows(ring, rows, 3)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventLog(&memLog{rows: rows}))

	fromStore, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     400,
		Subscribe:  escrowSubscribe(),
		Generation: 3,
	})
	require.NoError(t, err)
	require.Len(t, fromStore.Events, apiconfig.MaxHostEventsPage)
	require.Equal(t, uint64(401), fromStore.Events[0].Seq)
	require.LessOrEqual(t, len(fromStore.Events), apiconfig.MaxHostEventsPage)

	ringOnly := apiconfig.NewHostEventRing(2048, 1)
	for i := 0; i < 1500; i++ {
		ringOnly.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindBlock, Block: &apiconfig.BlockPayload{Height: int64(i + 1)}})
	}
	srv = NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ringOnly))
	fromRing, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:  []gen.HostEventKind{gen.HostEventKind_HOST_EVENT_KIND_BLOCK},
		Generation: 1,
	})
	require.NoError(t, err)
	require.Len(t, fromRing.Events, apiconfig.MaxHostEventsPage)
}

func TestNodeManager_GetHostEvents_FilteredStoreReadJumpsToHead(t *testing.T) {
	const n = 50000
	rows := make([]apiconfig.HostEvent, n)
	escrowAt := map[uint64]bool{100: true, 200: true, 300: true}
	for i := range rows {
		seq := uint64(i + 1)
		kind := apiconfig.HostEventKindMaintenanceScheduled
		if escrowAt[seq] {
			kind = apiconfig.HostEventKindEscrowCreated
		}
		rows[i] = seqEvent(seq, kind, 0)
	}
	ring := apiconfig.NewHostEventRing(128, 4)
	restoreRows(ring, rows, 4)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventLog(&memLog{rows: rows}))

	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:  escrowSubscribe(),
		Generation: 4,
	})
	require.NoError(t, err)
	require.Len(t, resp.Events, 3)
	require.Equal(t, []uint64{100, 200, 300}, []uint64{resp.Events[0].Seq, resp.Events[1].Seq, resp.Events[2].Seq})
	require.Equal(t, ring.Head(), resp.NextCursor)
}

func TestNodeManager_GetHostEvents_PageMayEndBeforeBlock(t *testing.T) {
	rows := make([]apiconfig.HostEvent, 0, 1005)
	for seq := uint64(1); seq <= 1000; seq++ {
		rows = append(rows, seqEvent(seq, apiconfig.HostEventKindEscrowCreated, 8))
	}
	for seq := uint64(1001); seq <= 1004; seq++ {
		rows = append(rows, seqEvent(seq, apiconfig.HostEventKindEscrowSettled, 8))
	}
	rows = append(rows, seqEvent(1005, apiconfig.HostEventKindBlock, 8))
	ring := apiconfig.NewHostEventRing(128, 2)
	restoreRows(ring, rows, 2)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventLog(&memLog{rows: rows}))

	subscribe := append(allKinds(), gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED)
	first, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:  subscribe,
		Generation: 2,
	})
	require.NoError(t, err)
	require.Len(t, first.Events, apiconfig.MaxHostEventsPage)
	require.NotEqual(t, gen.HostEventKind_HOST_EVENT_KIND_BLOCK, first.Events[len(first.Events)-1].Kind)

	second, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     first.NextCursor,
		Subscribe:  subscribe,
		Generation: 2,
	})
	require.NoError(t, err)
	require.Equal(t, gen.HostEventKind_HOST_EVENT_KIND_ESCROW_SETTLED, second.Events[0].Kind)
	require.Equal(t, uint64(1001), second.Events[0].Seq)
	var sawBlock bool
	for _, ev := range second.Events {
		if ev.Kind == gen.HostEventKind_HOST_EVENT_KIND_BLOCK {
			sawBlock = true
			require.Equal(t, int64(8), ev.Block.Height)
		}
	}
	require.True(t, sawBlock)
}

func TestNodeManager_GetHostEvents_HotTailSurvivesGeneration(t *testing.T) {
	ring := apiconfig.NewHostEventRing(16, 1)
	rows := []apiconfig.HostEvent{
		seqEvent(4, apiconfig.HostEventKindEscrowCreated, 0),
		seqEvent(5, apiconfig.HostEventKindBlock, 3),
	}
	ring.Restore(9, 6, rows)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventLog(&memLog{rows: rows}))
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     4,
		Subscribe:  allKinds(),
		Generation: 9,
	})
	require.NoError(t, err)
	require.False(t, resp.NeedsReset)
	require.Len(t, resp.Events, 1)
	require.Equal(t, uint64(5), resp.Events[0].Seq)
	require.Equal(t, uint64(9), resp.Generation)
}

func TestNodeManager_GetHostEvents_NeedsResetCases(t *testing.T) {
	rows := []apiconfig.HostEvent{
		seqEvent(10, apiconfig.HostEventKindBlock, 4),
		seqEvent(11, apiconfig.HostEventKindEscrowCreated, 0),
	}
	ring := apiconfig.NewHostEventRing(16, 6)
	ring.Restore(6, 12, rows)
	log := &memLog{rows: rows}
	live := &memLive{emitted: 20, tip: 20}
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventLog(log), WithHostEventStatus(live))

	cases := []struct {
		name string
		req  *gen.GetHostEventsRequest
	}{
		{name: "pruned cursor", req: &gen.GetHostEventsRequest{Cursor: 3, Subscribe: allKinds(), Generation: 6}},
		{name: "above head", req: &gen.GetHostEventsRequest{Cursor: 50, Subscribe: allKinds(), Generation: 6}},
		{name: "generation", req: &gen.GetHostEventsRequest{Cursor: 10, Subscribe: allKinds(), Generation: 1}},
		{name: "pruned after_height", req: &gen.GetHostEventsRequest{AfterHeight: 2, Subscribe: allKinds(), Generation: 6}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := srv.GetHostEvents(context.Background(), tc.req)
			require.NoError(t, err)
			require.True(t, resp.NeedsReset)
			require.Empty(t, resp.Events)
			require.Equal(t, uint64(6), resp.Generation)
			require.Equal(t, ring.Head(), resp.NextCursor)
		})
	}
}

func TestNodeManager_GetHostEvents_StoreGenerationMismatchNeedsReset(t *testing.T) {
	ring := apiconfig.NewHostEventRing(16, 6)
	ring.Restore(6, 92, []apiconfig.HostEvent{
		seqEvent(90, apiconfig.HostEventKindBlock, 90),
		seqEvent(91, apiconfig.HostEventKindBlock, 91),
	})
	log := &memLog{
		generation: 9,
		rows: []apiconfig.HostEvent{
			seqEvent(1, apiconfig.HostEventKindBlock, 200),
			seqEvent(2, apiconfig.HostEventKindEscrowCreated, 200),
		},
	}
	srv := NewServer(&mockBroker{}, nil, nil,
		WithHostEventRing(ring),
		WithHostEventLog(log),
		WithHostEventStatus(&memLive{emitted: 91, tip: 91}),
	)
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     50,
		Subscribe:  allKinds(),
		Generation: 6,
	})
	require.NoError(t, err)
	require.True(t, resp.NeedsReset)
	require.Empty(t, resp.Events)
	require.Equal(t, uint64(9), resp.Generation)
	require.Equal(t, uint64(2), resp.NextCursor)

	after, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		AfterHeight: 80,
		Subscribe:   allKinds(),
		Generation:  6,
	})
	require.NoError(t, err)
	require.True(t, after.NeedsReset)
	require.Empty(t, after.Events)
	require.Equal(t, uint64(9), after.Generation)
}

func TestNodeManager_GetHostEvents_PruneBetweenOldestAndReadNeedsReset(t *testing.T) {
	storeRows := make([]apiconfig.HostEvent, 0, 21)
	for seq := uint64(10); seq <= 30; seq++ {
		storeRows = append(storeRows, seqEvent(seq, apiconfig.HostEventKindBlock, int64(seq)))
	}
	ring := apiconfig.NewHostEventRing(16, 6)
	ring.Restore(6, 31, []apiconfig.HostEvent{
		seqEvent(29, apiconfig.HostEventKindBlock, 29),
		seqEvent(30, apiconfig.HostEventKindBlock, 30),
	})
	log := &memLog{rows: storeRows}
	log.afterOldest = func() {
		kept := make([]apiconfig.HostEvent, 0, 11)
		for _, ev := range log.rows {
			if ev.Seq >= 20 {
				kept = append(kept, ev)
			}
		}
		log.rows = kept
	}
	srv := NewServer(&mockBroker{}, nil, nil,
		WithHostEventRing(ring),
		WithHostEventLog(log),
		WithHostEventStatus(&memLive{emitted: 30, tip: 30}),
	)
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     9,
		Subscribe:  allKinds(),
		Generation: 6,
	})
	require.NoError(t, err)
	require.True(t, resp.NeedsReset)
	require.Empty(t, resp.Events)
	require.Equal(t, uint64(6), resp.Generation)
	require.Equal(t, ring.Head(), resp.NextCursor)
}

func TestNodeManager_GetHostEvents_FreshGenerationResetThenContinues(t *testing.T) {
	rows := []apiconfig.HostEvent{seqEvent(1, apiconfig.HostEventKindBlock, 40)}
	ring := apiconfig.NewHostEventRing(16, 8)
	ring.Restore(8, 2, rows)
	srv := NewServer(&mockBroker{}, nil, nil,
		WithHostEventRing(ring),
		WithHostEventLog(&memLog{rows: rows}),
		WithHostEventStatus(&memLive{emitted: 40, tip: 40}),
	)
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		AfterHeight: 35,
		Subscribe:   allKinds(),
		Generation:  8,
	})
	require.NoError(t, err)
	require.True(t, resp.NeedsReset)
	require.Empty(t, resp.Events)
	require.Equal(t, ring.Head(), resp.NextCursor)

	follow, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:      resp.NextCursor,
		AfterHeight: 0,
		Subscribe:   allKinds(),
		Generation:  resp.Generation,
	})
	require.NoError(t, err)
	require.False(t, follow.NeedsReset)
	require.Equal(t, uint64(8), follow.Generation)
}

func TestNodeManager_GetHostEvents_EscrowLoadOnlyForEscrowSubscribers(t *testing.T) {
	rows := make([]apiconfig.HostEvent, 80)
	rows[0] = seqEvent(1, apiconfig.HostEventKindBlock, 1)
	for i := 1; i < len(rows); i++ {
		rows[i] = seqEvent(uint64(i+1), apiconfig.HostEventKindMaintenanceScheduled, 0)
	}
	ring := apiconfig.NewHostEventRing(16, 1)
	restoreRows(ring, rows, 1)
	load := broker.NewEscrowLoadTracker(time.Minute)
	load.Record("7")
	srv := NewServer(&mockBroker{}, nil, nil,
		WithHostEventRing(ring),
		WithHostEventLog(&memLog{rows: rows}),
		WithEscrowLoadTracker(load),
	)
	blockOnly := []gen.HostEventKind{
		gen.HostEventKind_HOST_EVENT_KIND_BLOCK,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_GRANTED,
		gen.HostEventKind_HOST_EVENT_KIND_WARM_KEY_REVOKED,
	}
	page, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:  blockOnly,
		Generation: 1,
	})
	require.NoError(t, err)
	require.NotEmpty(t, page.Events)
	require.Empty(t, page.EscrowLoad)

	live, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     page.NextCursor,
		Subscribe:  blockOnly,
		Generation: 1,
	})
	require.NoError(t, err)
	require.True(t, live.Unchanged)
	require.Empty(t, live.EscrowLoad)

	escrow, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     page.NextCursor,
		Subscribe:  escrowSubscribe(),
		Generation: 1,
	})
	require.NoError(t, err)
	require.Len(t, escrow.EscrowLoad, 1)
	require.Equal(t, uint64(7), escrow.EscrowLoad[0].EscrowId)
}

func TestNodeManager_GetHostEvents_StalledFeedServesCommittedThenUnavailable(t *testing.T) {
	ring := testHostEventRing(t)
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated, Escrow: &apiconfig.EscrowPayload{EscrowID: 1}})
	live := &memLive{stalled: true, emitted: 4, tip: 4}
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventStatus(live))

	behind, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:      escrowSubscribe(),
		Generation:     1,
		MaxWaitSeconds: 5,
	})
	require.NoError(t, err)
	require.Len(t, behind.Events, 1)
	require.False(t, behind.NeedsReset)

	start := time.Now()
	_, err = srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         behind.NextCursor,
		Subscribe:      escrowSubscribe(),
		Generation:     behind.Generation,
		MaxWaitSeconds: 5,
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Less(t, time.Since(start), time.Second)
	require.Contains(t, err.Error(), "height 5")

	live.stalled = false
	live.tip = 4
	recovered, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     behind.NextCursor,
		Subscribe:  escrowSubscribe(),
		Generation: behind.Generation,
	})
	require.NoError(t, err)
	require.False(t, recovered.NeedsReset)
	require.Equal(t, behind.Generation, recovered.Generation)
	require.Equal(t, behind.NextCursor, recovered.NextCursor)
}

func TestNodeManager_GetHostEvents_BehindTipIsUnavailable(t *testing.T) {
	ring := testHostEventRing(t)
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventStatus(&memLive{emitted: 1, tip: 10}))
	_, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:      escrowSubscribe(),
		Generation:     1,
		MaxWaitSeconds: 5,
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Contains(t, err.Error(), "height 2")
}

func TestNodeManager_GetHostEvents_TimeoutReportsStall(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*memLive)
		height string
	}{
		{name: "store stalls during the wait", mutate: func(live *memLive) { live.setStalled(true) }, height: "height 11"},
		{name: "tip moves more than three blocks ahead", mutate: func(live *memLive) { live.setTip(14) }, height: "height 11"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ring := testHostEventRing(t)
			ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated, Escrow: &apiconfig.EscrowPayload{EscrowID: 1}})
			live := &memLive{emitted: 10, tip: 10}
			srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventStatus(live))

			go func() {
				time.Sleep(200 * time.Millisecond)
				tc.mutate(live)
			}()
			start := time.Now()
			resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
				Cursor:         ring.Head(),
				Subscribe:      escrowSubscribe(),
				Generation:     1,
				MaxWaitSeconds: 1,
			})
			require.Nil(t, resp)
			require.Equal(t, codes.Unavailable, status.Code(err))
			require.Contains(t, err.Error(), tc.height)
			require.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond)
		})
	}
}

func TestNodeManager_GetHostEvents_TimeoutKeepsEventsWhenStallStarts(t *testing.T) {
	ring := testHostEventRing(t)
	ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated, Escrow: &apiconfig.EscrowPayload{EscrowID: 1}})
	head := ring.Head()
	live := &memLive{emitted: 10, tip: 10}
	srv := NewServer(&mockBroker{}, nil, nil, WithHostEventRing(ring), WithHostEventStatus(live))

	go func() {
		time.Sleep(200 * time.Millisecond)
		live.setStalled(true)
		ring.Append(apiconfig.HostEvent{Kind: apiconfig.HostEventKindEscrowCreated, Escrow: &apiconfig.EscrowPayload{EscrowID: 2}})
	}()
	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         head,
		Subscribe:      escrowSubscribe(),
		Generation:     1,
		MaxWaitSeconds: 1,
	})
	require.NoError(t, err)
	require.Len(t, resp.Events, 1)
	require.Equal(t, uint64(2), resp.Events[0].Escrow.EscrowId)
	require.False(t, resp.NeedsReset)
}

func TestNodeManager_GetHostEvents_StoreReadUnavailable(t *testing.T) {
	rows := make([]apiconfig.HostEvent, 100)
	for i := range rows {
		rows[i] = seqEvent(uint64(i+1), apiconfig.HostEventKindEscrowCreated, 0)
	}
	ring := apiconfig.NewHostEventRing(16, 3)
	restoreRows(ring, rows, 3)
	srv := NewServer(&mockBroker{}, nil, nil,
		WithHostEventRing(ring),
		WithHostEventLog(&memLog{rows: rows, readErr: errors.New("db down")}),
	)
	_, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:  escrowSubscribe(),
		Generation: 3,
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NotContains(t, err.Error(), "needs_reset")
}

func TestNodeManager_GetHostEvents_CanceledStoreReadKeepsContextStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code codes.Code
	}{
		{name: "canceled", err: context.Canceled, code: codes.Canceled},
		{name: "deadline", err: fmt.Errorf("read rows: %w", context.DeadlineExceeded), code: codes.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := make([]apiconfig.HostEvent, 100)
			for i := range rows {
				rows[i] = seqEvent(uint64(i+1), apiconfig.HostEventKindEscrowCreated, 0)
			}
			ring := apiconfig.NewHostEventRing(16, 3)
			restoreRows(ring, rows, 3)
			srv := NewServer(&mockBroker{}, nil, nil,
				WithHostEventRing(ring),
				WithHostEventLog(&memLog{rows: rows, readErr: tc.err}),
			)
			_, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
				Subscribe:  escrowSubscribe(),
				Generation: 3,
			})
			require.Equal(t, tc.code, status.Code(err))
			require.NotEqual(t, codes.Unavailable, status.Code(err))
		})
	}
}

func TestNodeManager_GetHostEvents_UnrestoredRingUsesStoreHead(t *testing.T) {
	const storeGen = 7
	const bootNonce = 999
	rows := make([]apiconfig.HostEvent, 10)
	for i := range rows {
		rows[i] = seqEvent(uint64(i+1), apiconfig.HostEventKindEscrowCreated, 0)
	}
	ring := apiconfig.NewHostEventRing(16, bootNonce)
	log := &memLog{rows: rows, generation: storeGen}
	live := &memLive{unrestored: true, stalled: true, emitted: 10, tip: 10}
	srv := NewServer(&mockBroker{}, nil, nil,
		WithHostEventRing(ring),
		WithHostEventLog(log),
		WithHostEventStatus(live),
	)

	resp, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     4,
		Subscribe:  escrowSubscribe(),
		Generation: storeGen,
	})
	require.NoError(t, err)
	require.False(t, resp.NeedsReset)
	require.Equal(t, uint64(storeGen), resp.Generation)
	require.Len(t, resp.Events, 6)
	require.Equal(t, uint64(5), resp.Events[0].Seq)
	require.Equal(t, uint64(10), resp.NextCursor)

	start := time.Now()
	_, err = srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:         resp.NextCursor,
		Subscribe:      escrowSubscribe(),
		Generation:     storeGen,
		MaxWaitSeconds: 5,
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NotContains(t, err.Error(), "needs_reset")
	require.Less(t, time.Since(start), time.Second)

	reset, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     4,
		Subscribe:  escrowSubscribe(),
		Generation: 3,
	})
	require.NoError(t, err)
	require.True(t, reset.NeedsReset)
	require.Empty(t, reset.Events)
	require.Equal(t, uint64(storeGen), reset.Generation)
	require.Equal(t, uint64(10), reset.NextCursor)

	fromStart, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Subscribe:  escrowSubscribe(),
		Generation: 0,
	})
	require.NoError(t, err)
	require.False(t, fromStart.NeedsReset)
	require.Equal(t, uint64(storeGen), fromStart.Generation)
	require.Equal(t, uint64(1), fromStart.Events[0].Seq)
}

func TestNodeManager_GetHostEvents_UnrestoredHeadReadUnavailable(t *testing.T) {
	ring := apiconfig.NewHostEventRing(16, 999)
	srv := NewServer(&mockBroker{}, nil, nil,
		WithHostEventRing(ring),
		WithHostEventLog(&memLog{headErr: errors.New("db down"), generation: 7}),
		WithHostEventStatus(&memLive{unrestored: true, stalled: true}),
	)
	_, err := srv.GetHostEvents(context.Background(), &gen.GetHostEventsRequest{
		Cursor:     4,
		Subscribe:  escrowSubscribe(),
		Generation: 7,
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.NotContains(t, err.Error(), "needs_reset")
}
