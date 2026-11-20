package poc

import (
	"context"
	"fmt"
	"os"
	"testing"

	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/cosmosclient/tx_manager"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type epochGroupQueryServer struct {
	commitWorkerQueryServer
	groups map[string]types.EpochGroupData
	calls  int
}

func (s *epochGroupQueryServer) EpochGroupData(_ context.Context, req *types.QueryGetEpochGroupDataRequest) (*types.QueryGetEpochGroupDataResponse, error) {
	s.calls++
	return &types.QueryGetEpochGroupDataResponse{EpochGroupData: s.groups[req.ModelId]}, nil
}

func newEpochGroupQueryServer(participant string) *epochGroupQueryServer {
	return &epochGroupQueryServer{
		commitWorkerQueryServer: commitWorkerQueryServer{commitCounts: map[string]uint32{}},
		groups: map[string]types.EpochGroupData{
			"":        {EpochIndex: 1, SubGroupModels: []string{"model-a", "model-b"}},
			"model-a": {EpochIndex: 1, ModelId: "model-a", ValidationWeights: []*types.ValidationWeight{{MemberAddress: participant}}},
			"model-b": {EpochIndex: 1, ModelId: "model-b", ValidationWeights: []*types.ValidationWeight{{MemberAddress: "someone-else"}}},
		},
	}
}

func TestKeepEpochModelEntries(t *testing.T) {
	entries := []*types.PoCV2CommitEntry{{ModelId: "model-a"}, {ModelId: "model-b"}}

	require.Equal(t, entries, keepEpochModelEntries(entries, nil, 100))
	kept := keepEpochModelEntries(entries, map[string]bool{"model-a": true}, 100)
	require.Len(t, kept, 1)
	require.Equal(t, "model-a", kept[0].ModelId)
	require.Len(t, entries, 2)
	require.Empty(t, keepEpochModelEntries(entries, map[string]bool{}, 100))
}

func TestCommitWorker_OwnEpochModelsFromSubgroupMembership(t *testing.T) {
	server := newEpochGroupQueryServer("participant_addr")
	queryClient, cleanup := newCommitWorkerQueryClient(t, server)
	defer cleanup()
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("NewInferenceQueryClient").Return(queryClient)
	worker := &CommitWorker{recorder: recorder, participantAddress: "participant_addr"}

	models, ok := worker.ownEpochModels(1)
	require.True(t, ok)
	require.Equal(t, map[string]bool{"model-a": true}, models)
	calls := server.calls

	models, ok = worker.ownEpochModels(1)
	require.True(t, ok)
	require.Equal(t, map[string]bool{"model-a": true}, models)
	require.Equal(t, calls, server.calls)
}

func TestCommitWorker_OwnEpochModelsMatchesMemberAddressCaseInsensitively(t *testing.T) {
	server := newEpochGroupQueryServer("GONKA1PARTICIPANT")
	queryClient, cleanup := newCommitWorkerQueryClient(t, server)
	defer cleanup()
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("NewInferenceQueryClient").Return(queryClient)
	worker := &CommitWorker{recorder: recorder, participantAddress: "gonka1participant"}

	models, ok := worker.ownEpochModels(1)
	require.True(t, ok)
	require.Equal(t, map[string]bool{"model-a": true}, models)
}

func TestCommitWorker_OwnEpochModelsUnknownWhenQueryFails(t *testing.T) {
	queryClient, cleanup := newCommitWorkerQueryClient(t, &commitWorkerQueryServer{})
	defer cleanup()
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("NewInferenceQueryClient").Return(queryClient)
	worker := &CommitWorker{recorder: recorder, participantAddress: "participant_addr"}

	models, ok := worker.ownEpochModels(1)
	require.False(t, ok)
	require.Nil(t, models)
}

func TestCommitWorker_ConfirmationStageEpochModels(t *testing.T) {
	worker := &CommitWorker{
		participantAddress: "participant_addr",
		epochModelsIndex:   7,
		epochModels:        map[string]bool{"model-a": true},
	}
	event := &types.ConfirmationPoCEvent{EpochIndex: 7, TriggerHeight: 500}

	models, unknown := worker.confirmationStageEpochModels(&chainphase.EpochState{ActiveConfirmationPoCEvent: event}, 500)
	require.Equal(t, map[string]bool{"model-a": true}, models)
	require.False(t, unknown)
	models, unknown = worker.confirmationStageEpochModels(&chainphase.EpochState{ActiveConfirmationPoCEvent: event}, 100)
	require.Nil(t, models)
	require.False(t, unknown)
	models, unknown = worker.confirmationStageEpochModels(&chainphase.EpochState{}, 500)
	require.Nil(t, models)
	require.False(t, unknown)
}

func TestCommitWorker_ConfirmationStageEpochModelsUnknownWhenQueryFails(t *testing.T) {
	queryClient, cleanup := newCommitWorkerQueryClient(t, &commitWorkerQueryServer{})
	defer cleanup()
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("NewInferenceQueryClient").Return(queryClient)
	worker := &CommitWorker{recorder: recorder, participantAddress: "participant_addr"}
	event := &types.ConfirmationPoCEvent{EpochIndex: 7, TriggerHeight: 500}

	models, unknown := worker.confirmationStageEpochModels(&chainphase.EpochState{ActiveConfirmationPoCEvent: event}, 500)
	require.Nil(t, models)
	require.True(t, unknown)
}

func TestCommitWorker_OwnEpochModelsRetriesUnpopulatedEpochGroup(t *testing.T) {
	server := newEpochGroupQueryServer("participant_addr")
	populated := server.groups[""]
	server.groups[""] = types.EpochGroupData{EpochIndex: 1}
	queryClient, cleanup := newCommitWorkerQueryClient(t, server)
	defer cleanup()
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("NewInferenceQueryClient").Return(queryClient)
	worker := &CommitWorker{recorder: recorder, participantAddress: "participant_addr"}

	models, ok := worker.ownEpochModels(1)
	require.False(t, ok)
	require.Nil(t, models)

	server.groups[""] = populated
	models, ok = worker.ownEpochModels(1)
	require.True(t, ok)
	require.Equal(t, map[string]bool{"model-a": true}, models)
}

func TestCommitWorker_UnknownEpochModelsSendModelsSeparately(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_unknown_epoch_models")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)
	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()
	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	for _, model := range []string{"model-a", "model-b"} {
		artifactStore, err := store.GetOrCreateStore(pocHeight, model)
		require.NoError(t, err)
		require.NoError(t, artifactStore.AddWithNode(1, []byte("vec-"+model), "node-1"))
		require.NoError(t, artifactStore.Flush())
	}

	queryClient, cleanup := newCommitWorkerQueryClient(t, &commitWorkerQueryServer{commitCounts: map[string]uint32{}})
	defer cleanup()
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("NewInferenceQueryClient").Return(queryClient)
	recorder.On("SubmitPoCV2StoreCommitWithTimeout", mock.MatchedBy(func(msg *types.MsgPoCV2StoreCommit) bool {
		return len(msg.Entries) == 1 && msg.Entries[0].ModelId == "model-a"
	}), mock.Anything).Return(nil).Once()
	recorder.On("SubmitPoCV2StoreCommitWithTimeout", mock.MatchedBy(func(msg *types.MsgPoCV2StoreCommit) bool {
		return len(msg.Entries) == 1 && msg.Entries[0].ModelId == "model-b"
	}), mock.Anything).Return(fmt.Errorf("model outside epoch: %w", tx_manager.ErrTxCheckTxFail)).Once()

	worker := &CommitWorker{
		store:              store,
		recorder:           recorder,
		tracker:            commitWorkerTestTracker(110),
		participantAddress: "participant_addr",
		blockHeight:        110,
		stageModelsUnknown: true,
	}
	worker.maybeSubmitCommit(pocHeight, 150)
	recorder.AssertExpectations(t)

	own := commitKey{stage: pocHeight, modelID: "model-a"}
	foreign := commitKey{stage: pocHeight, modelID: "model-b"}
	require.Contains(t, worker.pending, own)
	require.NotContains(t, worker.permanentFailed, own)
	require.Contains(t, worker.permanentFailed, foreign)
}

func TestCommitWorker_ChallengeUnknownEpochModelsSendModelsSeparately(t *testing.T) {
	w, r, ch := newChallengeFrequencyWorker(t)
	w.epochModels = nil
	queryClient, cleanup := newCommitWorkerQueryClient(t, &commitWorkerQueryServer{})
	defer cleanup()
	r.On("NewInferenceQueryClient").Return(queryClient)
	r.failures["b"] = fmt.Errorf("model outside epoch: %w", tx_manager.ErrTxCheckTxFail)
	growChallengeModel(t, w, "a", 100)
	growChallengeModel(t, w, "b", 100)

	sendChallengeAt(w, ch, 800)

	require.Len(t, r.submissions, 2)
	for _, submission := range r.submissions {
		require.Len(t, submission.entries, 1)
	}
	require.Contains(t, w.challengePending, commitKey{stage: 500, modelID: "a"})
	require.NotContains(t, w.challengePending, commitKey{stage: 500, modelID: "b"})
}

func TestCommitWorker_ConfirmationCommitSkipsModelOutsideEpoch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_epoch_models")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)
	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()
	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	for _, model := range []string{"model-a", "model-b"} {
		artifactStore, err := store.GetOrCreateStore(pocHeight, model)
		require.NoError(t, err)
		require.NoError(t, artifactStore.AddWithNode(1, []byte("vec-"+model), "node-1"))
		require.NoError(t, artifactStore.Flush())
	}

	queryClient, cleanup := newCommitWorkerQueryClient(t, &commitWorkerQueryServer{commitCounts: map[string]uint32{}})
	defer cleanup()
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("NewInferenceQueryClient").Return(queryClient)
	recorder.On("SubmitPoCV2StoreCommitWithTimeout", mock.MatchedBy(func(msg *types.MsgPoCV2StoreCommit) bool {
		return len(msg.Entries) == 1 && msg.Entries[0].ModelId == "model-a"
	}), mock.Anything).Return(nil).Once()

	worker := &CommitWorker{
		store:              store,
		recorder:           recorder,
		tracker:            commitWorkerTestTracker(110),
		participantAddress: "participant_addr",
		blockHeight:        110,
		stageEpochModels:   map[string]bool{"model-a": true},
	}
	worker.maybeSubmitCommit(pocHeight, 150)
	recorder.AssertExpectations(t)
}

func TestCommitWorker_ChallengeCommitSkipsModelOutsideEpoch(t *testing.T) {
	w, r, ch := newChallengeFrequencyWorker(t)
	w.epochModels = map[string]bool{"a": true}
	growChallengeModel(t, w, "a", 100)
	growChallengeModel(t, w, "b", 100)

	sendChallengeAt(w, ch, 800)

	require.Len(t, r.submissions, 1)
	require.Len(t, r.submissions[0].entries, 1)
	require.Equal(t, "a", r.submissions[0].entries[0].ModelId)
}

func TestCommitWorker_UnknownEpochModelsForeignFirstThenOwnRetriedAfterRecovery(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_unknown_then_known")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)
	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()
	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	for _, model := range []string{"model-b", "model-z"} {
		artifactStore, err := store.GetOrCreateStore(pocHeight, model)
		require.NoError(t, err)
		require.NoError(t, artifactStore.AddWithNode(1, []byte("vec-"+model), "node-1"))
		require.NoError(t, artifactStore.Flush())
	}

	queryClient, cleanup := newCommitWorkerQueryClient(t, &commitWorkerQueryServer{commitCounts: map[string]uint32{}})
	defer cleanup()
	var submitted []string
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("NewInferenceQueryClient").Return(queryClient)
	singleModel := func(modelID string) interface{} {
		return mock.MatchedBy(func(msg *types.MsgPoCV2StoreCommit) bool {
			return len(msg.Entries) == 1 && msg.Entries[0].ModelId == modelID
		})
	}
	record := func(args mock.Arguments) {
		submitted = append(submitted, args.Get(0).(*types.MsgPoCV2StoreCommit).Entries[0].ModelId)
	}
	recorder.On("SubmitPoCV2StoreCommitWithTimeout", singleModel("model-b"), mock.Anything).
		Run(record).Return(fmt.Errorf("model outside epoch: %w", tx_manager.ErrTxCheckTxFail)).Once()
	recorder.On("SubmitPoCV2StoreCommitWithTimeout", singleModel("model-z"), mock.Anything).
		Run(record).Return(fmt.Errorf("connection reset")).Once()
	recorder.On("SubmitPoCV2StoreCommitWithTimeout", singleModel("model-z"), mock.Anything).
		Run(record).Return(nil).Once()

	worker := &CommitWorker{
		store:              store,
		recorder:           recorder,
		tracker:            commitWorkerTestTracker(110),
		participantAddress: "participant_addr",
		blockHeight:        110,
		stageModelsUnknown: true,
	}
	worker.maybeSubmitCommit(pocHeight, 150)

	own := commitKey{stage: pocHeight, modelID: "model-z"}
	foreign := commitKey{stage: pocHeight, modelID: "model-b"}
	require.Equal(t, []string{"model-b", "model-z"}, submitted)
	require.Contains(t, worker.permanentFailed, foreign)
	require.NotContains(t, worker.permanentFailed, own)
	require.NotContains(t, worker.pending, own)

	worker.blockHeight = 111
	worker.stageModelsUnknown = false
	worker.stageEpochModels = map[string]bool{"model-z": true}
	worker.maybeSubmitCommit(pocHeight, 150)

	require.Equal(t, []string{"model-b", "model-z", "model-z"}, submitted)
	require.Contains(t, worker.pending, own)
	recorder.AssertExpectations(t)
}
