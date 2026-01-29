package poc

import (
	"fmt"
	"os"
	"testing"
	"time"

	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

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
		name   string
		phase  types.EpochPhase
		expect bool
	}{
		{"validate phase", types.PoCValidatePhase, true},
		{"validate wind down", types.PoCValidateWindDownPhase, true},
		{"generate wind down", types.PoCGenerateWindDownPhase, true},
		{"generate phase", types.PoCGeneratePhase, false},
		{"inference phase", types.InferencePhase, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createCommitWorkerTestEpochState(tt.phase, 100, 50)
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
		lastCommitted: make(map[int64]commitState),
	}

	pocHeight := int64(100)

	// Get or create store and add an artifact
	artifactStore, err := store.GetOrCreateStore(pocHeight)
	assert.NoError(t, err)

	err = artifactStore.AddWithNode(1, []byte("test-vector"), "node-1")
	assert.NoError(t, err)
	err = artifactStore.Flush()
	assert.NoError(t, err)

	// First commit should submit
	mockRecorder.On("SubmitPoCV2StoreCommit", mock.AnythingOfType("*inference.MsgPoCV2StoreCommit")).Return(nil).Once()

	worker.maybeSubmitCommit(pocHeight)
	mockRecorder.AssertExpectations(t)

	// Second commit with same state should NOT submit
	worker.maybeSubmitCommit(pocHeight)
	mockRecorder.AssertExpectations(t) // No additional calls expected
}

func TestCommitWorker_StartAndStop(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "commit_worker_test")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store := artifacts.NewManagedArtifactStore(tmpDir, 5)
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	tracker := chainphase.NewChainPhaseTracker()

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

	_, err = store.GetOrCreateStore(100)
	assert.NoError(t, err)
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

func assertWeightSum(t *testing.T, weights []*inference.MLNodeWeight, expected uint32) {
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
		lastCommitted:      make(map[int64]commitState),
		currentPocHeight:   100,
	}

	worker.lastCommitted[100] = commitState{count: 50, rootHash: []byte("hash")}
	worker.lastDistributionAttempt = time.Now().Add(-time.Hour)

	epochState := createCommitWorkerTestEpochState(types.PoCGeneratePhase, 210, 200)
	epochState.PocV2Enabled = true

	worker.mu.Lock()
	pocHeight := GetCurrentPocStageHeight(epochState)
	if pocHeight > 0 && worker.currentPocHeight != pocHeight {
		worker.currentPocHeight = pocHeight
		worker.lastDistributionAttempt = time.Time{}
		worker.lastCommitted = make(map[int64]commitState)
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
