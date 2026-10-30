package bridge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeWarmKeyFetcher is a chain whose log the test edits directly.
type fakeWarmKeyFetcher struct {
	mu        sync.Mutex
	logs      map[string][]WarmKeyEntry
	byGranter map[string]int
	err       error
	fetches   int
}

func (f *fakeWarmKeyFetcher) FetchWarmKeyLog(_ context.Context, granter string) ([]WarmKeyEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if f.byGranter == nil {
		f.byGranter = make(map[string]int)
	}
	f.byGranter[granter]++
	if f.err != nil {
		return nil, f.err
	}
	return append([]WarmKeyEntry(nil), f.logs[granter]...), nil
}

func (f *fakeWarmKeyFetcher) FetchWarmKeyEntry(_ context.Context, granter string, id uint64) (WarmKeyEntry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if f.err != nil {
		return WarmKeyEntry{}, false, f.err
	}
	for _, e := range f.logs[granter] {
		if e.ID == id {
			return e, true, nil
		}
	}
	return WarmKeyEntry{}, false, nil
}

func (f *fakeWarmKeyFetcher) set(granter string, entries ...WarmKeyEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logs == nil {
		f.logs = make(map[string][]WarmKeyEntry)
	}
	f.logs[granter] = entries
}

func (f *fakeWarmKeyFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches
}

func (f *fakeWarmKeyFetcher) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeWarmKeyFetcher) granterFetches(granter string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byGranter[granter]
}

// Test flow:
//  1. Load a granter with one live and one ended entry, then ask twice.
//  2. Assert the first ask loads from the chain, the second answers from the copy, live entries exclude the
//     ended one and an expired one, and the entry lookup finds both without another fetch.
func TestWarmKeyLogCopy_LoadsOnceAndAnswersFromTheCopy(t *testing.T) {
	chain := &fakeWarmKeyFetcher{}
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "old", RevokedAtHeight: 50, End: "WARM_KEY_LOG_END_REVOKED"},
		WarmKeyEntry{Granter: "cold", ID: 2, Grantee: "warm"},
		WarmKeyEntry{Granter: "cold", ID: 3, Grantee: "lapsed", ExpiresAtUnix: 1})
	cp := NewWarmKeyLogCopy(chain, time.Hour)

	live, err := cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 1)
	require.Equal(t, "warm", live[0].Grantee)
	require.Equal(t, 1, chain.count())

	live, err = cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 1)
	require.Equal(t, 1, chain.count(), "a fresh copy is not reloaded")

	ended, found, err := cp.WarmKeyEntry("cold", 1)
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, ended.Ended())
	require.Equal(t, 1, chain.count())

	ok, err := IsWarmKeyLive(cp, "cold", "warm", time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = IsWarmKeyLive(cp, "cold", "old", time.Now())
	require.NoError(t, err)
	require.False(t, ok)
}

// Test flow:
//  1. Ask for an entry the copy does not have while the chain has it, then for one the chain has not either.
//  2. Assert the first is fetched directly and stored, and the second reports not found after the chain says so.
func TestWarmKeyLogCopy_EntryFallsBackToTheChain(t *testing.T) {
	chain := &fakeWarmKeyFetcher{}
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"})
	cp := NewWarmKeyLogCopy(chain, time.Hour)
	now := time.Unix(1000, 0)
	cp.now = func() time.Time { return now }

	e, found, err := cp.WarmKeyEntry("cold", 1)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "warm", e.Grantee)
	require.Equal(t, 1, chain.count())

	_, found, err = cp.WarmKeyEntry("cold", 1)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, 1, chain.count(), "stored after the direct fetch")

	require.NoError(t, ConfirmWarmKeyBind(cp, "cold", "warm", 1))
	require.ErrorIs(t, ConfirmWarmKeyBind(cp, "cold", "other", 1), ErrWarmKeyEntryMismatch)
}

// Test flow:
//  1. Apply a grant event for a granter the copy never loaded, then ask for its live keys; apply an end event and
//     a stale grant event for the same id.
//  2. Assert the first ask still loads the whole log (an event alone leaves holes), the end is applied, and the
//     stale grant does not revive the ended entry.
func TestWarmKeyLogCopy_EventsAppendAndEndsAreFinal(t *testing.T) {
	chain := &fakeWarmKeyFetcher{}
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"})
	cp := NewWarmKeyLogCopy(chain, time.Hour)

	cp.Apply(WarmKeyEntry{Granter: "cold", ID: 2, Grantee: "second"})
	live, err := cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 2, "the event and the loaded log together")
	require.Equal(t, 1, chain.count())

	cp.Apply(WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm", RevokedAtHeight: 90, End: "WARM_KEY_LOG_END_REVOKED"})
	live, err = cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 1)
	require.Equal(t, uint64(2), live[0].ID)

	cp.Apply(WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"})
	live, err = cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 1, "a replayed grant event does not revive an ended entry")
}

// Test flow:
//  1. Load a granter, make the copy stale, ask again; then Reset and ask once more with the chain changed.
//  2. Assert the stale ask answers from the copy and reloads in the background, and a reset answers from the
//     copy too and reloads in the background, so a grant the events missed is seen next.
func TestWarmKeyLogCopy_RefreshAndReset(t *testing.T) {
	chain := &fakeWarmKeyFetcher{}
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"})
	cp := NewWarmKeyLogCopy(chain, time.Minute)
	now := time.Unix(1000, 0)
	cp.now = func() time.Time { return now }

	live, err := cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 1)

	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm", RevokedAtHeight: 70, End: "WARM_KEY_LOG_END_REVOKED"})
	now = now.Add(2 * time.Minute)
	live, err = cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 1, "a stale copy answers at once")
	require.Eventually(t, func() bool {
		live, err := cp.LiveWarmKeys("cold")
		return err == nil && len(live) == 0
	}, 5*time.Second, 10*time.Millisecond, "the background reload brings the end in")

	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm", RevokedAtHeight: 70, End: "WARM_KEY_LOG_END_REVOKED"},
		WarmKeyEntry{Granter: "cold", ID: 2, Grantee: "next"})
	cp.Reset()
	live, err = cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Empty(t, live, "a reset answers from the copy")
	require.Eventually(t, func() bool {
		live, err := cp.LiveWarmKeys("cold")
		return err == nil && len(live) == 1 && live[0].Grantee == "next"
	}, 5*time.Second, 10*time.Millisecond, "the background reload brings the grant in")
}

// Test flow:
//  1. Ask for a granter the copy never loaded while the chain is down.
//  2. Assert the error reaches the caller and nothing is cached as empty: the next ask with the chain back loads.
func TestWarmKeyLogCopy_ChainErrorIsNotCachedAsEmpty(t *testing.T) {
	chain := &fakeWarmKeyFetcher{err: errors.New("down")}
	cp := NewWarmKeyLogCopy(chain, time.Hour)

	_, err := cp.LiveWarmKeys("cold")
	require.Error(t, err)
	_, _, err = cp.WarmKeyEntry("cold", 1)
	require.Error(t, err)

	chain.mu.Lock()
	chain.err = nil
	chain.mu.Unlock()
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"})
	live, err := cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 1)
}

// Test flow:
//  1. Load a granter, add a grant on the chain the copy has not seen (no events), and ask for that key.
//  2. Assert the first ask misses and schedules a reload, so the next ask finds the key; a miss on a copy
//     reloaded a moment ago schedules nothing.
func TestWarmKeyLogCopy_MissSchedulesAReload(t *testing.T) {
	chain := &fakeWarmKeyFetcher{}
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"})
	cp := NewWarmKeyLogCopy(chain, time.Hour)
	now := time.Unix(1000, 0)
	cp.now = func() time.Time { return now }

	ok, err := IsWarmKeyLive(cp, "cold", "warm", now)
	require.NoError(t, err)
	require.True(t, ok)
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"}, WarmKeyEntry{Granter: "cold", ID: 2, Grantee: "next"})

	for i := 0; i < 3; i++ {
		ok, err = IsWarmKeyLive(cp, "cold", "next", now)
		require.NoError(t, err)
		require.False(t, ok, "a miss right after the load does not reload")
	}
	require.Equal(t, 1, chain.count())

	now = now.Add(10 * time.Second)
	ok, err = IsWarmKeyLive(cp, "cold", "next", now)
	require.NoError(t, err)
	require.False(t, ok, "the miss answers from the copy and reloads in the background")
	require.Eventually(t, func() bool {
		ok, err := IsWarmKeyLive(cp, "cold", "next", now)
		return err == nil && ok
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, 2, chain.count())
}

// Test flow:
//  1. Load a granter, take the chain down, and send misses at request rate for twelve seconds.
//  2. Assert the misses start at most one reload attempt per five seconds although none succeeds.
func TestWarmKeyLogCopy_MissReloadsAreThrottledWhileTheChainFails(t *testing.T) {
	chain := &fakeWarmKeyFetcher{}
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"})
	cp := NewWarmKeyLogCopy(chain, time.Hour)
	now := time.Unix(1000, 0)
	cp.now = func() time.Time { return now }
	_, err := cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	chain.mu.Lock()
	chain.err = errors.New("down")
	chain.mu.Unlock()

	for second := 1; second <= 12; second++ {
		now = now.Add(time.Second)
		before := chain.count()
		for i := 0; i < 5; i++ {
			ok, err := IsWarmKeyLive(cp, "cold", "stranger", now)
			require.NoError(t, err)
			require.False(t, ok)
		}
		// The load counts as an attempt, so a reload is due six seconds
		// after it and every six seconds from then on. The clock advances
		// only once the reload has finished, or the next one would join it.
		if second == 6 || second == 12 {
			require.Eventually(t, func() bool { return chain.count() == before+1 && cp.idle() }, time.Second, time.Millisecond, "second %d", second)
		} else {
			require.Equal(t, before, chain.count(), "second %d", second)
		}
	}
	require.Equal(t, 1+2, chain.count(), "one load, then one attempt per five seconds")
}

// Test flow:
//  1. Load a granter with a one-second refresh interval, take the chain down, and look the granter up every
//     second for twelve seconds.
//  2. Assert the stale copy keeps answering and starts at most one reload attempt per five seconds.
func TestWarmKeyLogCopy_StaleRefreshesAreThrottledWhileTheChainFails(t *testing.T) {
	chain := &fakeWarmKeyFetcher{}
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"})
	cp := NewWarmKeyLogCopy(chain, time.Second)
	now := time.Unix(1000, 0)
	cp.now = func() time.Time { return now }
	live, err := cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 1)
	chain.mu.Lock()
	chain.err = errors.New("down")
	chain.mu.Unlock()

	for second := 1; second <= 12; second++ {
		now = now.Add(time.Second)
		before := chain.count()
		for i := 0; i < 3; i++ {
			live, err := cp.LiveWarmKeys("cold")
			require.NoError(t, err, "second %d", second)
			require.Len(t, live, 1, "the stale copy keeps answering")
		}
		if second == 6 || second == 12 {
			require.Eventually(t, func() bool { return chain.count() == before+1 && cp.idle() }, time.Second, time.Millisecond, "second %d", second)
		} else {
			require.Equal(t, before, chain.count(), "second %d", second)
		}
	}
	require.Equal(t, 1+2, chain.count())
}

// idle reports that no reload is in flight.
func (c *WarmKeyLogCopy) idle() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.loading) == 0
}

// Test flow:
//  1. Apply grants for two granters and reload, then fail the chain and reload again, then reset and reload while the chain is down.
//  2. Assert each reload reads once per known granter and never a stranger, and a failed read keeps the stored grant, also after reset.
func TestWarmKeyLogCopy_ReloadKnownOncePerGranter(t *testing.T) {
	chain := &fakeWarmKeyFetcher{}
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	chain.set("other", WarmKeyEntry{Granter: "other", ID: 2, Grantee: "second", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	cp := NewWarmKeyLogCopy(chain, time.Hour)
	cp.Apply(WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	cp.Apply(WarmKeyEntry{Granter: "other", ID: 2, Grantee: "second", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	require.Equal(t, []string{"cold", "other"}, cp.KnownGranters())

	before := chain.count()
	cp.ReloadKnown()
	require.Equal(t, before+2, chain.count())
	require.Equal(t, 1, chain.granterFetches("cold"))
	require.Equal(t, 1, chain.granterFetches("other"))
	require.Zero(t, chain.granterFetches("stranger"))

	chain.setErr(errors.New("down"))
	cp.ReloadKnown()
	require.Equal(t, before+4, chain.count())
	live, err := cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 1)
	require.Equal(t, "warm", live[0].Grantee)
	ok, err := IsWarmKeyLive(cp, "cold", "warm", time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, before+4, chain.count(), "LiveWarmKeys and IsWarmKeyLive do not start another read while the copy is complete")

	cp.Reset()
	cp.ReloadKnown()
	live, err = cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 1, "a granter loaded before answers from its entries after reset")
	cached, found, err := cp.WarmKeyEntry("cold", 1)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "warm", cached.Grantee)
	require.Zero(t, chain.granterFetches("stranger"))
}

// Test flow:
//  1. Load a grant whose expiration is already past, and one the log has ended.
//  2. Assert the consensus check accepts the expired grant and rejects the ended one, while the live check rejects both.
func TestWarmKeyLogCopy_UnendedGrantIgnoresExpiration(t *testing.T) {
	chain := &fakeWarmKeyFetcher{}
	past := time.Unix(1_000, 0)
	chain.set("cold",
		WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm", ExpiresAtUnix: past.Unix()},
		WarmKeyEntry{Granter: "cold", ID: 2, Grantee: "old", RevokedAtHeight: 9, End: "WARM_KEY_LOG_END_REVOKED"},
	)
	cp := NewWarmKeyLogCopy(chain, time.Hour)
	cp.now = func() time.Time { return past.Add(time.Hour) }

	ok, err := cp.HasUnendedGrant("cold", "warm")
	require.NoError(t, err)
	require.True(t, ok)
	live, err := IsWarmKeyLive(cp, "cold", "warm", cp.now())
	require.NoError(t, err)
	require.False(t, live)

	ok, err = cp.HasUnendedGrant("cold", "old")
	require.NoError(t, err)
	require.False(t, ok)
}

// Test flow:
//  1. Apply a grant, then the revoke event, which carries only the end.
//  2. Assert the stored entry keeps the grant height and the expiration.
func TestWarmKeyLogCopy_RevokeKeepsGrantFields(t *testing.T) {
	cp := NewWarmKeyLogCopy(nil, time.Hour)
	cp.Apply(WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm", GrantedAtHeight: 10, ExpiresAtUnix: 5000})
	cp.Apply(WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm", RevokedAtHeight: 20, End: "WARM_KEY_LOG_END_REVOKED"})

	got, found, err := cp.WarmKeyEntry("cold", 1)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(10), got.GrantedAtHeight)
	require.Equal(t, int64(5000), got.ExpiresAtUnix)
	require.Equal(t, int64(20), got.RevokedAtHeight)
	require.Equal(t, "WARM_KEY_LOG_END_REVOKED", got.End)
	require.True(t, got.Ended())
}

// heldWarmKeyFetcher returns the snapshot it read on entry, after release.
// A caller can Reset and change the chain while the first fetch is in flight.
type heldWarmKeyFetcher struct {
	inner   *fakeWarmKeyFetcher
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *heldWarmKeyFetcher) FetchWarmKeyLog(ctx context.Context, granter string) ([]WarmKeyEntry, error) {
	snap, err := f.inner.FetchWarmKeyLog(ctx, granter)
	f.once.Do(func() { close(f.entered) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.release:
		return snap, err
	}
}

func (f *heldWarmKeyFetcher) FetchWarmKeyEntry(ctx context.Context, granter string, id uint64) (WarmKeyEntry, bool, error) {
	return f.inner.FetchWarmKeyEntry(ctx, granter, id)
}

// Test flow:
//  1. Start a load, reset the copy while it is in flight, and return the pre-reset snapshot.
//  2. Assert that load does not restore completeness, and the next read fetches the revoked entry.
func TestWarmKeyLogCopy_ResetDropsInFlightLoad(t *testing.T) {
	inner := &fakeWarmKeyFetcher{}
	inner.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"})
	fetch := &heldWarmKeyFetcher{
		inner:   inner,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	cp := NewWarmKeyLogCopy(fetch, 10*time.Minute)
	retries := &retryRecorder{}
	cp.after = retries.after

	first := make(chan error, 1)
	go func() {
		_, err := cp.LiveWarmKeys("cold")
		first <- err
	}()
	<-fetch.entered
	cp.Reset()
	inner.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm", RevokedAtHeight: 20, End: "WARM_KEY_LOG_END_REVOKED"})
	close(fetch.release)

	require.Error(t, <-first)
	require.Equal(t, 1, retries.pending(), "the dropped load schedules a retry")
	live, err := cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Empty(t, live)
	require.Equal(t, 2, inner.count())
}

// retryRecorder holds the retries a copy schedules until the test fires them.
type retryRecorder struct {
	mu     sync.Mutex
	delays []time.Duration
	queued []func()
}

func (r *retryRecorder) after(d time.Duration, f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delays = append(r.delays, d)
	r.queued = append(r.queued, f)
}

func (r *retryRecorder) pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.queued)
}

func (r *retryRecorder) lastDelay() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.delays) == 0 {
		return 0
	}
	return r.delays[len(r.delays)-1]
}

// fire runs the oldest scheduled retry on the caller's goroutine.
func (r *retryRecorder) fire(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	require.NotEmpty(t, r.queued, "no retry scheduled")
	f := r.queued[0]
	r.queued = r.queued[1:]
	r.mu.Unlock()
	f()
}

// Test flow:
//  1. Load a granter, then Reset with the chain down, and ask about its grant and about a granter never loaded.
//  2. Assert the loaded granter answers from its entries while the reload fails in the background and schedules
//     a retry, and the granter never loaded still returns the chain error.
func TestWarmKeyLogCopy_ResetAnswersFromHeldEntries(t *testing.T) {
	chain := &fakeWarmKeyFetcher{}
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"})
	cp := NewWarmKeyLogCopy(chain, time.Hour)
	retries := &retryRecorder{}
	cp.after = retries.after

	ok, err := cp.HasUnendedGrant("cold", "warm")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 1, chain.count())

	chain.setErr(errors.New("down"))
	cp.Reset()
	ok, err = cp.HasUnendedGrant("cold", "warm")
	require.NoError(t, err)
	require.True(t, ok, "the held grant answers while the chain is down")
	require.Eventually(t, func() bool { return chain.count() == 2 && cp.idle() }, time.Second, time.Millisecond,
		"the reset starts one background reload")
	require.Equal(t, 1, retries.pending())
	require.Equal(t, warmKeyRetryMin, retries.lastDelay())

	ok, err = cp.HasUnendedGrant("cold", "warm")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 2, chain.count(), "a read right after the attempt does not start another")

	_, err = cp.HasUnendedGrant("fresh", "warm")
	require.Error(t, err, "a granter never loaded waits on the chain read")
	require.Equal(t, 2, retries.pending(), "its failed read schedules its own retry")
}

// Test flow:
//  1. Fail the first read of a granter, fire its retry while the chain is still down, ask again, then fire the
//     next retry with the chain back.
//  2. Assert one retry is pending at a time, its delay doubles, the successful retry completes the granter, and
//     the next read answers from the copy without a fetch.
func TestWarmKeyLogCopy_FailedReadRetriesInBackground(t *testing.T) {
	chain := &fakeWarmKeyFetcher{err: errors.New("down")}
	cp := NewWarmKeyLogCopy(chain, time.Hour)
	retries := &retryRecorder{}
	cp.after = retries.after

	_, err := cp.LiveWarmKeys("cold")
	require.Error(t, err)
	require.Equal(t, 1, retries.pending())
	require.Equal(t, warmKeyRetryMin, retries.lastDelay())

	retries.fire(t)
	require.Equal(t, 2, chain.count())
	require.Equal(t, 1, retries.pending())
	require.Equal(t, 2*warmKeyRetryMin, retries.lastDelay())

	_, err = cp.LiveWarmKeys("cold")
	require.Error(t, err)
	require.Equal(t, 1, retries.pending(), "a granter has one scheduled retry at a time")

	chain.setErr(nil)
	chain.set("cold", WarmKeyEntry{Granter: "cold", ID: 1, Grantee: "warm"})
	retries.fire(t)
	require.Zero(t, retries.pending())
	before := chain.count()
	live, err := cp.LiveWarmKeys("cold")
	require.NoError(t, err)
	require.Len(t, live, 1)
	require.Equal(t, before, chain.count(), "the retry completed the granter")
}

func TestNextWarmKeyRetryDoublesToTheCap(t *testing.T) {
	var got []time.Duration
	d := time.Duration(0)
	for i := 0; i < 6; i++ {
		d = nextWarmKeyRetry(d)
		got = append(got, d)
	}
	require.Equal(t, []time.Duration{
		5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute,
	}, got)
}
