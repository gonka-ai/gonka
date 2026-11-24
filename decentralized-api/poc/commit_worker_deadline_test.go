package poc

import (
	"sync"
	"testing"
	"time"

	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// The windows below follow setFinalWindowHeight: the stage starts at 100 and
// the exchange deadline is 250, so 247..249 is the final commit window and a
// commit sent at 249 is the stage's last.

func TestCommitWorker_CommitCarriesArtifactsTheLastPeriodicFlushMissed(t *testing.T) {
	for _, height := range []int64{110, 249} {
		w, r, _ := newFinalWindowWorker(t, height)
		for _, model := range []string{"model-a", "model-b"} {
			store, err := w.store.GetOrCreateStore(100, model)
			require.NoError(t, err)
			// Delivered by the ML node after the periodic flush, not flushed yet.
			require.NoError(t, store.AddWithNode(2, []byte("vec2"), "node-1"))
		}

		w.tick()

		require.NotEmpty(t, r.submissions, "height %d", height)
		for _, sub := range r.submissions {
			for _, entry := range sub.entries {
				require.Equal(t, uint32(2), entry.Count, "height %d model %s", height, entry.ModelId)
			}
		}
	}
}

type lockedCommitRecorder struct {
	cosmosclient.MockCosmosMessageClient
	mu     sync.Mutex
	counts []uint32
}

func (r *lockedCommitRecorder) SubmitPoCV2StoreCommitWithTimeout(msg *types.MsgPoCV2StoreCommit, _ uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range msg.Entries {
		r.counts = append(r.counts, entry.Count)
	}
	return nil
}

func (r *lockedCommitRecorder) SubmitMLNodeWeightDistribution(*types.MsgMLNodeWeightDistribution) error {
	return nil
}

func (r *lockedCommitRecorder) committed() []uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]uint32(nil), r.counts...)
}

// startRunWorker runs the worker loop with a one-hour interval, so only a new
// block height seen by the poll can trigger a commit.
func startRunWorker(t *testing.T, height int64) (*CommitWorker, *lockedCommitRecorder, artifacts.ArtifactStore) {
	t.Helper()
	store := artifacts.NewManagedArtifactStore(t.TempDir(), 5)
	t.Cleanup(func() { store.Close() })
	store.ActivateStage(100)
	a, err := store.GetOrCreateStore(100, "model-a")
	require.NoError(t, err)
	require.NoError(t, a.AddWithNode(1, []byte("vec"), "node-1"))
	require.NoError(t, a.Flush())

	server := &commitWorkerQueryServer{commitCounts: map[string]uint32{}, failModels: map[string]bool{}}
	client, cleanup := newCommitWorkerQueryClient(t, server)
	t.Cleanup(cleanup)
	recorder := &lockedCommitRecorder{}
	recorder.On("NewInferenceQueryClient").Return(client)

	tracker := &chainphase.ChainPhaseTracker{}
	setFinalWindowHeight(tracker, height)
	w := &CommitWorker{
		store:              store,
		recorder:           recorder,
		tracker:            tracker,
		participantAddress: "participant_addr",
		interval:           time.Hour,
		heightPoll:         5 * time.Millisecond,
		stop:               make(chan struct{}),
		done:               make(chan struct{}),
	}
	go w.run()
	t.Cleanup(func() {
		close(w.stop)
		<-w.done
	})
	return w, recorder, a
}

func TestCommitWorker_RunCommitsOnANewBlockWithoutWaitingForTheInterval(t *testing.T) {
	w, recorder, a := startRunWorker(t, 248)
	require.Eventually(t, func() bool { return len(recorder.committed()) == 1 }, 5*time.Second, 5*time.Millisecond,
		"the first block seen in the commit window is committed without waiting for the interval")

	// The ML node delivers more, then the last block before the deadline arrives.
	require.NoError(t, a.AddWithNode(2, []byte("vec2"), "node-1"))
	setFinalWindowHeight(w.tracker, 249)
	require.Eventually(t, func() bool { return len(recorder.committed()) == 2 }, 5*time.Second, 5*time.Millisecond,
		"the block before the exchange deadline is committed as soon as it is seen")
	require.Equal(t, []uint32{1, 2}, recorder.committed())
}

func TestCommitWorker_RunIgnoresAHeightThatGoesBack(t *testing.T) {
	w, recorder, a := startRunWorker(t, 249)
	require.Eventually(t, func() bool { return len(recorder.committed()) == 1 }, 5*time.Second, 5*time.Millisecond)

	// A slower block event updates the tracker after a newer one. 248 is in
	// the final window and the count grew, so a tick there would send a second
	// commit for this model before the deadline block.
	require.NoError(t, a.AddWithNode(2, []byte("vec2"), "node-1"))
	setFinalWindowHeight(w.tracker, 248)
	require.Never(t, func() bool { return len(recorder.committed()) > 1 }, 300*time.Millisecond, 5*time.Millisecond,
		"a lower height is not a new block")
}

func TestCommitWorker_ChallengeCommitCarriesArtifactsTheLastPeriodicFlushMissed(t *testing.T) {
	t.Cleanup(OpenChallenges.Reset)
	addr := "participant_addr"
	OpenChallenges.Replace(addr, []*types.OpenPoCChallenge{openCh(addr, 500, 2000, true, 1)}, 0)

	store := artifacts.NewManagedArtifactStore(t.TempDir(), 5)
	t.Cleanup(func() { store.Close() })
	store.ActivateStage(500)
	a, err := store.GetOrCreateStore(500, "model-a")
	require.NoError(t, err)
	require.NoError(t, a.AddWithNode(1, []byte("vec-1"), "node-1"))
	require.NoError(t, a.Flush())
	// Delivered by the ML node after the periodic flush, not flushed yet.
	require.NoError(t, a.AddWithNode(2, []byte("vec-2"), "node-1"))

	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("SubmitPoCChallengeStoreCommitWithTimeout", mock.MatchedBy(func(msg *types.MsgPoCChallengeStoreCommit) bool {
		return msg != nil && len(msg.Entries) == 1 && msg.Entries[0].Count == 2
	}), uint64(803)).Return(nil).Once()

	tracker := &chainphase.ChainPhaseTracker{}
	tracker.Update(
		chainphase.BlockInfo{Height: 800, Hash: "h"},
		&types.Epoch{Index: 1, PocStartBlockHeight: 100},
		&types.EpochParams{EpochLength: 1000, PocStageDuration: 100, PocExchangeDuration: 50},
		true,
		nil,
	)
	w := &CommitWorker{
		store:                  store,
		recorder:               recorder,
		tracker:                tracker,
		participantAddress:     addr,
		epochModels:            map[string]bool{"model-a": true},
		challengeLastCommitted: make(map[commitKey]commitState),
		challengePending:       make(map[commitKey]pendingCommit),
		lastCommitted:          make(map[commitKey]commitState),
		pending:                make(map[commitKey]pendingCommit),
	}
	w.tick()
	recorder.AssertExpectations(t)
}
