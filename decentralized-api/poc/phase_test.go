package poc

import (
	"testing"

	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

// createTestEpochState creates a test epoch state for phase predicate tests.
func createTestEpochState(phase types.EpochPhase, blockHeight, pocStartHeight int64) *chainphase.EpochState {
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

func TestShouldAcceptGeneratedArtifacts_RegularPoC(t *testing.T) {
	tests := []struct {
		name   string
		phase  types.EpochPhase
		expect bool
	}{
		{"generate phase accepts", types.PoCGeneratePhase, true},
		{"generate wind down rejects", types.PoCGenerateWindDownPhase, false},
		{"validate phase rejects", types.PoCValidatePhase, false},
		{"validate wind down rejects", types.PoCValidateWindDownPhase, false},
		{"inference phase rejects", types.InferencePhase, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createTestEpochState(tt.phase, 110, 100)
			result := ShouldAcceptGeneratedArtifacts(epochState)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestShouldAcceptGeneratedArtifacts_ConfirmationPoC(t *testing.T) {
	tests := []struct {
		name       string
		eventPhase types.ConfirmationPoCPhase
		expect     bool
	}{
		{"confirmation generation accepts", types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION, true},
		{"confirmation validation rejects", types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION, false},
		{"confirmation grace period rejects", types.ConfirmationPoCPhase_CONFIRMATION_POC_GRACE_PERIOD, false},
		{"confirmation completed rejects", types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createTestEpochState(types.InferencePhase, 500, 100)
			epochState.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
				TriggerHeight: 450,
				Phase:         tt.eventPhase,
			}
			result := ShouldAcceptGeneratedArtifacts(epochState)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestShouldAcceptGeneratedArtifacts_NilOrNotSynced(t *testing.T) {
	// Nil state
	var nilState *chainphase.EpochState
	assert.False(t, ShouldAcceptGeneratedArtifacts(nilState))

	// Not synced
	notSynced := createTestEpochState(types.PoCGeneratePhase, 110, 100)
	notSynced.IsSynced = false
	assert.False(t, ShouldAcceptGeneratedArtifacts(notSynced))
}

func TestShouldAcceptValidatedArtifacts_RegularPoC(t *testing.T) {
	tests := []struct {
		name   string
		phase  types.EpochPhase
		expect bool
	}{
		{"validate phase accepts", types.PoCValidatePhase, true},
		{"validate wind down accepts", types.PoCValidateWindDownPhase, true},
		{"generate phase rejects", types.PoCGeneratePhase, false},
		{"generate wind down rejects", types.PoCGenerateWindDownPhase, false},
		{"inference phase rejects", types.InferencePhase, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createTestEpochState(tt.phase, 200, 100)
			result := ShouldAcceptValidatedArtifacts(epochState)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestShouldAcceptValidatedArtifacts_ConfirmationPoC(t *testing.T) {
	tests := []struct {
		name       string
		eventPhase types.ConfirmationPoCPhase
		expect     bool
	}{
		{"confirmation validation accepts", types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION, true},
		{"confirmation generation rejects", types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION, false},
		{"confirmation grace period rejects", types.ConfirmationPoCPhase_CONFIRMATION_POC_GRACE_PERIOD, false},
		{"confirmation completed rejects", types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createTestEpochState(types.InferencePhase, 600, 100)
			epochState.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
				TriggerHeight: 450,
				Phase:         tt.eventPhase,
			}
			result := ShouldAcceptValidatedArtifacts(epochState)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestShouldAcceptValidatedArtifacts_NilOrNotSynced(t *testing.T) {
	// Nil state
	var nilState *chainphase.EpochState
	assert.False(t, ShouldAcceptValidatedArtifacts(nilState))

	// Not synced
	notSynced := createTestEpochState(types.PoCValidatePhase, 200, 100)
	notSynced.IsSynced = false
	assert.False(t, ShouldAcceptValidatedArtifacts(notSynced))
}

func TestGetCurrentPocStageHeight_RegularPoC(t *testing.T) {
	tests := []struct {
		name           string
		phase          types.EpochPhase
		pocStartHeight int64
	}{
		{"generate phase", types.PoCGeneratePhase, 100},
		{"validate phase", types.PoCValidatePhase, 200},
		{"inference phase", types.InferencePhase, 300},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createTestEpochState(tt.phase, 500, tt.pocStartHeight)
			height := GetCurrentPocStageHeight(epochState)
			assert.Equal(t, tt.pocStartHeight, height)
		})
	}
}

func TestGetCurrentPocStageHeight_ConfirmationPoC(t *testing.T) {
	epochState := createTestEpochState(types.InferencePhase, 500, 100)
	epochState.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
		TriggerHeight: 450,
		Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
	}

	height := GetCurrentPocStageHeight(epochState)
	assert.Equal(t, int64(450), height)
}

func TestGetCurrentPocStageHeight_NilOrNotSynced(t *testing.T) {
	// Nil state
	var nilState *chainphase.EpochState
	assert.Equal(t, int64(0), GetCurrentPocStageHeight(nilState))

	// Not synced
	notSynced := createTestEpochState(types.PoCGeneratePhase, 110, 100)
	notSynced.IsSynced = false
	assert.Equal(t, int64(0), GetCurrentPocStageHeight(notSynced))
}

func TestShouldAcceptStoreCommit_RegularPoC(t *testing.T) {
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
			epochState := createTestEpochState(tt.phase, tt.blockHeight, tt.pocStartHeight)
			result := ShouldAcceptStoreCommit(epochState, tt.pocStartHeight)
			assert.Equal(t, tt.expectAccept, result)
		})
	}
}

func TestShouldAcceptStoreCommit_WrongPocHeight(t *testing.T) {
	epochState := createTestEpochState(types.PoCGeneratePhase, 110, 100)
	// Pass wrong poc height - should reject
	result := ShouldAcceptStoreCommit(epochState, 999)
	assert.False(t, result)
}

func TestShouldAcceptStoreCommit_NilOrNotSynced(t *testing.T) {
	// Nil state
	var nilState *chainphase.EpochState
	assert.False(t, ShouldAcceptStoreCommit(nilState, 100))

	// Not synced
	notSynced := createTestEpochState(types.PoCGeneratePhase, 110, 100)
	notSynced.IsSynced = false
	assert.False(t, ShouldAcceptStoreCommit(notSynced, 100))
}

func TestShouldHaveDistributedWeights_AllPhases(t *testing.T) {
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
			epochState := createTestEpochState(tt.phase, 100, 50)
			result := ShouldHaveDistributedWeights(epochState)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestShouldHaveDistributedWeights_ConfirmationPoC(t *testing.T) {
	tests := []struct {
		name       string
		eventPhase types.ConfirmationPoCPhase
		expect     bool
	}{
		{"confirmation validation accepts", types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION, true},
		{"confirmation generation rejects", types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION, false},
		{"confirmation grace period rejects", types.ConfirmationPoCPhase_CONFIRMATION_POC_GRACE_PERIOD, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createTestEpochState(types.InferencePhase, 600, 100)
			epochState.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
				TriggerHeight: 450,
				Phase:         tt.eventPhase,
			}
			result := ShouldHaveDistributedWeights(epochState)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestShouldHaveDistributedWeights_NilOrNotSynced(t *testing.T) {
	// Nil state
	var nilState *chainphase.EpochState
	assert.False(t, ShouldHaveDistributedWeights(nilState))

	// Not synced
	notSynced := createTestEpochState(types.PoCValidatePhase, 200, 100)
	notSynced.IsSynced = false
	assert.False(t, ShouldHaveDistributedWeights(notSynced))
}
