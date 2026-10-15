package poc

import (
	"fmt"
	"math"
	"testing"

	"decentralized-api/cosmosclient"
	"decentralized-api/poc/artifacts"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type challengeFrequencyRecorder struct {
	cosmosclient.MockCosmosMessageClient
	worker      *CommitWorker
	submissions []finalWindowSubmission
	failures    map[string]error
}

func (r *challengeFrequencyRecorder) SubmitPoCChallengeStoreCommitWithTimeout(msg *types.MsgPoCChallengeStoreCommit, timeout uint64) error {
	r.submissions = append(r.submissions, finalWindowSubmission{r.worker.blockHeight, timeout, msg.Entries})
	return r.failures[msg.Entries[0].ModelId]
}

func newChallengeFrequencyWorker(t *testing.T) (*CommitWorker, *challengeFrequencyRecorder, *types.OpenPoCChallenge) {
	t.Helper()
	t.Cleanup(OpenChallenges.Reset)
	store := artifacts.NewManagedArtifactStore(t.TempDir(), 5)
	t.Cleanup(func() { store.Close() })
	store.ActivateStage(500)
	ch := openCh("participant_addr", 500, 2000, true, 1)
	r := &challengeFrequencyRecorder{failures: map[string]error{}}
	w := &CommitWorker{store: store, recorder: r, tracker: commitWorkerTestTracker(800), participantAddress: ch.Target()}
	r.worker = w
	return w, r, ch
}

func growChallengeModel(t *testing.T, w *CommitWorker, model string, count uint32) {
	t.Helper()
	s, err := w.store.GetOrCreateStore(500, model)
	require.NoError(t, err)
	current, _ := s.GetFlushedRoot()
	for n := current + 1; n <= count; n++ {
		require.NoError(t, s.AddWithNode(int32(n), []byte(fmt.Sprint(n)), "node-1"))
	}
	require.NoError(t, s.Flush())
}

func sendChallengeAt(w *CommitWorker, ch *types.OpenPoCChallenge, height int64) {
	OpenChallenges.Replace(ch.Target(), []*types.OpenPoCChallenge{ch}, 0)
	setCommitWorkerHeight(w.tracker, height)
	w.blockHeight = height
	w.maybeSubmitChallengeCommit(w.tracker.GetCurrentEpochState())
}

func TestChallengeCountReady(t *testing.T) {
	for _, tc := range []struct {
		count, confirmed uint32
		ready            bool
	}{
		{0, 0, false}, {1, 0, true}, {100, 100, false}, {99, 100, false},
		{102, 100, false}, {103, 100, false}, {104, 100, true},
		{1030, 1000, false}, {1031, 1000, true}, {34, 33, true},
		{math.MaxUint32, math.MaxUint32, false}, {math.MaxUint32, 4000000000, true},
	} {
		t.Run(fmt.Sprintf("%d_to_%d", tc.confirmed, tc.count), func(t *testing.T) {
			require.Equal(t, tc.ready, challengeCountReady(tc.count, tc.confirmed))
		})
	}
}

func TestChallengeCommitGrowthUsesLatestConfirmedCountPerModel(t *testing.T) {
	w, r, ch := newChallengeFrequencyWorker(t)
	growChallengeModel(t, w, "a", 100)
	growChallengeModel(t, w, "b", 100)
	sendChallengeAt(w, ch, 800)
	require.Len(t, r.submissions, 1)
	require.Len(t, r.submissions[0].entries, 2)
	ch.Commits = []*types.PoCV2StoreCommit{{ModelId: "a", Count: 100}, {ModelId: "b", Count: 100}}
	growChallengeModel(t, w, "a", 103)
	growChallengeModel(t, w, "b", 104)
	sendChallengeAt(w, ch, 801)
	require.Len(t, r.submissions, 2)
	require.Len(t, r.submissions[1].entries, 1)
	require.Equal(t, "b", r.submissions[1].entries[0].ModelId)
	ch.Commits[1] = &types.PoCV2StoreCommit{ModelId: "b", Count: 104}
	growChallengeModel(t, w, "a", 104)
	growChallengeModel(t, w, "b", 107)
	sendChallengeAt(w, ch, 802)
	require.Len(t, r.submissions, 3)
	require.Equal(t, "a", r.submissions[2].entries[0].ModelId)
	growChallengeModel(t, w, "b", 108)
	sendChallengeAt(w, ch, 803)
	require.Len(t, r.submissions, 4)
	require.Equal(t, "b", r.submissions[3].entries[0].ModelId)
}

func TestChallengeCommitLostTransactionRetriesAfterExpiry(t *testing.T) {
	w, r, ch := newChallengeFrequencyWorker(t)
	growChallengeModel(t, w, "a", 104)
	ch.Commits = []*types.PoCV2StoreCommit{{ModelId: "a", Count: 100}}
	sendChallengeAt(w, ch, 800)
	require.Equal(t, uint64(803), r.submissions[0].timeout)
	for h := int64(801); h <= 803; h++ {
		sendChallengeAt(w, ch, h)
	}
	require.Len(t, r.submissions, 1)
	sendChallengeAt(w, ch, 804)
	require.Len(t, r.submissions, 2)
	require.Equal(t, uint32(104), r.submissions[1].entries[0].Count)
}

func TestChallengeCommitFinalWindowRetriesPendingBatchSeparately(t *testing.T) {
	w, r, ch := newChallengeFrequencyWorker(t)
	growChallengeModel(t, w, "a", 104)
	growChallengeModel(t, w, "b", 104)
	ch.Commits = []*types.PoCV2StoreCommit{{ModelId: "a", Count: 100}, {ModelId: "b", Count: 100}}
	sendChallengeAt(w, ch, 1995)
	require.Len(t, r.submissions, 1)
	require.Len(t, r.submissions[0].entries, 2)
	require.Equal(t, uint64(1998), r.submissions[0].timeout)
	// An older count lands while count 104 is still pending.
	ch.Commits[0] = &types.PoCV2StoreCommit{ModelId: "a", Count: 103}
	for h := int64(1996); h <= 1998; h++ {
		sendChallengeAt(w, ch, h)
		require.Len(t, r.submissions, 1+2*int(h-1995))
		for _, sub := range r.submissions[len(r.submissions)-2:] {
			require.Len(t, sub.entries, 1)
			require.Equal(t, uint64(1999), sub.timeout)
			require.Equal(t, h, sub.height)
		}
		require.Equal(t, uint32(103), w.challengeLastCommitted[commitKey{500, "a"}].count)
		require.Equal(t, uint32(104), w.challengePending[commitKey{500, "a"}].state.count)
		n := len(r.submissions)
		sendChallengeAt(w, ch, h)
		require.Len(t, r.submissions, n)
	}
	sendChallengeAt(w, ch, 1999)
	sendChallengeAt(w, ch, 2000)
	require.Len(t, r.submissions, 7)
}

func TestChallengeCommitFinalWindowFlushesSmallGrowth(t *testing.T) {
	w, r, ch := newChallengeFrequencyWorker(t)
	growChallengeModel(t, w, "a", 101)
	ch.Commits = []*types.PoCV2StoreCommit{{ModelId: "a", Count: 100}}
	sendChallengeAt(w, ch, 1995)
	require.Empty(t, r.submissions)
	sendChallengeAt(w, ch, 1996)
	require.Len(t, r.submissions, 1)
	ch.Commits[0] = &types.PoCV2StoreCommit{ModelId: "a", Count: 101}
	sendChallengeAt(w, ch, 1997)
	require.Len(t, r.submissions, 1)
}

func TestChallengeCommitFinalWindowFailureDoesNotBlockOtherModel(t *testing.T) {
	w, r, ch := newChallengeFrequencyWorker(t)
	growChallengeModel(t, w, "a", 1)
	growChallengeModel(t, w, "b", 1)
	r.failures["a"] = fmt.Errorf("broadcast failed")
	sendChallengeAt(w, ch, 1996)
	require.Len(t, r.submissions, 2)
	require.Equal(t, int64(1996), w.challengePending[commitKey{500, "b"}].submittedHeight)
	delete(r.failures, "a")
	growChallengeModel(t, w, "b", 2)
	sendChallengeAt(w, ch, 1996)
	require.Len(t, r.submissions, 3)
	require.Equal(t, "a", r.submissions[2].entries[0].ModelId)
	require.Equal(t, uint32(1), w.challengePending[commitKey{500, "b"}].state.count)
	sendChallengeAt(w, ch, 1997)
	require.Len(t, r.submissions, 5)
	require.Equal(t, uint32(2), w.challengePending[commitKey{500, "b"}].state.count)
}
