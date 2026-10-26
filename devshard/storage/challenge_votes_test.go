package storage

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// The first store of an entry wins over later ones, the first settle wins
// over a pending entry and over later settles, and a fresh handle on the same
// backend reads what was kept.
func runChallengeVotes(t *testing.T, store Storage) {
	t.Helper()
	require.NoError(t, store.CreateSession(defaultParams()))
	votes, ok := AsChallengeVoteStore(store)
	require.True(t, ok)

	kept, settled, err := votes.LoadOrStoreChallengeVote("escrow-1", 4, []byte(`{"deadline":100}`))
	require.NoError(t, err)
	require.False(t, settled)
	require.JSONEq(t, `{"deadline":100}`, string(kept))

	kept, settled, err = votes.LoadOrStoreChallengeVote("escrow-1", 4, []byte(`{"deadline":999}`))
	require.NoError(t, err)
	require.False(t, settled)
	require.JSONEq(t, `{"deadline":100}`, string(kept), "a later challenge keeps the first deadline")

	kept, err = votes.SettleChallengeVote("escrow-1", 4, []byte(`{"deadline":100,"fault":1}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"deadline":100,"fault":1}`, string(kept))

	kept, err = votes.SettleChallengeVote("escrow-1", 4, []byte(`{"deadline":100,"fault":2}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"deadline":100,"fault":1}`, string(kept), "the first settled verdict stays")

	kept, settled, err = votes.LoadOrStoreChallengeVote("escrow-1", 4, []byte(`{"deadline":5}`))
	require.NoError(t, err)
	require.True(t, settled)
	require.JSONEq(t, `{"deadline":100,"fault":1}`, string(kept))

	kept, err = votes.SettleChallengeVote("escrow-1", 5, []byte(`{"fault":3}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"fault":3}`, string(kept), "a verdict settles without a pending entry")
}

func TestMemory_ChallengeVotes(t *testing.T) {
	runChallengeVotes(t, NewMemory())
}

func TestSQLite_ChallengeVotes(t *testing.T) {
	runChallengeVotes(t, newTestSQLite(t))
}

func TestPostgres_ChallengeVotes(t *testing.T) {
	runChallengeVotes(t, newTestPostgres(t))
}

// devshardd wraps its store in an ObsRepairGate; the vote table behind it is
// the durable one, not a fallback.
func TestObsRepairGate_ChallengeVotes(t *testing.T) {
	runChallengeVotes(t, NewObsRepairGate(NewManagedStorage(newTestSQLite(t), 2, nil)))
}

// Two replicas of one participant share a Postgres: the first settle wins and
// the other handle reads the winner's verdict back.
func TestPostgres_ChallengeVotes_FirstSettleWinsAcrossHandles(t *testing.T) {
	ctx := context.Background()
	a := newTestPostgres(t)
	require.NoError(t, a.CreateSession(defaultParams()))
	b, err := NewPostgres(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	require.NoError(t, b.WaitReady(ctx))
	votesA, ok := AsChallengeVoteStore(a)
	require.True(t, ok)
	votesB, ok := AsChallengeVoteStore(b)
	require.True(t, ok)

	_, _, err = votesA.LoadOrStoreChallengeVote("escrow-1", 4, []byte(`{"deadline":100}`))
	require.NoError(t, err)
	kept, settled, err := votesB.LoadOrStoreChallengeVote("escrow-1", 4, []byte(`{"deadline":999}`))
	require.NoError(t, err)
	require.False(t, settled)
	require.JSONEq(t, `{"deadline":100}`, string(kept), "the sibling reads the first deadline")

	results := make([][]byte, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		results[0], _ = votesA.SettleChallengeVote("escrow-1", 4, []byte(`{"deadline":100,"fault":1}`))
	}()
	go func() {
		defer wg.Done()
		results[1], _ = votesB.SettleChallengeVote("escrow-1", 4, []byte(`{"deadline":100,"fault":2}`))
	}()
	wg.Wait()
	require.JSONEq(t, string(results[0]), string(results[1]), "both replicas hold one verdict")
	kept, settled, err = votesA.LoadOrStoreChallengeVote("escrow-1", 4, []byte(`{"deadline":5}`))
	require.NoError(t, err)
	require.True(t, settled)
	require.JSONEq(t, string(results[0]), string(kept))
}
