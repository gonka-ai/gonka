package state

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
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

func newHeaderTimeDriver(t *testing.T) (*floorDriver, *headerTimes) {
	d := newFloorDriver(t)
	src := newHeaderTimes()
	d.sm.headerTimes = src
	return d, src
}

func TestTimeoutCountsBlockTimeNotHeights(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	refusal := time.Duration(d.sm.Config().RefusalTimeout) * time.Second
	src.set(100, time.Hour)
	d.raise(100, []byte{0x01, 0x00})
	id := d.next()
	require.NoError(t, d.start(id, 100, []byte{0x01, 0x00}))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, time.Hour.Milliseconds(), rec.StartedAtTimeMs)

	src.set(100_000, time.Hour+refusal-time.Millisecond)
	d.raise(100_000, []byte{0x02, 0x00})
	remaining, err := d.sm.TimeoutRemaining(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.NoError(t, err)
	require.Equal(t, time.Millisecond, remaining, "a long run of fast blocks is not the timeout")
	require.ErrorIs(t, d.apply(d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)), types.ErrTimeoutNotDue)

	src.set(100_001, time.Hour+refusal)
	d.raise(100_001, []byte{0x02, 0x01})
	require.NoError(t, d.apply(d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)))
	rec, _ = d.sm.GetInference(id)
	require.Equal(t, types.StatusTimedOut, rec.Status)
}

func TestOneSlowBlockCoversTheWholeWindow(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	exec := time.Duration(d.sm.Config().ExecutionTimeout) * time.Second
	hash := []byte{0x01, 0x00}
	src.set(100, time.Hour)
	d.raise(100, hash)
	id := d.next()
	require.NoError(t, d.start(id, 100, hash))
	require.NoError(t, d.apply(d.confirm(id, 100, hash)))
	rec, _ := d.sm.GetInference(id)
	require.Equal(t, time.Hour.Milliseconds(), rec.ConfirmedAtTimeMs)

	src.set(101, time.Hour+exec)
	d.raise(101, []byte{0x01, 0x01})
	require.NoError(t, d.apply(d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)),
		"a halted chain resumes with a block whose time covers the stall")
}

func TestMissingHeaderBlocksTheTimeoutInsteadOfGuessing(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	src.set(100, time.Hour)
	d.raise(100, hash)
	id := d.next()
	require.NoError(t, d.start(id, 100, hash))
	src.set(1_000_000, time.Hour+time.Duration(d.sm.Config().RefusalTimeout)*time.Second)
	d.raise(1_000_000, []byte{0x0f, 0x00})
	src.set(1_000_000, 0)

	before, err := d.sm.ComputeStateRoot()
	require.NoError(t, err)
	require.ErrorIs(t, d.sm.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED), types.ErrHeaderPending)
	require.ErrorIs(t, d.apply(d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)), types.ErrHeaderPending,
		"a floor without a block time is a wait, never a block-count estimate")
	after, err := d.sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, before, after)

	next := d.next()
	require.ErrorIs(t, d.start(next, 1_000_000, []byte{0x0f, 0x00}), types.ErrHeaderPending,
		"a start anchored on a floor without a block time waits too")
}

func TestSettlementWaitsForTheDrainFloorHeader(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	src.set(100, time.Hour)
	d.raise(100, hash)
	id := d.next()
	require.NoError(t, d.start(id, 100, hash))
	src.set(200, 2*time.Hour)
	d.raise(200, []byte{0x02, 0x00})
	src.set(200, 0)

	sealNonce := d.next()
	d.sm.mu.Lock()
	err := d.sm.drainLiveIntoSealedAccLocked(sealNonce)
	_, live := d.sm.state.Inferences[id]
	d.sm.mu.Unlock()
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.True(t, live, "a drain that cannot read the floor time leaves every record live")

	src.set(200, time.Hour+time.Duration(d.sm.Config().RefusalTimeout)*time.Second)
	d.sm.mu.Lock()
	rec := *d.sm.state.Inferences[id]
	due, err := d.sm.liveTimeoutDueAtDrainLocked(&rec, sealNonce)
	d.sm.mu.Unlock()
	require.NoError(t, err)
	require.True(t, due)
}

func TestComposeDropsAFloorRaiseTheChainCannotProve(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	src.set(100, time.Hour)
	d.raise(100, hash)

	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	nonce := d.next()
	fake := divAckTx(t, d.hosts[1], d.turn, ref, 1, 1_000_000, []byte{0x0f, 0x00}, types.SyncState_SYNCED)
	_, applied, err := d.sm.ApplyLocalBestEffort(nonce, []*types.DevshardTx{fake, divHeartbeatTx(0, nil)})
	require.NoError(t, err)
	require.Len(t, applied, 1)
	require.Nil(t, applied[0].GetHeightAck(), "the gateway does not sequence a height its chain cannot prove")
	h, _, _ := d.sm.HeightSyncFloorAsOf(d.next())
	require.Equal(t, uint64(100), h)
}

func TestSealClockReadsConfirmBlockTimes(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	src.set(100, time.Hour)
	d.raise(100, hash)
	id := d.next()
	require.NoError(t, d.start(id, 100, hash))
	src.set(5000, 2*time.Hour)
	d.raise(5000, []byte{0x50, 0x00})
	require.NoError(t, d.apply(d.confirm(id, 100, hash)))

	clock := d.sm.AutoSealStateClock()
	require.True(t, clock.Known)
	require.Equal(t, (2 * time.Hour).Milliseconds(), clock.ClockMs)
}

func TestReplayWaitsForAHeaderInsteadOfFailingRecovery(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0x01, 0x00}
	src.set(100, time.Hour)
	d.raise(100, hash)
	src.set(200, 2*time.Hour)
	d.raise(200, []byte{0x02, 0x00})
	src.set(200, 0)
	nonce := d.next()
	start := divStartTx(nonce, 200, []byte{0x02, 0x00})

	go func() {
		time.Sleep(300 * time.Millisecond)
		src.set(200, 2*time.Hour)
	}()
	attempts := 0
	err := RetryWhileHeaderPending(context.Background(), 5*time.Second, func() error {
		attempts++
		_, err := d.sm.ApplyLocalPersisted(nonce, []*types.DevshardTx{start})
		return err
	})
	require.NoError(t, err)
	require.Greater(t, attempts, 1, "the first replay attempt met a pending header")
	rec, ok := d.sm.GetInference(nonce)
	require.True(t, ok)
	require.Equal(t, (2 * time.Hour).Milliseconds(), rec.StartedAtTimeMs)
}

func TestReplayGivesUpOnAHeaderThatNeverArrives(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	src.set(100, time.Hour)
	d.raise(100, []byte{0x01, 0x00})
	src.set(200, 2*time.Hour)
	d.raise(200, []byte{0x02, 0x00})
	src.set(200, 0)
	nonce := d.next()
	start := divStartTx(nonce, 200, []byte{0x02, 0x00})
	err := RetryWhileHeaderPending(context.Background(), 1500*time.Millisecond, func() error {
		_, err := d.sm.ApplyLocalPersisted(nonce, []*types.DevshardTx{start})
		return err
	})
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.Equal(t, nonce-1, d.sm.LatestNonce(), "a failed attempt leaves no partial apply behind")
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

func TestEvidenceRejectsAFinishWhoseFloorRaiseCannotBeProven(t *testing.T) {
	d, src, id := startedUnderHeaderTimes(t)
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	fake := d.stampedFinish(id, 1_000_000, []byte{0x0f, 0x00})

	require.ErrorIs(t, d.sm.CheckEvidence(&rec, fake), types.ErrHeaderPending,
		"evidence the gateway could never sequence cannot veto a timeout")

	src.set(1_000_000, 2*time.Hour)
	require.NoError(t, d.sm.CheckEvidence(&rec, fake), "once the block is proven the same Finish is evidence")
}

func TestComposeKeepsAFinishWhoseHeaderIsOnlyLate(t *testing.T) {
	d, src, id := startedUnderHeaderTimes(t)
	finish := d.stampedFinish(id, 200, []byte{0x02, 0x00})

	nonce := d.next()
	_, applied, deferred, err := d.sm.ApplyLocalBestEffortDeferred(nonce, []*types.DevshardTx{finish, divHeartbeatTx(0, nil)})
	require.NoError(t, err)
	require.Len(t, applied, 1)
	require.Len(t, deferred, 1, "a Finish waiting on a header is deferred, not discarded")
	require.Equal(t, types.TxHash(finish), types.TxHash(deferred[0]))

	src.set(200, 2*time.Hour)
	_, applied, deferred, err = d.sm.ApplyLocalBestEffortDeferred(d.next(), deferred)
	require.NoError(t, err)
	require.Len(t, applied, 1)
	require.Empty(t, deferred)
	rec, _ := d.sm.GetInference(id)
	require.Equal(t, types.StatusFinished, rec.Status)
}

func TestApplyPathsWaitBeforeInstallingAnUnresolvableFloorRaise(t *testing.T) {
	for _, path := range []string{"ApplyDiff", "ValidateDiff", "ApplyLocal", "ApplyLocalPersisted"} {
		t.Run(path, func(t *testing.T) {
			d, src, id := startedUnderHeaderTimes(t)
			finish := d.stampedFinish(id, 1_000_000, []byte{0x0f, 0x00})
			nonce := d.next()
			txs := []*types.DevshardTx{divStartTx(nonce, 100, []byte{0x01, 0x00}), finish}
			before, err := d.sm.ComputeStateRoot()
			require.NoError(t, err)
			apply := func() error {
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
				return err
			}
			require.ErrorIs(t, apply(), types.ErrHeaderPending)
			after, err := d.sm.ComputeStateRoot()
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Equal(t, nonce-1, d.sm.LatestNonce())
			floor, _, _ := d.sm.HeightSyncFloorAsOf(nonce)
			require.EqualValues(t, 100, floor)
			src.set(1_000_000, 2*time.Hour)
			require.NoError(t, apply())
		})
	}
}

func TestLargeTimeoutWindowDoesNotOverflowIntoAnImmediateTimeout(t *testing.T) {
	for _, seconds := range []int64{math.MaxInt64, math.MaxInt64/int64(time.Second) + 1} {
		d, src := newHeaderTimeDriver(t)
		d.sm.state.Config.RefusalTimeout = seconds
		src.set(100, time.Hour)
		hash := []byte{0x01, 0x00}
		d.raise(100, hash)
		id := d.next()
		require.NoError(t, d.start(id, 100, hash))
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

func TestPersistedReplayAwaitsTheHeaderInOneApply(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	src.set(100, time.Hour)
	d.raise(100, []byte{0x01, 0x00})
	arrive := make(chan struct{})
	d.sm.headerTimes = awaitingHeaderTimes{headerTimes: src, arrive: arrive}

	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	ack := divAckTx(t, d.hosts[1], d.turn, ref, 1, 300, []byte{0x03, 0x00}, types.SyncState_SYNCED)
	go func() {
		time.Sleep(200 * time.Millisecond)
		src.set(300, 2*time.Hour)
		close(arrive)
	}()
	_, err := d.sm.ApplyLocalPersisted(d.next(), []*types.DevshardTx{ack})
	require.NoError(t, err, "replay waits for the raise's header instead of failing the apply")
	h, _, _ := d.sm.HeightSyncFloorAsOf(d.next())
	require.Equal(t, uint64(300), h)
}
