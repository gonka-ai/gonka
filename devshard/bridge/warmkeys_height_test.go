package bridge

import (
	"context"
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
