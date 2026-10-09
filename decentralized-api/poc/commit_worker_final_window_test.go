package poc

import (
	"context"
	"fmt"
	"testing"

	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/cosmosclient/tx_manager"
	"decentralized-api/poc/artifacts"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

type finalWindowSubmission struct {
	height  int64
	timeout uint64
	entries []*types.PoCV2CommitEntry
}

type finalWindowRecorder struct {
	cosmosclient.MockCosmosMessageClient
	worker      *CommitWorker
	submissions []finalWindowSubmission
	failures    map[string]error
	refreshes   int
	prev        map[string]uint32
}

func (r *finalWindowRecorder) SubmitPoCV2StoreCommitWithTimeout(msg *types.MsgPoCV2StoreCommit, timeout uint64) error {
	r.submissions = append(r.submissions, finalWindowSubmission{r.worker.blockHeight, timeout, msg.Entries})
	return r.failures[msg.Entries[0].ModelId]
}
func (r *finalWindowRecorder) SubmitMLNodeWeightDistribution(*types.MsgMLNodeWeightDistribution) error {
	return nil
}

func (r *finalWindowRecorder) RefreshFeeTree(context.Context) error      { r.refreshes++; return nil }
func (r *finalWindowRecorder) SetStoreCommitPrev(prev map[string]uint32) { r.prev = prev }

func newFinalWindowWorker(t *testing.T, height int64) (*CommitWorker, *finalWindowRecorder, *commitWorkerQueryServer) {
	t.Helper()
	store := artifacts.NewManagedArtifactStore(t.TempDir(), 5)
	t.Cleanup(func() { store.Close() })
	store.ActivateStage(100)
	for _, model := range []string{"model-a", "model-b"} {
		a, err := store.GetOrCreateStore(100, model)
		require.NoError(t, err)
		require.NoError(t, a.AddWithNode(1, []byte("vec"), "node-1"))
		require.NoError(t, a.Flush())
	}
	server := &commitWorkerQueryServer{commitCounts: map[string]uint32{}, failModels: map[string]bool{}}
	client, cleanup := newCommitWorkerQueryClient(t, server)
	t.Cleanup(cleanup)
	recorder := &finalWindowRecorder{failures: map[string]error{}}
	recorder.On("NewInferenceQueryClient").Return(client)
	worker := &CommitWorker{store: store, recorder: recorder, tracker: &chainphase.ChainPhaseTracker{}, participantAddress: "participant_addr"}
	setFinalWindowHeight(worker.tracker, height)
	recorder.worker = worker
	return worker, recorder, server
}

func setFinalWindowHeight(tracker *chainphase.ChainPhaseTracker, height int64) {
	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: 100}
	params := &types.EpochParams{
		EpochLength:           1000,
		PocStageDuration:      100,
		PocExchangeDuration:   50,
		PocValidationDelay:    51,
		PocValidationDuration: 100,
	}
	tracker.Update(chainphase.BlockInfo{Height: height, Hash: fmt.Sprintf("h-%d", height)}, epoch, params, true, nil)
}

func TestCommitWorker_FinalWindowSplitsPendingBatchAndRetriesUntilDeadline(t *testing.T) {
	w, r, q := newFinalWindowWorker(t, 246)
	w.tick()
	require.Len(t, r.submissions, 1)
	require.Len(t, r.submissions[0].entries, 2)
	require.Equal(t, uint64(249), r.submissions[0].timeout)
	for h := int64(247); h <= 249; h++ {
		q.queryHeights = nil
		setFinalWindowHeight(w.tracker, h)
		w.tick()
		n := len(r.submissions)
		require.Equal(t, 1+2*int(h-246), n)
		for _, sub := range r.submissions[n-2:] {
			require.Len(t, sub.entries, 1)
			require.Equal(t, h, sub.height)
			require.Equal(t, uint64(250), sub.timeout)
			require.Equal(t, uint32(1), sub.entries[0].Count)
		}
		for _, queryHeight := range q.queryHeights {
			require.Equal(t, fmt.Sprint(h), queryHeight)
		}
		require.NotEmpty(t, q.queryHeights)
		w.tick()
		require.Len(t, r.submissions, n, "no repeat in the same block")
	}
	setFinalWindowHeight(w.tracker, 250)
	w.tick()
	require.Len(t, r.submissions, 7)
}

func TestCommitWorker_FinalWindowFailuresDoNotBlockOtherModels(t *testing.T) {
	for _, tc := range []struct {
		name       string
		queryFails bool
		err        error
	}{
		{"query", true, nil},
		{"transient", false, fmt.Errorf("connection lost")},
		{"fee", false, tx_manager.ErrTxCheckTxInsufficientFee},
		{"permanent", false, tx_manager.ErrTxCheckTxFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r, q := newFinalWindowWorker(t, 247)
			q.failModels["model-a"] = tc.queryFails
			r.failures["model-a"] = tc.err
			w.tick()
			b := commitKey{100, "model-b"}
			require.Equal(t, int64(247), w.pending[b].submittedHeight)
			require.Equal(t, uint64(250), w.pending[b].timeoutHeight)
			require.Equal(t, "model-b", r.submissions[len(r.submissions)-1].entries[0].ModelId)
			before := len(r.submissions)
			w.tick()
			require.Len(t, r.submissions, before)
			if tc.name == "fee" {
				require.Equal(t, 1, r.refreshes)
			}
		})
	}
}

func TestCommitWorker_FinalWindowOlderConfirmationKeepsNewerPending(t *testing.T) {
	w, r, q := newFinalWindowWorker(t, 247)
	w.tick()
	a, err := w.store.GetOrCreateStore(100, "model-a")
	require.NoError(t, err)
	require.NoError(t, a.AddWithNode(2, []byte("vec2"), "node-1"))
	require.NoError(t, a.Flush())
	setFinalWindowHeight(w.tracker, 248)
	w.tick()
	key := commitKey{100, "model-a"}
	require.Equal(t, uint32(2), w.pending[key].state.count)
	q.commitCounts["100|participant_addr|model-a"] = 1
	q.commitCounts["100|participant_addr|model-b"] = 1
	w.blockHeight = 249
	w.reconcilePending(100)
	require.Equal(t, uint32(1), w.lastCommitted[key].count)
	require.Equal(t, uint32(2), w.pending[key].state.count)
	before := len(r.submissions)
	setFinalWindowHeight(w.tracker, 249)
	w.tick()
	require.Len(t, r.submissions, before+1)
	require.Equal(t, "model-a", r.submissions[before].entries[0].ModelId)
	require.Equal(t, uint32(2), r.submissions[before].entries[0].Count)
	require.Equal(t, uint32(1), r.prev["model-a"])
}

func TestCommitWorker_NormalPendingModelDoesNotBlockNewModel(t *testing.T) {
	w, r, q := newFinalWindowWorker(t, 110)
	w.tick()
	q.commitCounts["100|participant_addr|model-b"] = 1
	b, err := w.store.GetOrCreateStore(100, "model-b")
	require.NoError(t, err)
	require.NoError(t, b.AddWithNode(2, []byte("vec2"), "node-1"))
	require.NoError(t, b.Flush())
	setFinalWindowHeight(w.tracker, 111)
	w.tick()
	require.Len(t, r.submissions, 2)
	require.Len(t, r.submissions[1].entries, 1)
	require.Equal(t, "model-b", r.submissions[1].entries[0].ModelId)
	require.Equal(t, uint32(2), r.submissions[1].entries[0].Count)
	require.Equal(t, int64(110), w.pending[commitKey{100, "model-a"}].submittedHeight)
}

func TestCommitWorker_FinalWindowAdmissionLimitIsPerModel(t *testing.T) {
	w, r, q := newFinalWindowWorker(t, 247)
	q.failModels["model-a"] = true
	w.tick()
	require.Len(t, r.submissions, 1)
	require.Equal(t, "model-b", r.submissions[0].entries[0].ModelId)
	b, err := w.store.GetOrCreateStore(100, "model-b")
	require.NoError(t, err)
	require.NoError(t, b.AddWithNode(2, []byte("vec2"), "node-1"))
	require.NoError(t, b.Flush())
	q.failModels["model-a"] = false
	w.tick()
	require.Len(t, r.submissions, 2)
	require.Equal(t, "model-a", r.submissions[1].entries[0].ModelId)
	w.tick()
	require.Len(t, r.submissions, 2)
	setFinalWindowHeight(w.tracker, 248)
	w.tick()
	require.Len(t, r.submissions, 4)
	require.Equal(t, uint32(2), w.pending[commitKey{100, "model-b"}].state.count)
}
