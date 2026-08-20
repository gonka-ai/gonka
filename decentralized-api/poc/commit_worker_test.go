package poc

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/cosmosclient/tx_manager"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type commitWorkerQueryServer struct {
	types.UnimplementedQueryServer
	commitCounts        map[string]uint32
	commitRoots         map[string][]byte
	distributionOnChain map[string]bool
	failCommitQuery     bool
}

func (s *commitWorkerQueryServer) commitKey(req *types.QueryPoCV2StoreCommitRequest) string {
	return fmt.Sprintf("%d|%s|%s", req.PocStageStartBlockHeight, req.ParticipantAddress, req.ModelId)
}

func (s *commitWorkerQueryServer) distributionKey(req *types.QueryMLNodeWeightDistributionRequest) string {
	return fmt.Sprintf("%d|%s|%s", req.PocStageStartBlockHeight, req.ParticipantAddress, req.ModelId)
}

func (s *commitWorkerQueryServer) PoCV2StoreCommit(_ context.Context, req *types.QueryPoCV2StoreCommitRequest) (*types.QueryPoCV2StoreCommitResponse, error) {
	if s.failCommitQuery {
		return nil, fmt.Errorf("query unavailable")
	}
	key := s.commitKey(req)
	count := s.commitCounts[key]
	var root []byte
	if s.commitRoots != nil {
		root = s.commitRoots[key]
	}
	return &types.QueryPoCV2StoreCommitResponse{
		Found:    count > 0,
		Count:    count,
		RootHash: root,
	}, nil
}

func (s *commitWorkerQueryServer) MLNodeWeightDistribution(_ context.Context, req *types.QueryMLNodeWeightDistributionRequest) (*types.QueryMLNodeWeightDistributionResponse, error) {
	return &types.QueryMLNodeWeightDistributionResponse{
		Found: s.distributionOnChain[s.distributionKey(req)],
	}, nil
}

func newCommitWorkerQueryClient(t *testing.T, server *commitWorkerQueryServer) (types.QueryClient, func()) {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	types.RegisterQueryServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
	)
	if err != nil {
		t.Fatalf("grpc dial failed: %v", err)
	}

	return types.NewQueryClient(conn), func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = listener.Close()
	}
}

func TestCommitWorker_ShouldAcceptStoreCommit_RegularPoC(t *testing.T) {
	tests := []struct {
		name           string
		phase          types.EpochPhase
		blockHeight    int64
		pocStartHeight int64
		expectAccept   bool
	}{
		{
			name:           "accept during generate phase in exchange window",
			phase:          types.PoCGeneratePhase,
			blockHeight:    110,
			pocStartHeight: 100,
			expectAccept:   true,
		},
		{
			name:           "accept during generate wind down phase",
			phase:          types.PoCGenerateWindDownPhase,
			blockHeight:    150,
			pocStartHeight: 100,
			expectAccept:   true,
		},
		{
			name:           "reject during inference phase",
			phase:          types.InferencePhase,
			blockHeight:    500,
			pocStartHeight: 100,
			expectAccept:   false,
		},
		{
			name:           "reject during validation phase",
			phase:          types.PoCValidatePhase,
			blockHeight:    200,
			pocStartHeight: 100,
			expectAccept:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createCommitWorkerTestEpochState(tt.phase, tt.blockHeight, tt.pocStartHeight)
			result := ShouldAcceptStoreCommit(epochState, tt.pocStartHeight)
			assert.Equal(t, tt.expectAccept, result)
		})
	}
}

func TestCommitWorker_ShouldHaveDistributedWeights(t *testing.T) {
	tests := []struct {
		name        string
		phase       types.EpochPhase
		blockHeight int64
		expect      bool
	}{
		{"validate phase", types.PoCValidatePhase, 300, true},
		{"validate wind down", types.PoCValidateWindDownPhase, 350, true},
		{"wind down after generation end", types.PoCGenerateWindDownPhase, 210, true},
		{"wind down before generation end", types.PoCGenerateWindDownPhase, 180, false},
		{"generate phase", types.PoCGeneratePhase, 120, false},
		{"inference phase", types.InferencePhase, 500, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createCommitWorkerTestEpochState(tt.phase, tt.blockHeight, 100)
			result := ShouldHaveDistributedWeights(epochState)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestCommitWorker_GetPocStageHeight_RegularPoC(t *testing.T) {
	epochState := createCommitWorkerTestEpochState(types.PoCGeneratePhase, 110, 100)
	height := GetCurrentPocStageHeight(epochState)

	assert.Equal(t, int64(100), height)
}

func TestCommitWorker_GetPocStageHeight_ConfirmationPoC(t *testing.T) {
	epochState := createCommitWorkerTestEpochState(types.InferencePhase, 500, 100)
	epochState.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
		TriggerHeight: 450,
		Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
	}

	height := GetCurrentPocStageHeight(epochState)

	assert.Equal(t, int64(450), height)
}

func TestCommitWorker_MaybeSubmitCommit_SkipsUnchanged(t *testing.T) {
	// Create temp dir for artifact store
	tmpDir, err := os.MkdirTemp("", "commit_worker_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}

	worker := &CommitWorker{
		store:         store,
		recorder:      mockRecorder,
		lastCommitted: make(map[commitKey]commitState),
	}

	pocHeight := int64(100)
	store.ActivateStage(pocHeight)

	// Get or create store and add an artifact
	artifactStore, err := store.GetOrCreateStore(pocHeight, "model-a")
	assert.NoError(t, err)

	err = artifactStore.AddWithNode(1, []byte("test-vector"), "node-1")
	assert.NoError(t, err)
	err = artifactStore.Flush()
	assert.NoError(t, err)

	// First commit should submit
	mockRecorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).Return(nil).Once()

	worker.maybeSubmitCommit(pocHeight)
	mockRecorder.AssertExpectations(t)
	assert.Empty(t, worker.lastCommitted, "CheckTx success must not promote lastCommitted")
	assert.NotEmpty(t, worker.pending)

	// Second commit with same state should NOT submit
	worker.maybeSubmitCommit(pocHeight)
	mockRecorder.AssertExpectations(t) // No additional calls expected
}

func TestCommitWorker_MaybeSubmitCommit_RetriesTransientCheckTx(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_retry_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	worker := &CommitWorker{
		store:         store,
		recorder:      mockRecorder,
		lastCommitted: make(map[commitKey]commitState),
	}

	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	artifactStore, err := store.GetOrCreateStore(pocHeight, "model-a")
	assert.NoError(t, err)
	assert.NoError(t, artifactStore.AddWithNode(1, []byte("test-vector"), "node-1"))
	assert.NoError(t, artifactStore.Flush())

	mockRecorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).
		Return(tx_manager.ErrTxCheckTxRetry).Once()
	worker.maybeSubmitCommit(pocHeight)
	assert.Empty(t, worker.lastCommitted)
	assert.Empty(t, worker.pending)

	mockRecorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).
		Return(nil).Once()
	worker.maybeSubmitCommit(pocHeight)
	mockRecorder.AssertExpectations(t)
	assert.Empty(t, worker.lastCommitted, "CheckTx success is pending, not committed")
	assert.NotEmpty(t, worker.pending)
}

func TestCommitWorker_MaybeSubmitCommit_SkipsPermanentUntilCountGrows(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_permanent_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	worker := &CommitWorker{
		store:         store,
		recorder:      mockRecorder,
		lastCommitted: make(map[commitKey]commitState),
	}

	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	artifactStore, err := store.GetOrCreateStore(pocHeight, "model-a")
	assert.NoError(t, err)
	assert.NoError(t, artifactStore.AddWithNode(1, []byte("test-vector"), "node-1"))
	assert.NoError(t, artifactStore.Flush())

	mockRecorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).
		Return(tx_manager.ErrTxCheckTxFail).Once()
	worker.maybeSubmitCommit(pocHeight)
	assert.Empty(t, worker.lastCommitted)

	worker.maybeSubmitCommit(pocHeight)
	mockRecorder.AssertExpectations(t)

	assert.NoError(t, artifactStore.AddWithNode(2, []byte("test-vector-2"), "node-1"))
	assert.NoError(t, artifactStore.Flush())
	mockRecorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).
		Return(nil).Once()
	worker.maybeSubmitCommit(pocHeight)
	mockRecorder.AssertExpectations(t)
	assert.Empty(t, worker.lastCommitted)
	assert.NotEmpty(t, worker.pending)
}

func TestCommitWorker_MaybeSubmitCommit_BatchesModels(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}

	worker := &CommitWorker{
		store:         store,
		recorder:      mockRecorder,
		lastCommitted: make(map[commitKey]commitState),
	}

	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	for _, tc := range []struct {
		modelID string
		nonce   int32
	}{
		{modelID: "model-a", nonce: 1},
		{modelID: "org/model-b", nonce: 2},
	} {
		artifactStore, err := store.GetOrCreateStore(pocHeight, tc.modelID)
		assert.NoError(t, err)
		err = artifactStore.AddWithNode(tc.nonce, []byte("test-vector"), "node-1")
		assert.NoError(t, err)
		err = artifactStore.Flush()
		assert.NoError(t, err)
	}

	mockRecorder.
		On("SubmitPoCV2StoreCommit", mock.MatchedBy(func(msg *types.MsgPoCV2StoreCommit) bool {
			if msg == nil || msg.PocStageStartBlockHeight != pocHeight || len(msg.Entries) != 2 {
				return false
			}
			return msg.Entries[0].ModelId == "model-a" && msg.Entries[1].ModelId == "org/model-b"
		})).
		Return(nil).
		Once()

	worker.maybeSubmitCommit(pocHeight)
	mockRecorder.AssertExpectations(t)
}

type storeCommitPrevRecorder struct {
	cosmosclient.MockCosmosMessageClient
	prev map[string]uint32
}

func (r *storeCommitPrevRecorder) SetStoreCommitPrev(prev map[string]uint32) {
	r.prev = prev
}

type feeRefreshRecorder struct {
	cosmosclient.MockCosmosMessageClient
	calls       int
	err         error
	sawDeadline bool
}

func (r *feeRefreshRecorder) RefreshFeeTree(ctx context.Context) error {
	r.calls++
	_, r.sawDeadline = ctx.Deadline()
	return r.err
}

func commitWorkerTestTracker(height int64) *chainphase.ChainPhaseTracker {
	tracker := &chainphase.ChainPhaseTracker{}
	setCommitWorkerHeight(tracker, height)
	return tracker
}

func setCommitWorkerHeight(tracker *chainphase.ChainPhaseTracker, height int64) {
	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: 100}
	params := &types.EpochParams{
		EpochLength:         1000,
		PocStageDuration:    100,
		PocExchangeDuration: 50,
	}
	tracker.Update(chainphase.BlockInfo{Height: height, Hash: fmt.Sprintf("h-%d", height)}, epoch, params, true, nil)
}

func TestCommitWorker_TickDoesNotDeadlockWhenRecorderSetsPrev(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_deadlock_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	recorder := &storeCommitPrevRecorder{}
	tracker := &chainphase.ChainPhaseTracker{}
	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: 100}
	params := &types.EpochParams{
		EpochLength:         1000,
		PocStageDuration:    100,
		PocExchangeDuration: 50,
	}
	tracker.Update(chainphase.BlockInfo{Height: 110, Hash: "test-hash"}, epoch, params, true, nil)

	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	artifactStore, err := store.GetOrCreateStore(pocHeight, "model-a")
	assert.NoError(t, err)
	assert.NoError(t, artifactStore.AddWithNode(1, []byte("test-vector"), "node-1"))
	assert.NoError(t, artifactStore.Flush())

	recorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).Return(nil).Once()

	worker := &CommitWorker{
		store:         store,
		recorder:      recorder,
		tracker:       tracker,
		lastCommitted: make(map[commitKey]commitState),
	}

	done := make(chan struct{})
	go func() {
		worker.tick()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tick() deadlocked on SetStoreCommitPrev nested mutex")
	}

	recorder.AssertExpectations(t)
	assert.NotNil(t, recorder.prev, "SetStoreCommitPrev must run under tick's existing lock")
	assert.Empty(t, worker.lastCommitted)
	assert.NotEmpty(t, worker.pending)
}

func TestCommitWorker_AdmissionSuccessStaysPendingUntilChainMatch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_pending_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	artifactStore, err := store.GetOrCreateStore(pocHeight, "model-a")
	assert.NoError(t, err)
	assert.NoError(t, artifactStore.AddWithNode(1, []byte("test-vector"), "node-1"))
	assert.NoError(t, artifactStore.Flush())
	count, root := artifactStore.GetFlushedRoot()

	queryServer := &commitWorkerQueryServer{
		commitCounts: map[string]uint32{},
		commitRoots:  map[string][]byte{},
	}
	queryClient, cleanup := newCommitWorkerQueryClient(t, queryServer)
	defer cleanup()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(queryClient)
	mockRecorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).Return(nil).Once()

	tracker := commitWorkerTestTracker(110)
	worker := &CommitWorker{
		store:              store,
		recorder:           mockRecorder,
		tracker:            tracker,
		participantAddress: "participant_addr",
		lastCommitted:      make(map[commitKey]commitState),
		pending:            make(map[commitKey]pendingCommit),
	}

	worker.tick()
	assert.Empty(t, worker.lastCommitted, "Code 0/19 must not update lastCommitted")
	assert.NotEmpty(t, worker.pending)

	queryServer.commitCounts["100|participant_addr|model-a"] = count
	queryServer.commitRoots["100|participant_addr|model-a"] = root
	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: 100}
	params := &types.EpochParams{EpochLength: 1000, PocStageDuration: 100, PocExchangeDuration: 50}
	tracker.Update(chainphase.BlockInfo{Height: 111, Hash: "next"}, epoch, params, true, nil)

	worker.tick()
	mockRecorder.AssertExpectations(t)
	key := commitKey{stage: pocHeight, modelID: "model-a"}
	got, ok := worker.lastCommitted[key]
	assert.True(t, ok, "canonical count/root match must promote lastCommitted")
	assert.Equal(t, count, got.count)
	assert.Empty(t, worker.pending)
}

func TestCommitWorker_SamePayloadWaitsGraceBeforeRetry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_retry_absent_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	artifactStore, err := store.GetOrCreateStore(pocHeight, "model-a")
	assert.NoError(t, err)
	assert.NoError(t, artifactStore.AddWithNode(1, []byte("test-vector"), "node-1"))
	assert.NoError(t, artifactStore.Flush())

	queryServer := &commitWorkerQueryServer{commitCounts: map[string]uint32{}}
	queryClient, cleanup := newCommitWorkerQueryClient(t, queryServer)
	defer cleanup()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(queryClient)
	mockRecorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).Return(nil).Twice()

	tracker := commitWorkerTestTracker(110)
	worker := &CommitWorker{
		store:              store,
		recorder:           mockRecorder,
		tracker:            tracker,
		participantAddress: "participant_addr",
		lastCommitted:      make(map[commitKey]commitState),
		pending:            make(map[commitKey]pendingCommit),
	}

	worker.tick()
	assert.Empty(t, worker.lastCommitted)
	assert.NotEmpty(t, worker.pending)

	setCommitWorkerHeight(tracker, 111)
	worker.tick()
	setCommitWorkerHeight(tracker, 112)
	worker.tick()
	mockRecorder.AssertNumberOfCalls(t, "SubmitPoCV2StoreCommit", 1)

	setCommitWorkerHeight(tracker, 113)
	worker.tick()
	mockRecorder.AssertExpectations(t)
	assert.Empty(t, worker.lastCommitted, "absent chain commit must not promote")
}

func TestCommitWorker_QueryOutageDoesNotResendSamePayload(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_query_outage_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	artifactStore, err := store.GetOrCreateStore(pocHeight, "model-a")
	assert.NoError(t, err)
	assert.NoError(t, artifactStore.AddWithNode(1, []byte("test-vector"), "node-1"))
	assert.NoError(t, artifactStore.Flush())

	queryServer := &commitWorkerQueryServer{commitCounts: map[string]uint32{}}
	queryClient, cleanup := newCommitWorkerQueryClient(t, queryServer)
	defer cleanup()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(queryClient)
	mockRecorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).Return(nil).Once()

	tracker := commitWorkerTestTracker(110)
	worker := &CommitWorker{
		store:              store,
		recorder:           mockRecorder,
		tracker:            tracker,
		participantAddress: "participant_addr",
		lastCommitted:      make(map[commitKey]commitState),
		pending:            make(map[commitKey]pendingCommit),
	}

	worker.tick()
	queryServer.failCommitQuery = true
	for h := int64(111); h <= 120; h++ {
		setCommitWorkerHeight(tracker, h)
		worker.tick()
	}
	mockRecorder.AssertExpectations(t)
	assert.NotEmpty(t, worker.pending)
}

func TestCommitWorker_HigherCountSubmitsWhileOlderPending(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_higher_count_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	artifactStore, err := store.GetOrCreateStore(pocHeight, "model-a")
	assert.NoError(t, err)
	assert.NoError(t, artifactStore.AddWithNode(1, []byte("vec-1"), "node-1"))
	assert.NoError(t, artifactStore.Flush())

	queryServer := &commitWorkerQueryServer{commitCounts: map[string]uint32{}}
	queryClient, cleanup := newCommitWorkerQueryClient(t, queryServer)
	defer cleanup()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(queryClient)
	mockRecorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).Return(nil).Twice()

	tracker := commitWorkerTestTracker(110)
	worker := &CommitWorker{
		store:              store,
		recorder:           mockRecorder,
		tracker:            tracker,
		participantAddress: "participant_addr",
		lastCommitted:      make(map[commitKey]commitState),
		pending:            make(map[commitKey]pendingCommit),
	}

	worker.tick()
	assert.NoError(t, artifactStore.AddWithNode(2, []byte("vec-2"), "node-1"))
	assert.NoError(t, artifactStore.Flush())
	worker.tick()
	mockRecorder.AssertExpectations(t)
	key := commitKey{stage: pocHeight, modelID: "model-a"}
	pending, ok := worker.pending[key]
	assert.True(t, ok)
	count, _ := artifactStore.GetFlushedRoot()
	assert.Equal(t, count, pending.state.count)
	assert.Greater(t, pending.state.count, uint32(1))
}

func TestCommitWorker_InsufficientFee_DoesNotPermanentFail(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_insuff_fee_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	artifactStore, err := store.GetOrCreateStore(pocHeight, "model-a")
	assert.NoError(t, err)
	assert.NoError(t, artifactStore.AddWithNode(1, []byte("test-vector"), "node-1"))
	assert.NoError(t, artifactStore.Flush())

	recorder := &feeRefreshRecorder{}
	recorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).
		Return(tx_manager.ErrTxCheckTxInsufficientFee).Once()

	tracker := commitWorkerTestTracker(110)
	worker := &CommitWorker{
		store:           store,
		recorder:        recorder,
		tracker:         tracker,
		lastCommitted:   make(map[commitKey]commitState),
		permanentFailed: make(map[commitKey]uint32),
	}

	worker.tick()
	assert.Empty(t, worker.permanentFailed)
	assert.Empty(t, worker.lastCommitted)
	assert.Equal(t, 1, recorder.calls)
	assert.True(t, recorder.sawDeadline, "fee refresh must use a bounded context")

	worker.tick()
	recorder.AssertExpectations(t)

	epoch := &types.Epoch{Index: 1, PocStartBlockHeight: 100}
	params := &types.EpochParams{EpochLength: 1000, PocStageDuration: 100, PocExchangeDuration: 50}
	tracker.Update(chainphase.BlockInfo{Height: 111, Hash: "next"}, epoch, params, true, nil)
	recorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).Return(nil).Once()
	worker.tick()
	recorder.AssertExpectations(t)
	assert.Empty(t, worker.permanentFailed)
	assert.NotEmpty(t, worker.pending)
}

func TestCommitWorker_InsufficientFee_FailedRefreshKeepsCountEligible(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_fee_refresh_fail_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	pocHeight := int64(100)
	store.ActivateStage(pocHeight)
	artifactStore, err := store.GetOrCreateStore(pocHeight, "model-a")
	assert.NoError(t, err)
	assert.NoError(t, artifactStore.AddWithNode(1, []byte("test-vector"), "node-1"))
	assert.NoError(t, artifactStore.Flush())

	recorder := &feeRefreshRecorder{err: fmt.Errorf("params rpc down")}
	recorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*types.MsgPoCV2StoreCommit")).
		Return(tx_manager.ErrTxCheckTxInsufficientFee).Once()

	tracker := commitWorkerTestTracker(110)
	worker := &CommitWorker{
		store:           store,
		recorder:        recorder,
		tracker:         tracker,
		lastCommitted:   make(map[commitKey]commitState),
		permanentFailed: make(map[commitKey]uint32),
	}

	worker.tick()
	assert.Empty(t, worker.permanentFailed, "failed refresh must not suppress the count")
	assert.Equal(t, 1, recorder.calls)
	assert.True(t, recorder.sawDeadline)
}

func TestCommitWorker_StartAndStop(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	tracker := &chainphase.ChainPhaseTracker{}

	worker := NewCommitWorker(store, mockRecorder, tracker, "participant_addr", 100*time.Millisecond)

	// Worker should start
	assert.NotNil(t, worker)

	// Give it time to tick once
	time.Sleep(150 * time.Millisecond)

	// Close should complete without hanging
	done := make(chan struct{})
	go func() {
		worker.Close()
		close(done)
	}()

	select {
	case <-done:
		// Good - closed successfully
	case <-time.After(2 * time.Second):
		t.Fatal("Worker.Close() timed out")
	}
}

// Helper functions

func createCommitWorkerTestEpochState(phase types.EpochPhase, blockHeight, pocStartHeight int64) *chainphase.EpochState {
	epochParams := types.EpochParams{
		EpochLength:           1000,
		EpochShift:            0,
		PocStageDuration:      100,
		PocExchangeDuration:   50,
		PocValidationDelay:    10,
		PocValidationDuration: 100,
	}

	epoch := types.Epoch{
		Index:               1,
		PocStartBlockHeight: pocStartHeight,
	}

	return &chainphase.EpochState{
		LatestEpoch: types.NewEpochContext(epoch, epochParams),
		CurrentBlock: chainphase.BlockInfo{
			Height: blockHeight,
			Hash:   "test-hash",
		},
		CurrentPhase: phase,
		IsSynced:     true,
	}
}

func TestCommitWorker_SubmitWeightDistribution_NoCommitFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()
	store.ActivateStage(100)

	_, err = store.GetOrCreateStore(100, "model-a")
	assert.NoError(t, err)
}

func TestCommitWorker_SubmitWeightDistribution_BatchesModels(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()
	store.ActivateStage(100)

	for idx, modelID := range []string{"model-a", "org/model-b"} {
		artifactStore, err := store.GetOrCreateStore(100, modelID)
		assert.NoError(t, err)
		assert.NoError(t, artifactStore.AddWithNode(int32(idx*10+1), []byte("vec-1"), fmt.Sprintf("node-%d-a", idx)))
		assert.NoError(t, artifactStore.AddWithNode(int32(idx*10+2), []byte("vec-2"), fmt.Sprintf("node-%d-b", idx)))
		assert.NoError(t, artifactStore.Flush())
	}

	queryClient, cleanup := newCommitWorkerQueryClient(t, &commitWorkerQueryServer{
		commitCounts: map[string]uint32{
			"100|participant_addr|model-a":     2,
			"100|participant_addr|org/model-b": 2,
		},
		distributionOnChain: map[string]bool{},
	})
	defer cleanup()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(queryClient)
	mockRecorder.
		On("SubmitMLNodeWeightDistribution", mock.MatchedBy(func(msg *types.MsgMLNodeWeightDistribution) bool {
			if msg == nil || msg.PocStageStartBlockHeight != 100 || len(msg.Entries) != 2 {
				return false
			}
			return msg.Entries[0].ModelId == "model-a" && len(msg.Entries[0].Weights) == 2 &&
				msg.Entries[1].ModelId == "org/model-b" && len(msg.Entries[1].Weights) == 2
		})).
		Return(nil).
		Once()

	worker := &CommitWorker{
		store:              store,
		recorder:           mockRecorder,
		participantAddress: "participant_addr",
		lastCommitted:      make(map[commitKey]commitState),
	}

	worker.submitWeightDistribution(100)
	mockRecorder.AssertExpectations(t)
}

func TestCommitWorker_SubmitWeightDistribution_OneModelAlreadyOnChain(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()
	store.ActivateStage(100)

	for idx, modelID := range []string{"model-a", "org/model-b"} {
		artifactStore, err := store.GetOrCreateStore(100, modelID)
		assert.NoError(t, err)
		assert.NoError(t, artifactStore.AddWithNode(int32(idx+1), []byte("vec"), fmt.Sprintf("node-%d", idx)))
		assert.NoError(t, artifactStore.Flush())
	}

	queryClient, cleanup := newCommitWorkerQueryClient(t, &commitWorkerQueryServer{
		commitCounts: map[string]uint32{
			"100|participant_addr|model-a":     1,
			"100|participant_addr|org/model-b": 1,
		},
		distributionOnChain: map[string]bool{
			"100|participant_addr|model-a": true,
		},
	})
	defer cleanup()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(queryClient)
	mockRecorder.
		On("SubmitMLNodeWeightDistribution", mock.MatchedBy(func(msg *types.MsgMLNodeWeightDistribution) bool {
			return msg != nil &&
				msg.PocStageStartBlockHeight == 100 &&
				len(msg.Entries) == 1 &&
				msg.Entries[0].ModelId == "org/model-b"
		})).
		Return(nil).
		Once()

	worker := &CommitWorker{
		store:              store,
		recorder:           mockRecorder,
		participantAddress: "participant_addr",
		lastCommitted:      make(map[commitKey]commitState),
	}

	worker.submitWeightDistribution(100)
	mockRecorder.AssertExpectations(t)
}

func TestCommitWorker_HasPendingWeightDistribution(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()
	store.ActivateStage(100)

	for _, modelID := range []string{"model-a", "model-b"} {
		artifactStore, err := store.GetOrCreateStore(100, modelID)
		assert.NoError(t, err)
		assert.NoError(t, artifactStore.AddWithNode(1, []byte("vec"), "node-"+modelID))
		assert.NoError(t, artifactStore.Flush())
	}

	queryClient, cleanup := newCommitWorkerQueryClient(t, &commitWorkerQueryServer{
		commitCounts: map[string]uint32{
			"100|participant_addr|model-a": 1,
			"100|participant_addr|model-b": 1,
		},
		distributionOnChain: map[string]bool{
			"100|participant_addr|model-a": true,
			"100|participant_addr|model-b": false,
		},
	})
	defer cleanup()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(queryClient)

	worker := &CommitWorker{
		store:              store,
		recorder:           mockRecorder,
		participantAddress: "participant_addr",
		lastCommitted:      make(map[commitKey]commitState),
	}

	assert.True(t, worker.hasPendingWeightDistribution(100))
	mockRecorder.AssertExpectations(t)
}

func TestCommitWorker_SubmitWeightDistribution_UpdatesLastAttemptOnNoOp(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()
	store.ActivateStage(100)

	artifactStore, err := store.GetOrCreateStore(100, "model-a")
	assert.NoError(t, err)
	assert.NoError(t, artifactStore.AddWithNode(1, []byte("vec"), "node-a"))
	assert.NoError(t, artifactStore.Flush())

	queryClient, cleanup := newCommitWorkerQueryClient(t, &commitWorkerQueryServer{
		commitCounts: map[string]uint32{
			"100|participant_addr|model-a": 1,
		},
		distributionOnChain: map[string]bool{
			"100|participant_addr|model-a": true,
		},
	})
	defer cleanup()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(queryClient)

	worker := &CommitWorker{
		store:              store,
		recorder:           mockRecorder,
		participantAddress: "participant_addr",
		lastCommitted:      make(map[commitKey]commitState),
	}

	worker.submitWeightDistribution(100)
	assert.False(t, worker.lastDistributionAttempt.IsZero())
	mockRecorder.AssertExpectations(t)
}

func TestGetWeightDistribution_ExactMatch(t *testing.T) {
	distribution := map[string]uint32{
		"node1": 100,
		"node2": 200,
		"node3": 300,
	}
	targetCount := uint32(600)

	weights, err := getWeightDistribution(distribution, targetCount)

	assert.NoError(t, err)
	assert.Len(t, weights, 3)
	assertWeightSum(t, weights, targetCount)
}

func TestGetWeightDistribution_ScaleUp(t *testing.T) {
	tests := []struct {
		name         string
		distribution map[string]uint32
		targetCount  uint32
	}{
		{
			name:         "small scale up",
			distribution: map[string]uint32{"node1": 100, "node2": 200},
			targetCount:  400,
		},
		{
			name:         "large scale up",
			distribution: map[string]uint32{"node1": 10, "node2": 20},
			targetCount:  1000,
		},
		{
			name:         "scale from original error case",
			distribution: map[string]uint32{"node1": 5232, "node2": 5232},
			targetCount:  10688,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			weights, err := getWeightDistribution(tt.distribution, tt.targetCount)
			assert.NoError(t, err)
			assertWeightSum(t, weights, tt.targetCount)
		})
	}
}

func TestGetWeightDistribution_ScaleDown(t *testing.T) {
	tests := []struct {
		name         string
		distribution map[string]uint32
		targetCount  uint32
	}{
		{
			name:         "small scale down",
			distribution: map[string]uint32{"node1": 500, "node2": 500},
			targetCount:  800,
		},
		{
			name:         "large scale down",
			distribution: map[string]uint32{"node1": 10000, "node2": 5000},
			targetCount:  100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			weights, err := getWeightDistribution(tt.distribution, tt.targetCount)
			assert.NoError(t, err)
			assertWeightSum(t, weights, tt.targetCount)
		})
	}
}

func TestGetWeightDistribution_SingleNode(t *testing.T) {
	distribution := map[string]uint32{"node1": 50}
	targetCount := uint32(100)

	weights, err := getWeightDistribution(distribution, targetCount)

	assert.NoError(t, err)
	assert.Len(t, weights, 1)
	assertWeightSum(t, weights, targetCount)
}

func TestGetWeightDistribution_ManyNodesSmallWeights(t *testing.T) {
	distribution := map[string]uint32{}
	for i := 0; i < 100; i++ {
		distribution[fmt.Sprintf("node%d", i)] = 1
	}
	targetCount := uint32(500)

	weights, err := getWeightDistribution(distribution, targetCount)

	assert.NoError(t, err)
	assertWeightSum(t, weights, targetCount)
}

func TestGetWeightDistribution_LargeDiffRoundRobin(t *testing.T) {
	distribution := map[string]uint32{"node1": 1, "node2": 1}
	targetCount := uint32(1000)

	weights, err := getWeightDistribution(distribution, targetCount)

	assert.NoError(t, err)
	assert.Len(t, weights, 2)
	assertWeightSum(t, weights, targetCount)
}

func TestGetWeightDistribution_Errors(t *testing.T) {
	tests := []struct {
		name         string
		distribution map[string]uint32
		targetCount  uint32
		expectError  string
	}{
		{
			name:         "empty distribution",
			distribution: map[string]uint32{},
			targetCount:  100,
			expectError:  "empty distribution",
		},
		{
			name:         "zero target",
			distribution: map[string]uint32{"node1": 100},
			targetCount:  0,
			expectError:  "targetCount is 0",
		},
		{
			name:         "zero sum distribution",
			distribution: map[string]uint32{"node1": 0, "node2": 0},
			targetCount:  100,
			expectError:  "distribution sum is 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := getWeightDistribution(tt.distribution, tt.targetCount)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectError)
		})
	}
}

func TestGetWeightDistribution_AlwaysExactSum(t *testing.T) {
	testCases := []struct {
		localSum    uint32
		targetCount uint32
		nodes       int
	}{
		{100, 200, 2},
		{200, 100, 2},
		{333, 500, 3},
		{500, 333, 3},
		{1, 1000, 5},
		{10464, 10688, 2},
		{10688, 10464, 2},
		{7, 1000, 7},
		{1000, 7, 7},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("local%d_target%d_nodes%d", tc.localSum, tc.targetCount, tc.nodes), func(t *testing.T) {
			distribution := make(map[string]uint32)
			perNode := tc.localSum / uint32(tc.nodes)
			remainder := tc.localSum % uint32(tc.nodes)
			for i := 0; i < tc.nodes; i++ {
				w := perNode
				if uint32(i) < remainder {
					w++
				}
				distribution[fmt.Sprintf("node%d", i)] = w
			}

			weights, err := getWeightDistribution(distribution, tc.targetCount)
			assert.NoError(t, err)
			assertWeightSum(t, weights, tc.targetCount)
		})
	}
}

func assertWeightSum(t *testing.T, weights []*types.MLNodeWeight, expected uint32) {
	t.Helper()
	var sum uint32
	for _, w := range weights {
		sum += w.Weight
	}
	assert.Equal(t, expected, sum, "weight sum should equal target exactly")
}

func TestCommitWorker_HeightChangeResetsState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	defer store.Close()

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}

	worker := &CommitWorker{
		store:              store,
		recorder:           mockRecorder,
		participantAddress: "test_addr",
		lastCommitted:      make(map[commitKey]commitState),
		currentPocHeight:   100,
	}

	worker.lastCommitted[commitKey{stage: 100, modelID: "model-a"}] = commitState{count: 50, rootHash: []byte("hash")}
	worker.lastDistributionAttempt = time.Now().Add(-time.Hour)

	epochState := createCommitWorkerTestEpochState(types.PoCGeneratePhase, 210, 200)

	worker.mu.Lock()
	pocHeight := GetCurrentPocStageHeight(epochState)
	if pocHeight > 0 && worker.currentPocHeight != pocHeight {
		worker.currentPocHeight = pocHeight
		worker.lastDistributionAttempt = time.Time{}
		worker.lastCommitted = make(map[commitKey]commitState)
	}
	worker.mu.Unlock()

	assert.Equal(t, int64(200), worker.currentPocHeight)
	assert.True(t, worker.lastDistributionAttempt.IsZero())
	assert.Empty(t, worker.lastCommitted)
}

func TestCommitWorker_RetryLogic(t *testing.T) {
	tests := []struct {
		name                    string
		lastDistributionAttempt time.Time
		expectRetry             bool
	}{
		{
			name:                    "first attempt triggers immediately",
			lastDistributionAttempt: time.Time{},
			expectRetry:             true,
		},
		{
			name:                    "retry after interval",
			lastDistributionAttempt: time.Now().Add(-35 * time.Second),
			expectRetry:             true,
		},
		{
			name:                    "no retry within interval",
			lastDistributionAttempt: time.Now().Add(-10 * time.Second),
			expectRetry:             false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shouldRetry := tt.lastDistributionAttempt.IsZero() ||
				time.Since(tt.lastDistributionAttempt) > distributionRetryInterval
			assert.Equal(t, tt.expectRetry, shouldRetry)
		})
	}
}
