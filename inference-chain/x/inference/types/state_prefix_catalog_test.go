package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStatePrefixCatalogNoDuplicates(t *testing.T) {
	catalog := StatePrefixCatalog()
	require.NotEmpty(t, catalog)

	names := map[string]bool{}
	prefixes := map[string]bool{}
	for _, p := range catalog {
		require.NotEmpty(t, p.Name)
		require.NotEmpty(t, p.Bytes, "prefix %q must have bytes", p.Name)

		require.Falsef(t, names[p.Name], "duplicate prefix name %q", p.Name)
		names[p.Name] = true

		key := string(p.Bytes)
		require.Falsef(t, prefixes[key], "duplicate prefix bytes for %q", p.Name)
		prefixes[key] = true
	}
}

func TestStatePrefixCatalogSortedLongestFirst(t *testing.T) {
	catalog := StatePrefixCatalog()
	for i := 1; i < len(catalog); i++ {
		require.GreaterOrEqualf(t, len(catalog[i-1].Bytes), len(catalog[i].Bytes),
			"catalog must be sorted longest-prefix-first for MatchStatePrefix to work (index %d)", i)
	}
}

func TestMatchStatePrefixLongestWins(t *testing.T) {
	catalog := StatePrefixCatalog()

	// A "TrainingTask/sync/..." key must match the specific sync prefix, not a
	// shorter "TrainingTask/..." prefix.
	syncKey := []byte("TrainingTask/sync/1/store/key/value")
	match := MatchStatePrefix(catalog, syncKey)
	require.NotNil(t, match)
	require.Equal(t, "TrainingTaskSync", match.Name)
	require.True(t, match.Legacy)

	// A single-byte collection prefix resolves to its named bucket.
	participantKey := append([]byte(ParticipantsPrefix), []byte("addr")...)
	match = MatchStatePrefix(catalog, participantKey)
	require.NotNil(t, match)
	require.Equal(t, "Participants", match.Name)
	require.False(t, match.Legacy)

	// Params raw key.
	match = MatchStatePrefix(catalog, ParamsKey)
	require.NotNil(t, match)
	require.Equal(t, "Params", match.Name)

	// Developer-stats indexes (the dominant live state on mainnet) must be
	// attributed to their own buckets rather than left unmatched.
	match = MatchStatePrefix(catalog, append([]byte("stats/developers/inference"), []byte("inf-id")...))
	require.NotNil(t, match)
	require.Equal(t, "DeveloperStatsByInference", match.Name)

	match = MatchStatePrefix(catalog, append([]byte("stats/model/inference"), []byte("k")...))
	require.NotNil(t, match)
	require.Equal(t, "DeveloperStatsByModel", match.Name)

	// An unknown leading byte returns no match.
	require.Nil(t, MatchStatePrefix(catalog, []byte{0xff, 0x00}))
}
