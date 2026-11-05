package state

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/chainoracle/blocks/headertime"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

type headerTimes struct {
	mu    sync.Mutex
	times map[uint64]int64
}

func newHeaderTimes() *headerTimes {
	return &headerTimes{times: make(map[uint64]int64)}
}

func (h *headerTimes) set(height uint64, at time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.times[height] = at.Milliseconds()
}

func (h *headerTimes) HeaderTimeMs(height uint64, _ []byte) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ms, ok := h.times[height]
	if !ok {
		return 0, fmt.Errorf("%w: height %d", types.ErrHeaderPending, height)
	}
	return ms, nil
}

// StoredAt lets compose ResolveObservedTimeMs see the same rows HeaderTimeMs
// returns, without fetching.
func (h *headerTimes) StoredAt(height uint64) (headertime.Record, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ms, ok := h.times[height]
	if !ok || ms <= 0 {
		return headertime.Record{}, false
	}
	return headertime.Record{TimeMs: ms}, true
}

// catchUpTimes records EnsureFrom calls so WarmUnfinishedHeaders can be checked
// without a live block filler.
type catchUpTimes struct {
	*headerTimes
	mu      sync.Mutex
	ensured []uint64
}

func (c *catchUpTimes) EnsureFrom(height uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensured = append(c.ensured, height)
}

func (c *catchUpTimes) takeEnsured() []uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]uint64(nil), c.ensured...)
	c.ensured = nil
	return out
}

// fillRecorder is a headertime.Filler that remembers which heights catch-up asked for.
type fillRecorder struct {
	mu   sync.Mutex
	got  []int64
	base time.Time
}

func (f *fillRecorder) Fill(_ context.Context, height int64) (headertime.Fill, error) {
	f.mu.Lock()
	f.got = append(f.got, height)
	f.mu.Unlock()
	return headertime.Fill{
		Header: blocks.HashOnlyHeader(height, f.base.Add(time.Duration(height)*time.Millisecond), "c", []byte{byte(height)}),
		Origin: headertime.OriginChain,
	}, nil
}

func (f *fillRecorder) take() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.got...)
}

func newHeaderTimeDriver(t *testing.T) (*floorDriver, *headerTimes) {
	d := newFloorDriver(t)
	src := newHeaderTimes()
	d.sm.headerTimes = src
	return d, src
}

func TestTimeoutCountsBlockTimeNotHeights(t *testing.T) {
	d, _ := newHeaderTimeDriver(t)
	refusal := time.Duration(d.sm.Config().RefusalTimeout) * time.Second
	hash := []byte{0x01, 0x00}
	anchor := time.Hour
	d.raiseWithTime(100, hash, anchor.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, anchor.Milliseconds(), rec.StartedAtTimeMs)

	d.raiseWithTime(100_000, []byte{0x02, 0x00}, (anchor + refusal - time.Millisecond).Milliseconds())
	remaining, err := d.sm.TimeoutRemaining(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.NoError(t, err)
	require.Equal(t, time.Millisecond, remaining, "a long run of fast blocks is not the timeout")
	require.ErrorIs(t, d.sm.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED), types.ErrTimeoutNotDue,
		"TimeoutDue ages from carried stamp/floor times, not block count")

	d.raiseWithTime(100_001, []byte{0x02, 0x01}, (anchor + refusal).Milliseconds())
	require.NoError(t, d.sm.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED))
	require.NoError(t, d.apply(d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)))
	rec, _ = d.sm.GetInference(id)
	require.Equal(t, types.StatusTimedOut, rec.Status)
}

func TestOneSlowBlockCoversTheWholeWindow(t *testing.T) {
	d, _ := newHeaderTimeDriver(t)
	exec := time.Duration(d.sm.Config().ExecutionTimeout) * time.Second
	hash := []byte{0x01, 0x00}
	anchor := time.Hour
	d.raiseWithTime(100, hash, anchor.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
	require.NoError(t, d.apply(d.confirmWithTime(id, 100, hash, anchor.Milliseconds())))
	rec, _ := d.sm.GetInference(id)
	require.Equal(t, anchor.Milliseconds(), rec.ConfirmedAtTimeMs)

	d.raiseWithTime(101, []byte{0x01, 0x01}, (anchor + exec).Milliseconds())
	require.NoError(t, d.apply(d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)),
		"a halted chain resumes with a floor time that covers the stall")
}

func TestMissingCarriedFloorTimeBlocksTimeoutDue(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	anchor := time.Hour
	d.raiseWithTime(100, hash, anchor.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
	// Tip floor raised without carried time and without a stored map row.
	d.raiseWithTime(1_000_000, []byte{0x0f, 0x00}, 0)
	src.set(1_000_000, 0)

	require.ErrorIs(t, d.sm.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED), types.ErrHeaderPending,
		"TimeoutDue is pending when floor TimeMs and StoredAt both miss — no fetch under lock")
	// Apply does not read times: votes alone commit the timeout.
	require.NoError(t, d.apply(d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusTimedOut, rec.Status)

	next := d.next()
	require.NoError(t, d.start(next, 1_000_000, []byte{0x0f, 0x00}),
		"a start applies from the stamp and the log when carried time is zero")
	rec, ok = d.sm.GetInference(next)
	require.True(t, ok)
	require.Zero(t, rec.StartedAtTimeMs)
}

func TestSettlementWaitsForCarriedFloorTime(t *testing.T) {
	d, _ := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	anchor := time.Hour
	d.raiseWithTime(100, hash, anchor.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
	d.raiseWithTime(200, []byte{0x02, 0x00}, 0)

	sealNonce := d.next()
	d.sm.mu.Lock()
	err := d.sm.drainLiveIntoSealedAccLocked(sealNonce)
	_, live := d.sm.state.Inferences[id]
	d.sm.mu.Unlock()
	require.ErrorIs(t, err, types.ErrFloorTimeMissing)
	require.NotErrorIs(t, err, types.ErrHeaderPending)
	require.True(t, live, "a drain that cannot read carried floor time leaves every record live")

	// Fill the tip floor time (first non-zero wins on the same height).
	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	slot := uint32(d.turn % uint64(len(d.hosts)))
	dueMs := (anchor + time.Duration(d.sm.Config().RefusalTimeout)*time.Second).Milliseconds()
	require.NoError(t, d.apply(divAckTxWithTime(t, d.hosts[slot], d.turn, ref, slot, 200, []byte{0x02, 0x00}, types.SyncState_SYNCED, dueMs)))

	d.sm.mu.Lock()
	rec := *d.sm.state.Inferences[id]
	due, err := d.sm.liveTimeoutDueAtDrainLocked(&rec, sealNonce)
	d.sm.mu.Unlock()
	require.NoError(t, err)
	require.True(t, due)
}

func TestAckAppliesWhileTheHeaderMapIsBehind(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	src.set(100, time.Hour)
	d.raise(100, hash)

	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	nonce := d.next()
	ack := divAckTx(t, d.hosts[1], d.turn, ref, 1, 1_000_000, []byte{0x0f, 0x00}, types.SyncState_SYNCED)
	started := time.Now()
	_, applied, err := d.sm.ApplyLocalBestEffort(nonce, []*types.DevshardTx{ack})
	require.NoError(t, err)
	require.Less(t, time.Since(started), 200*time.Millisecond, "a missing row does not wait on the poller")
	require.Len(t, applied, 1)
	require.NotNil(t, applied[0].GetHeightAck())
	h, got, known := d.sm.HeightSyncFloorAsOf(d.next())
	require.True(t, known)
	require.Equal(t, uint64(1_000_000), h)
	require.Equal(t, []byte{0x0f, 0x00}, got)
}

func TestAckWithADifferentStoredHashIsLeftOut(t *testing.T) {
	d := newFloorDriver(t)
	src := headertime.New(headertime.WithGateway(), headertime.WithMissWait(2*time.Second))
	d.sm.headerTimes = src
	at := time.Unix(1_800_000_000, 0).UTC()
	src.Observe(&blocks.Header{Height: 100, Time: at, BlockHash: []byte{0x01, 0x00}}, headertime.OriginDAPI, true)
	d.raise(100, []byte{0x01, 0x00})
	src.Observe(&blocks.Header{Height: 200, Time: at.Add(time.Second), BlockHash: []byte{0xaa}}, headertime.OriginDAPI, true)

	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	nonce := d.next()
	bad := divAckTx(t, d.hosts[1], d.turn, ref, 1, 200, []byte{0xbb}, types.SyncState_SYNCED)
	logs := captureStateLogs(t)
	started := time.Now()
	_, applied, err := d.sm.ApplyLocalBestEffort(nonce, []*types.DevshardTx{bad})
	require.NoError(t, err)
	require.Less(t, time.Since(started), 500*time.Millisecond)
	require.Empty(t, applied, "a contradicting ack is dropped, not retried")
	require.Contains(t, logs.String(), "host ack hash disagrees with the polled header")
	h, got, known := d.sm.HeightSyncFloorAsOf(d.next())
	require.True(t, known)
	require.Equal(t, uint64(100), h)
	require.Equal(t, []byte{0x01, 0x00}, got)
}

func TestSecondHostAckWithADifferentHashIsLeftOut(t *testing.T) {
	d := newFloorDriver(t)
	src := headertime.New(headertime.WithGateway())
	d.sm.headerTimes = src
	d.raise(200, []byte{0xaa})

	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	nonce := d.next()
	bad := divAckTx(t, d.hosts[1], d.turn, ref, 1, 200, []byte{0xbb}, types.SyncState_SYNCED)
	logs := captureStateLogs(t)
	_, applied, err := d.sm.ApplyLocalBestEffort(nonce, []*types.DevshardTx{bad})
	require.NoError(t, err)
	require.Empty(t, applied)
	require.Contains(t, logs.String(), "host ack hash disagrees with an earlier host ack")
	h, got, _ := d.sm.HeightSyncFloorAsOf(d.next())
	require.Equal(t, uint64(200), h)
	require.Equal(t, []byte{0xaa}, got)
}

func TestCommittedAckStillAppliesWhenThePolledHashDiffers(t *testing.T) {
	d := newFloorDriver(t)
	src := headertime.New(headertime.WithGateway())
	d.sm.headerTimes = src
	at := time.Unix(1_800_000_000, 0).UTC()
	src.Observe(&blocks.Header{Height: 300, Time: at, BlockHash: []byte{0xaa}}, headertime.OriginDAPI, true)
	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	logs := captureStateLogs(t)
	require.NoError(t, d.apply(divAckTx(t, d.hosts[1], d.turn, ref, 1, 300, []byte{0xbb}, types.SyncState_SYNCED)))
	require.Contains(t, logs.String(), "polled header hash disagrees with a host ack")
	h, got, known := d.sm.HeightSyncFloorAsOf(d.next())
	require.True(t, known)
	require.Equal(t, uint64(300), h)
	require.Equal(t, []byte{0xbb}, got)
}

func captureStateLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestSealClockReadsCarriedFloorTime(t *testing.T) {
	d, _ := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	anchor := time.Hour
	d.raiseWithTime(100, hash, anchor.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
	require.NoError(t, d.apply(d.confirmWithTime(id, 100, hash, anchor.Milliseconds())))

	clock := d.sm.AutoSealStateClock()
	require.True(t, clock.Known)
	require.Equal(t, anchor.Milliseconds(), clock.ClockMs)

	// Raising F advances the seal clock without a new confirm in the live set.
	d.raiseWithTime(5000, []byte{0x50, 0x00}, (2 * time.Hour).Milliseconds())
	clock = d.sm.AutoSealStateClock()
	require.True(t, clock.Known)
	require.Equal(t, (2 * time.Hour).Milliseconds(), clock.ClockMs)
	require.Equal(t, anchor.Milliseconds(), clock.MinConfirmedAtMs)
	require.Equal(t, anchor.Milliseconds(), clock.MaxConfirmedAtMs)
}

func TestReplayAppliesOnTheFirstAttemptWhenTheMapIsEmpty(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	src.set(100, time.Hour)
	d.raise(100, hash)
	src.set(200, 2*time.Hour)
	d.raise(200, []byte{0x02, 0x00})
	src.set(200, 0)
	nonce := d.next()
	start := divStartTx(nonce, 200, []byte{0x02, 0x00})

	started := time.Now()
	_, err := d.sm.ApplyLocalPersisted(nonce, []*types.DevshardTx{start})
	require.NoError(t, err)
	require.Less(t, time.Since(started), 200*time.Millisecond)
	rec, ok := d.sm.GetInference(nonce)
	require.True(t, ok)
	require.Zero(t, rec.StartedAtTimeMs)
}

func TestReplayAppliesATimeoutWithoutTheFloorHeader(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	src.set(100, time.Hour)
	d.raise(100, hash)
	id := d.next()
	require.NoError(t, d.start(id, 100, hash))
	src.set(100_000, 0)
	d.raise(100_000, []byte{0x0f, 0x00})

	nonce := d.next()
	timeoutTx := d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	started := time.Now()
	_, err := d.sm.ApplyLocalPersisted(nonce, []*types.DevshardTx{timeoutTx})
	require.NoError(t, err, "timeout apply counts votes only; the map is not required")
	require.Less(t, time.Since(started), 200*time.Millisecond)
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusTimedOut, rec.Status)
}

func TestReplayDrainFailsClosedWhenCarriedFloorTimeIsMissing(t *testing.T) {
	d, _ := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	anchor := time.Hour
	d.raiseWithTime(100, hash, anchor.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
	d.raiseWithTime(200, []byte{0x02, 0x00}, 0)

	sealNonce := d.next()
	d.sm.mu.Lock()
	d.sm.replayingPersisted = true
	started := time.Now()
	err := d.sm.drainLiveIntoSealedAccLocked(sealNonce)
	d.sm.replayingPersisted = false
	_, live := d.sm.state.Inferences[id]
	d.sm.mu.Unlock()
	require.ErrorIs(t, err, types.ErrFloorTimeMissing)
	require.NotErrorIs(t, err, types.ErrHeaderPending)
	require.Less(t, time.Since(started), 200*time.Millisecond)
	require.True(t, live, "replay does not refund when carried floor time is missing")

	// Fill tip floor time; drain due uses carried start + floor only (empty map).
	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	slot := uint32(d.turn % uint64(len(d.hosts)))
	dueMs := (2 * time.Hour).Milliseconds()
	require.NoError(t, d.apply(divAckTxWithTime(t, d.hosts[slot], d.turn, ref, slot, 200, []byte{0x02, 0x00}, types.SyncState_SYNCED, dueMs)))
	d.sm.SetHeaderTimes(headertime.New())

	d.sm.mu.Lock()
	rec := *d.sm.state.Inferences[id]
	due, err := d.sm.liveTimeoutDueAtDrainLocked(&rec, sealNonce)
	d.sm.mu.Unlock()
	require.NoError(t, err)
	require.True(t, due, "carried floor and start times decide the drain while the map is empty")
}

func TestLowestUnfinishedStartHeightIgnoresFinishedRecords(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	low := []byte{0x32, 0x00}
	src.set(50, time.Hour)
	d.raise(50, low)
	pending := d.next()
	require.NoError(t, d.start(pending, 50, low))

	hash := []byte{0x01, 0x00}
	src.set(100, time.Hour)
	d.raise(100, hash)
	finished := d.next()
	require.NoError(t, d.start(finished, 100, hash))
	require.NoError(t, d.apply(d.confirm(finished, 100, hash)))
	require.NoError(t, d.apply(d.stampedFinish(finished, 100, hash)))

	require.Equal(t, uint64(50), d.sm.LowestUnfinishedStartHeight())
}

func TestWarmUnfinishedHeadersAsksEnsureFromLowestUnfinishedStart(t *testing.T) {
	d := newFloorDriver(t)
	src := &catchUpTimes{headerTimes: newHeaderTimes()}
	d.sm.headerTimes = src

	low := []byte{0x32, 0x00}
	src.set(50, time.Hour)
	d.raise(50, low)
	require.NoError(t, d.start(d.next(), 50, low))

	mid := []byte{0x50, 0x00}
	src.set(80, 2*time.Hour)
	d.raise(80, mid)
	require.NoError(t, d.start(d.next(), 80, mid))

	done := []byte{0x64, 0x00}
	src.set(100, 3*time.Hour)
	d.raise(100, done)
	finished := d.next()
	require.NoError(t, d.start(finished, 100, done))
	require.NoError(t, d.apply(d.confirm(finished, 100, done)))
	require.NoError(t, d.apply(d.stampedFinish(finished, 100, done)))

	d.sm.WarmUnfinishedHeaders()
	require.Equal(t, []uint64{50}, src.takeEnsured())
}

func TestReplayDoesNotBlockAndWarmPollsFromLowestUnfinishedStart(t *testing.T) {
	d := newFloorDriver(t)
	seed := newHeaderTimes()
	d.sm.headerTimes = seed

	lowHash := []byte{0x32, 0x00}
	seed.set(50, time.Hour)
	d.raise(50, lowHash)
	live := d.next()
	require.NoError(t, d.start(live, 50, lowHash))

	tipHash := []byte{0x64, 0x00}
	seed.set(100, 2*time.Hour)
	d.raise(100, tipHash)

	// Replay a start while the floor header is absent: must not wait on the map.
	seed.set(100, 0)
	replayID := d.next()
	started := time.Now()
	_, err := d.sm.ApplyLocalPersisted(replayID, []*types.DevshardTx{divStartTx(replayID, 100, tipHash)})
	require.NoError(t, err)
	require.Less(t, time.Since(started), 200*time.Millisecond)
	require.Equal(t, uint64(50), d.sm.LowestUnfinishedStartHeight())

	base := time.Unix(1_800_000_000, 0)
	rec := &fillRecorder{base: base}
	src := headertime.New(headertime.WithFiller(rec))
	src.Observe(blocks.HashOnlyHeader(100, base.Add(100*time.Millisecond), "c", tipHash), headertime.OriginComet, true)
	d.sm.SetHeaderTimes(src)

	d.sm.WarmUnfinishedHeaders()
	require.Eventually(t, func() bool {
		for h := uint64(50); h < 100; h++ {
			if _, ok := src.StoredAt(h); !ok {
				return false
			}
		}
		return true
	}, 2*time.Second, 10*time.Millisecond)

	got := rec.take()
	require.NotEmpty(t, got)
	require.Contains(t, got, int64(50), "catch-up must ask the filler for the unfinished start height")
	for _, h := range got {
		require.GreaterOrEqual(t, h, int64(50))
		require.Less(t, h, int64(100))
	}
}

func TestCanonicalInferenceEntryOmitsHeaderTimes(t *testing.T) {
	base := &types.InferenceRecord{
		Status:             types.StatusStarted,
		ExecutorSlot:       1,
		Model:              "llama",
		PromptHash:         []byte("prompt"),
		InputLength:        100,
		MaxTokens:          testutil.TestMaxTokens,
		ReservedCost:       100,
		StartedAtHeight:    100,
		StartedAtBlockHash: []byte{0xaa},
		ConfirmedAtHeight:  101,
	}
	withMapTimes := *base
	withMapTimes.StartedAtTimeMs = time.Hour.Milliseconds()
	withMapTimes.ConfirmedAtTimeMs = (2 * time.Hour).Milliseconds()

	a, err := marshalInferenceEntry(1, base)
	require.NoError(t, err)
	b, err := marshalInferenceEntry(1, &withMapTimes)
	require.NoError(t, err)
	require.Equal(t, a, b, "header times from the map must not enter the canonical inference bytes")

	_, decoded, err := unmarshalInferenceEntry(b)
	require.NoError(t, err)
	require.Zero(t, decoded.StartedAtTimeMs)
	require.Zero(t, decoded.ConfirmedAtTimeMs)
	require.Equal(t, base.StartedAtHeight, decoded.StartedAtHeight)
	require.Equal(t, base.ConfirmedAtHeight, decoded.ConfirmedAtHeight)
}

func TestStateRootIgnoresHeaderMapDifferences(t *testing.T) {
	hosts := fourHosts(t)
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	hash := []byte{0xaa}
	headerMs := (3 * time.Hour).Milliseconds()

	newSM := func(src *headerTimes) *StateMachine {
		t.Helper()
		sm, err := NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), verifier,
			testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000),
			WithStateRootAndProtocolVersion("v6"),
			WithHeaderTimes(src),
		)
		require.NoError(t, err)
		seedFloor(t, sm, 100, hash)
		return sm
	}

	filled := newHeaderTimes()
	filled.set(100, time.Duration(headerMs)*time.Millisecond)
	withMap := newSM(filled)
	empty := newSM(newHeaderTimes())

	const id uint64 = 1
	executorSlot := group[id%uint64(len(group))].SlotID
	executor := hosts[executorSlot]
	start := divStartTx(id, 100, hash)
	confirm := divConfirmTx(t, executor, id, 100, hash)
	finish := divFinishTx(t, executor, id, executorSlot, 100, hash)

	for nonce, txs := range [][]*types.DevshardTx{{start}, {confirm}, {finish}} {
		diff := testutil.SignDiff(t, user, "escrow-1", uint64(nonce+1), txs)
		_, err := withMap.ApplyDiff(diff)
		require.NoError(t, err)
		_, err = empty.ApplyDiff(diff)
		require.NoError(t, err)
	}

	withRec, ok := withMap.GetInference(id)
	require.True(t, ok)
	emptyRec, ok := empty.GetInference(id)
	require.True(t, ok)
	require.Zero(t, withRec.StartedAtTimeMs)
	require.Zero(t, withRec.ConfirmedAtTimeMs)
	require.Zero(t, emptyRec.StartedAtTimeMs)
	require.Zero(t, emptyRec.ConfirmedAtTimeMs)

	withRoot, err := withMap.ComputeStateRoot()
	require.NoError(t, err)
	emptyRoot, err := empty.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, withRoot, emptyRoot, "replicas must agree when only the header map differs")
}

func TestStateRootUnchangedWhenHeaderMapFillsAfterApply(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0xaa}
	src.set(100, time.Hour)
	d.raise(100, hash)
	src.set(100, 0)

	id := d.next()
	require.NoError(t, d.start(id, 100, hash))
	require.NoError(t, d.apply(d.confirm(id, 100, hash)))
	require.NoError(t, d.apply(d.stampedFinish(id, 100, hash)))

	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Zero(t, rec.StartedAtTimeMs)
	require.Zero(t, rec.ConfirmedAtTimeMs)

	before, err := d.sm.ComputeStateRoot()
	require.NoError(t, err)
	entryBefore, err := marshalInferenceEntry(id, &rec)
	require.NoError(t, err)

	src.set(100, 5*time.Hour)
	rec.StartedAtTimeMs = (5 * time.Hour).Milliseconds()
	rec.ConfirmedAtTimeMs = (5 * time.Hour).Milliseconds()
	require.NoError(t, func() error {
		d.sm.mu.Lock()
		defer d.sm.mu.Unlock()
		live := d.sm.state.Inferences[id]
		live.StartedAtTimeMs = rec.StartedAtTimeMs
		live.ConfirmedAtTimeMs = rec.ConfirmedAtTimeMs
		return d.sm.updateCommittedEntryLocked(id, live)
	}())

	after, err := d.sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, before, after, "filling the map after apply must not move the state root")

	entryAfter, err := marshalInferenceEntry(id, &rec)
	require.NoError(t, err)
	require.Equal(t, entryBefore, entryAfter, "canonical record bytes contain no header-time field")
}

func (d *floorDriver) stampedFinish(id, height uint64, hash []byte) *types.DevshardTx {
	d.t.Helper()
	rec, ok := d.sm.GetInference(id)
	require.True(d.t, ok)
	finish := &types.MsgFinishInference{
		InferenceId: id, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
		InputTokens: 10, OutputTokens: 20, ExecutorSlot: rec.ExecutorSlot, EscrowId: "escrow-1",
		ObservedHeight: height, ObservedBlockHash: hash,
	}
	finish.ProposerSig = testutil.SignProposerTx(d.t, d.hosts[rec.ExecutorSlot], finish)
	return txFinish(finish)
}

func startedUnderHeaderTimes(t *testing.T) (*floorDriver, *headerTimes, uint64) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	src.set(100, time.Hour)
	d.raise(100, hash)
	id := d.next()
	require.NoError(t, d.start(id, 100, hash))
	require.NoError(t, d.apply(d.confirm(id, 100, hash)))
	return d, src, id
}

func TestEvidenceAcceptsAFinishWithoutTheHeaderTime(t *testing.T) {
	d, _, id := startedUnderHeaderTimes(t)
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	fake := d.stampedFinish(id, 1_000_000, []byte{0x0f, 0x00})

	require.NoError(t, d.sm.CheckEvidence(&rec, fake),
		"a Finish the stamp and the log accept is evidence while the map has no row")
}

func TestComposeAppliesAFinishWhileTheHeaderMapIsEmpty(t *testing.T) {
	d, _, id := startedUnderHeaderTimes(t)
	finish := d.stampedFinish(id, 200, []byte{0x02, 0x00})

	nonce := d.next()
	started := time.Now()
	_, applied, err := d.sm.ApplyLocalBestEffort(nonce, []*types.DevshardTx{finish, divHeartbeatTx(0, nil)})
	require.NoError(t, err)
	require.Less(t, time.Since(started), 200*time.Millisecond)
	require.Len(t, applied, 2)
	rec, _ := d.sm.GetInference(id)
	require.Equal(t, types.StatusFinished, rec.Status)
}

func TestApplyPathsInstallAFloorRaiseWithoutTheHeaderTime(t *testing.T) {
	for _, path := range []string{"ApplyDiff", "ValidateDiff", "ApplyLocal", "ApplyLocalPersisted"} {
		t.Run(path, func(t *testing.T) {
			d, _, id := startedUnderHeaderTimes(t)
			finish := d.stampedFinish(id, 1_000_000, []byte{0x0f, 0x00})
			nonce := d.next()
			txs := []*types.DevshardTx{divStartTx(nonce, 100, []byte{0x01, 0x00}), finish}
			started := time.Now()
			var err error
			switch path {
			case "ApplyDiff":
				_, err = d.sm.ApplyDiff(testutil.SignDiff(t, d.user, "escrow-1", nonce, txs))
			case "ValidateDiff":
				_, err = d.sm.ValidateDiff(testutil.SignDiff(t, d.user, "escrow-1", nonce, txs))
			case "ApplyLocal":
				_, err = d.sm.ApplyLocal(nonce, txs)
			case "ApplyLocalPersisted":
				_, err = d.sm.ApplyLocalPersisted(nonce, txs)
			}
			require.NoError(t, err)
			require.Less(t, time.Since(started), 200*time.Millisecond)
			if path == "ValidateDiff" {
				require.Equal(t, nonce-1, d.sm.LatestNonce(), "validation trials the diff and leaves it uncommitted")
				rec, ok := d.sm.GetInference(id)
				require.True(t, ok)
				require.Equal(t, types.StatusStarted, rec.Status)
				return
			}
			require.Equal(t, nonce, d.sm.LatestNonce())
			floor, _, _ := d.sm.HeightSyncFloorAsOf(nonce + 1)
			require.EqualValues(t, 1_000_000, floor)
			rec, ok := d.sm.GetInference(id)
			require.True(t, ok)
			require.Equal(t, types.StatusFinished, rec.Status)
		})
	}
}

func TestLargeTimeoutWindowDoesNotOverflowIntoAnImmediateTimeout(t *testing.T) {
	for _, seconds := range []int64{math.MaxInt64, math.MaxInt64/int64(time.Second) + 1} {
		d, _ := newHeaderTimeDriver(t)
		d.sm.state.Config.RefusalTimeout = seconds
		hash := []byte{0x01, 0x00}
		anchor := time.Hour.Milliseconds()
		d.raiseWithTime(100, hash, anchor)
		id := d.next()
		require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor)))
		remaining, err := d.sm.TimeoutRemaining(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
		require.NoError(t, err)
		require.Positive(t, remaining)
		require.ErrorIs(t, d.sm.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED), types.ErrTimeoutNotDue)
	}
	require.EqualValues(t, math.MaxInt64, FinishedClockRequiredSeconds(1, math.MaxInt64))
}

type awaitingHeaderTimes struct {
	*headerTimes
	arrive chan struct{}
}

func (a awaitingHeaderTimes) AwaitHeaderTimeMs(ctx context.Context, height uint64, hash []byte) (int64, error) {
	select {
	case <-a.arrive:
	case <-ctx.Done():
	}
	return a.HeaderTimeMs(height, hash)
}

func TestPersistedReplayDoesNotWaitForTheHeader(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	src.set(100, time.Hour)
	d.raise(100, []byte{0x01, 0x00})
	arrive := make(chan struct{})
	d.sm.headerTimes = awaitingHeaderTimes{headerTimes: src, arrive: arrive}

	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	ack := divAckTx(t, d.hosts[1], d.turn, ref, 1, 300, []byte{0x03, 0x00}, types.SyncState_SYNCED)
	done := make(chan error, 1)
	go func() {
		_, err := d.sm.ApplyLocalPersisted(d.next(), []*types.DevshardTx{ack})
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("replay waited for the header map")
	}
	h, _, _ := d.sm.HeightSyncFloorAsOf(d.next())
	require.Equal(t, uint64(300), h)
}
