package state

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/types"
)

// fetchCountingTimes tracks HeaderTimeMs calls. StoredAt stays empty so a
// buggy TimeoutDue path that fetches under lock is visible.
type fetchCountingTimes struct {
	headerCalls atomic.Int32
}

func (f *fetchCountingTimes) HeaderTimeMs(height uint64, _ []byte) (int64, error) {
	f.headerCalls.Add(1)
	return 0, fmt.Errorf("%w: height %d", types.ErrHeaderPending, height)
}

func (f *fetchCountingTimes) StoredAt(uint64) (headertime.Record, bool) {
	return headertime.Record{}, false
}

func TestTimeoutDue_DoesNotFetchUnderLockWhenFloorTimeMissing(t *testing.T) {
	spy := &fetchCountingTimes{}
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(spy)

	hash := []byte{0xb1}
	anchor := time.Hour
	d.raiseWithTime(100, hash, anchor.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
	d.raiseWithTime(200, []byte{0xb2}, 0)

	require.Zero(t, spy.headerCalls.Load())
	err := d.sm.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.ErrorIs(t, err, types.ErrFloorTimeMissing)
	require.NotErrorIs(t, err, types.ErrHeaderPending)
	require.Zero(t, spy.headerCalls.Load(), "TimeoutDue must not call HeaderTimeMs under sm.mu")
}

func TestTimeoutDue_UsesStoredMapWithoutFetch(t *testing.T) {
	d, src := newHeaderTimeDriver(t)
	hash := []byte{0xb3}
	anchor := time.Hour
	d.raiseWithTime(100, hash, anchor.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
	d.raiseWithTime(200, []byte{0xb4}, 0)

	dueMs := (anchor + time.Duration(d.sm.Config().RefusalTimeout)*time.Second).Milliseconds()
	src.set(200, time.Duration(dueMs)*time.Millisecond)

	err := d.sm.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.ErrorIs(t, err, types.ErrFloorTimeMissing, "a map row does not supply time(F)")
}

func TestTimeoutDue_StampAnchorStoredAtOnly(t *testing.T) {
	spy := &fetchCountingTimes{}
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(spy)

	hash := []byte{0xb5}
	// Floor has time so nowMs resolves; start carried time is 0 so anchor
	// would fall back to stampTimeMsLocked.
	d.raiseWithTime(100, hash, time.Hour.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(divStartTx(id, 100, hash)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Zero(t, rec.StartedAtTimeMs)

	require.Zero(t, spy.headerCalls.Load())
	_, err := d.sm.TimeoutRemaining(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.ErrorIs(t, err, types.ErrHeaderPending)
	require.Zero(t, spy.headerCalls.Load(), "stampTimeMsLocked must not HeaderTimeMs under lock")
}

func TestLookupHeaderTimeMs_StoredAtOnly(t *testing.T) {
	spy := &fetchCountingTimes{}
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(spy)

	_, ok := d.sm.LookupHeaderTimeMs(100, []byte{0xb6})
	require.False(t, ok)
	require.Zero(t, spy.headerCalls.Load(), "LookupHeaderTimeMs must not call HeaderTimeMs")

	src := headertime.New()
	d.sm.SetHeaderTimes(src)
	require.NoError(t, src.Seed(100, []byte{0xb6}, time.Hour.Milliseconds()))
	ms, ok := d.sm.LookupHeaderTimeMs(100, []byte{0xb6})
	require.True(t, ok)
	require.Equal(t, time.Hour.Milliseconds(), ms)
	_, ok = d.sm.LookupHeaderTimeMs(100, []byte{0xb7})
	require.False(t, ok, "hash mismatch is a miss")
}
