package bridge

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWarmKeyCache_NegativeAnswerIsCached(t *testing.T) {
	var c WarmKeyCache
	calls := 0
	fetch := func(string) ([]string, error) { calls++; return nil, nil }

	for i := 0; i < 3; i++ {
		ok, err := c.Verify("warm1", "host", fetch)
		require.NoError(t, err)
		require.False(t, ok)
	}
	require.Equal(t, 1, calls, "a repeated miss costs one query, as before")
}

func TestWarmKeyCache_NegativesAreBounded(t *testing.T) {
	var c WarmKeyCache
	fetch := func(string) ([]string, error) { return []string{"warm1"}, nil }

	for i := 0; i < 3*maxWarmKeyNegatives; i++ {
		ok, err := c.Verify(fmt.Sprintf("stranger-%d", i), "host", fetch)
		require.NoError(t, err)
		require.False(t, ok)
		require.LessOrEqual(t, len(c.denied), maxWarmKeyNegatives)
	}
	ok, err := c.Verify("warm1", "host", fetch)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, c.granted, 1, "only the real grant is remembered")
}

func TestWarmKeyCache_EvictedNegativeIsQueriedAgain(t *testing.T) {
	var c WarmKeyCache
	grantees := []string{}
	fetch := func(string) ([]string, error) { return grantees, nil }

	ok, _ := c.Verify("warm2", "host", fetch)
	require.False(t, ok)
	for i := 0; i < maxWarmKeyNegatives; i++ {
		_, _ = c.Verify(fmt.Sprintf("stranger-%d", i), "host", fetch)
	}
	grantees = []string{"warm2"}
	ok, _ = c.Verify("warm2", "host", fetch)
	require.True(t, ok, "an evicted negative is re-queried, so it can only get fresher")
}

func TestWarmKeyCache_PositiveAnswerIsSticky(t *testing.T) {
	var c WarmKeyCache
	grantees := []string{"warm1"}
	calls := 0
	fetch := func(string) ([]string, error) { calls++; return grantees, nil }

	ok, _ := c.Verify("warm1", "host", fetch)
	require.True(t, ok)
	grantees = nil
	for i := 0; i < 2*maxWarmKeyNegatives; i++ {
		_, _ = c.Verify(fmt.Sprintf("stranger-%d", i), "host", fetch)
	}
	calls = 0
	ok, _ = c.Verify("warm1", "host", fetch)
	require.True(t, ok, "a verified binding is remembered for the process lifetime, as before")
	require.Equal(t, 0, calls)
}

func TestWarmKeyCache_FetchErrorIsNotCached(t *testing.T) {
	var c WarmKeyCache
	fail := true
	fetch := func(string) ([]string, error) {
		if fail {
			return nil, errors.New("node down")
		}
		return []string{"warm1"}, nil
	}
	_, err := c.Verify("warm1", "host", fetch)
	require.Error(t, err)
	fail = false
	ok, err := c.Verify("warm1", "host", fetch)
	require.NoError(t, err)
	require.True(t, ok)
}
