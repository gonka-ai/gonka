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
	mu      sync.Mutex
	logs    map[string][]WarmKeyEntry
	err     error
	fetches int
}

func (f *fakeWarmKeyFetcher) FetchWarmKeyLog(_ context.Context, granter string) ([]WarmKeyEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
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
//  2. Assert the stale ask answers from the copy and reloads in the background, and a reset reloads before
//     answering, so a revoke the events missed is seen.
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
	require.Len(t, live, 1)
	require.Equal(t, "next", live[0].Grantee, "a reset reloads before answering")
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

	ok, err = IsWarmKeyLive(cp, "cold", "next", now)
	require.NoError(t, err)
	require.False(t, ok, "a miss right after the load does not reload")
	require.Equal(t, 1, chain.count())

	now = now.Add(10 * time.Second)
	ok, err = IsWarmKeyLive(cp, "cold", "next", now)
	require.NoError(t, err)
	require.False(t, ok, "the miss answers from the copy and reloads in the background")
	require.Eventually(t, func() bool {
		ok, err := IsWarmKeyLive(cp, "cold", "next", now)
		return err == nil && ok
	}, 5*time.Second, 10*time.Millisecond)
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
		// The first miss after the load is due at once (no attempt yet),
		// the next ones every six seconds.
		if second == 1 || second == 7 {
			require.Eventually(t, func() bool { return chain.count() == before+1 }, time.Second, time.Millisecond, "second %d", second)
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
		if second == 2 || second == 8 {
			require.Eventually(t, func() bool { return chain.count() == before+1 }, time.Second, time.Millisecond, "second %d", second)
		} else {
			require.Equal(t, before, chain.count(), "second %d", second)
		}
	}
	require.Equal(t, 1+2, chain.count())
}
