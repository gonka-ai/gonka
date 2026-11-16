package bridge

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type heightWarmFetcher struct {
	fakeWarmKeyFetcher
	height int64
}

func (f *heightWarmFetcher) FetchWarmKeyLogWithHeight(ctx context.Context, cold string) ([]WarmKeyEntry, int64, error) {
	entries, err := f.FetchWarmKeyLog(ctx, cold)
	return entries, f.height, err
}

func TestWarmKeyHeightRequiresACompleteChainViewAndRetainsHistoricalEnds(t *testing.T) {
	blockTime := time.Unix(1000, 123)
	entry := WarmKeyEntry{Granter: "cold", Grantee: "warm", ID: 1, GrantedAtHeight: 90}
	fetch := &heightWarmFetcher{fakeWarmKeyFetcher: fakeWarmKeyFetcher{logs: map[string][]WarmKeyEntry{"cold": {entry}}}, height: 100}
	source := NewWarmKeyLogCopy(fetch, time.Hour)
	live, err := CheckWarmKeyAtHeight(source, "cold", "warm", 1, 100, blockTime)
	require.NoError(t, err)
	require.True(t, live)
	// A cached live entry or delivered event does not attest completeness at H+1.
	source.Apply(WarmKeyEntry{Granter: "cold", Grantee: "other", ID: 2, GrantedAtHeight: 101})
	_, err = CheckWarmKeyAtHeight(source, "cold", "warm", 1, 101, blockTime)
	require.ErrorIs(t, err, ErrWarmKeyViewBehind)
	entry.RevokedAtHeight = 110
	entry.End = "WARM_KEY_LOG_END_REVOKED"
	fetch.logs["cold"] = []WarmKeyEntry{entry}
	fetch.height = 115
	live, err = CheckWarmKeyAtHeight(source, "cold", "warm", 1, 105, blockTime)
	require.NoError(t, err)
	require.True(t, live, "later revoke must not erase earlier authorization")
	reads := fetch.fetches
	live, err = CheckWarmKeyAtHeight(source, "cold", "warm", 1, 110, blockTime)
	require.NoError(t, err)
	require.False(t, live)
	live, err = CheckWarmKeyAtHeight(source, "cold", "warm", 1, 105, blockTime)
	require.NoError(t, err)
	require.True(t, live)
	require.Equal(t, reads, fetch.fetches, "slots/keys at an already covered height share the full view")
	live, err = CheckWarmKeyAtHeight(source, "cold", "another", 1, 105, blockTime)
	require.NoError(t, err)
	require.False(t, live)
	live, err = CheckWarmKeyAtHeight(source, "cold", "warm", 1, 89, blockTime)
	require.NoError(t, err)
	require.False(t, live, "a future grant cannot authorize past diffs")
	fetch.height = 0
	_, err = CheckWarmKeyAtHeight(source, "cold", "warm", 1, 120, blockTime)
	require.ErrorIs(t, err, ErrWarmKeyViewBehind, "missing response height is not success")
}

func TestWarmKeyExpirationUsesAuthenticatedNanoseconds(t *testing.T) {
	at := time.Unix(1000, 123)
	grant := WarmKeyEntry{GrantedAtHeight: 90, ExpiresAtUnix: at.UnixNano()}
	require.True(t, grant.LiveAtHeight(100, at))
	require.False(t, grant.LiveAtHeight(100, at.Add(time.Nanosecond)))
	require.True(t, grant.LiveAtHeight(100, at.Add(-time.Nanosecond)))
	grant.RevokedAtHeight = 100
	require.False(t, grant.LiveAtHeight(100, at))
	require.True(t, grant.LiveAtHeight(99, at))
}

func TestGRPCBridgeExposesHeightAwareGrantSource(t *testing.T) {
	entry := WarmKeyEntry{Granter: "cold", Grantee: "warm", ID: 1, GrantedAtHeight: 90}
	fetch := &heightWarmFetcher{fakeWarmKeyFetcher: fakeWarmKeyFetcher{logs: map[string][]WarmKeyEntry{"cold": {entry}}}, height: 100}
	bridge := &GRPCBridge{warmKeys: NewWarmKeyLogCopy(fetch, time.Hour)}
	live, err := CheckWarmKeyAtHeight(bridge, "cold", "warm", 1, 100, time.Unix(1000, 0))
	require.NoError(t, err)
	require.True(t, live)
}

func TestWarmKeyEndEventPreservesHistoricalGrantFields(t *testing.T) {
	at := time.Unix(1000, 123)
	entry := WarmKeyEntry{Granter: "cold", Grantee: "warm", ID: 1, GrantedAtHeight: 100, ExpiresAtUnix: at.Add(time.Hour).UnixNano()}
	fetch := &heightWarmFetcher{fakeWarmKeyFetcher: fakeWarmKeyFetcher{logs: map[string][]WarmKeyEntry{"cold": {entry}}}, height: 100}
	source := NewWarmKeyLogCopy(fetch, time.Hour)
	live, err := CheckWarmKeyAtHeight(source, "cold", "warm", 1, 99, at)
	require.NoError(t, err)
	require.False(t, live)
	// Revoke events carry the end height, not the original grant height or expiration.
	source.Apply(WarmKeyEntry{Granter: "cold", Grantee: "warm", ID: 1, RevokedAtHeight: 101, End: "WARM_KEY_LOG_END_REVOKED"})
	live, err = CheckWarmKeyAtHeight(source, "cold", "warm", 1, 99, at)
	require.NoError(t, err)
	require.False(t, live, "an end event must not authorize a grant before it existed")
	live, err = CheckWarmKeyAtHeight(source, "cold", "warm", 1, 100, at)
	require.NoError(t, err)
	require.True(t, live, "historical validity survives the revoke")
	got, found, err := source.WarmKeyEntry("cold", 1)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, entry.GrantedAtHeight, got.GrantedAtHeight)
	require.Equal(t, entry.ExpiresAtUnix, got.ExpiresAtUnix)
	require.Equal(t, 1, fetch.count(), "a complete cached entry needs no extra query")
}

func TestWarmKeyUnknownEndMustLoadGrantBeforeHistoricalAuthorization(t *testing.T) {
	for _, queryAlreadyEnded := range []bool{false, true} {
		t.Run(fmt.Sprint(queryAlreadyEnded), func(t *testing.T) {
			old := WarmKeyEntry{Granter: "cold", Grantee: "old", ID: 1, GrantedAtHeight: 90}
			fetch := &heightWarmFetcher{fakeWarmKeyFetcher: fakeWarmKeyFetcher{logs: map[string][]WarmKeyEntry{"cold": {old}}}, height: 100}
			source := NewWarmKeyLogCopy(fetch, time.Hour)
			_, _, err := source.WarmKeyEntryAtHeight("cold", 1, 100)
			require.NoError(t, err)
			source.Apply(WarmKeyEntry{Granter: "cold", Grantee: "warm", ID: 2, RevokedAtHeight: 102, End: "WARM_KEY_LOG_END_REVOKED"})
			entry := WarmKeyEntry{Granter: "cold", Grantee: "warm", ID: 2, GrantedAtHeight: 101, ExpiresAtUnix: time.Unix(2000, 0).UnixNano()}
			fetch.height = 101
			if queryAlreadyEnded {
				entry.RevokedAtHeight, entry.End = 102, "WARM_KEY_LOG_END_REVOKED"
				fetch.height = 102
			}
			fetch.set("cold", old, entry)
			live, err := CheckWarmKeyAtHeight(source, "cold", "warm", 2, 99, time.Unix(1000, 0))
			require.NoError(t, err)
			require.False(t, live)
			require.Equal(t, 2, fetch.count(), "a partial tombstone must trigger a complete read even below the cached height")
			live, err = CheckWarmKeyAtHeight(source, "cold", "warm", 2, 101, time.Unix(1000, 0))
			require.NoError(t, err)
			require.True(t, live)
			got, found, err := source.WarmKeyEntry("cold", 2)
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, got.Ended(), "an older query enriches the grant without reviving it")
			require.Equal(t, entry.GrantedAtHeight, got.GrantedAtHeight)
			require.Equal(t, entry.ExpiresAtUnix, got.ExpiresAtUnix)
		})
	}
}
