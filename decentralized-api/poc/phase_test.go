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
		name        string
		phase       types.EpochPhase
		blockHeight int64
		expect      bool
	}{
		{"generate phase accepts", types.PoCGeneratePhase, 110, true},
		{"wind down accepts in exchange window", types.PoCGenerateWindDownPhase, 190, true},
		{"wind down rejects after exchange window", types.PoCGenerateWindDownPhase, 260, false},
		{"validate phase rejects", types.PoCValidatePhase, 300, false},
		{"validate wind down rejects", types.PoCValidateWindDownPhase, 350, false},
		{"inference phase rejects", types.InferencePhase, 500, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createTestEpochState(tt.phase, tt.blockHeight, 100)
			result := ShouldAcceptGeneratedArtifacts(epochState)
			assert.Equal(t, tt.expect, result)
		})
	}
}

func TestShouldAcceptGeneratedArtifacts_ConfirmationPoC(t *testing.T) {
	tests := []struct {
		name        string
		eventPhase  types.ConfirmationPoCPhase
		blockHeight int64
		genStart    int64
		expect      bool
	}{
		{"generation accepts in window", types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION, 500, 450, true},
		{"generation rejects after window", types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION, 700, 450, false},
		{"validation rejects", types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION, 600, 450, false},
		{"grace period rejects", types.ConfirmationPoCPhase_CONFIRMATION_POC_GRACE_PERIOD, 440, 450, false},
		{"completed rejects", types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED, 800, 450, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createTestEpochState(types.InferencePhase, tt.blockHeight, 100)
			epochState.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
				TriggerHeight:         tt.genStart - 10,
				GenerationStartHeight: tt.genStart,
				Phase:                 tt.eventPhase,
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

func TestShouldStopValidationForStage(t *testing.T) {
	tests := []struct {
		name        string
		state       *chainphase.EpochState
		stageHeight int64
		expect      bool
	}{
		{
			name:        "nil state waits for next tracker update",
			state:       nil,
			stageHeight: 100,
			expect:      false,
		},
		{
			name: "transient not-synced state waits instead of cancelling",
			state: func() *chainphase.EpochState {
				s := createTestEpochState(types.PoCValidatePhase, 200, 100)
				s.IsSynced = false
				return s
			}(),
			stageHeight: 100,
			expect:      false,
		},
		{
			name:        "current validation stage continues",
			state:       createTestEpochState(types.PoCValidatePhase, 200, 100),
			stageHeight: 100,
			expect:      false,
		},
		{
			name:        "non-validation phase stops immediately",
			state:       createTestEpochState(types.InferencePhase, 500, 100),
			stageHeight: 100,
			expect:      true,
		},
		{
			name:        "different PoC stage stops immediately",
			state:       createTestEpochState(types.PoCValidatePhase, 200, 120),
			stageHeight: 100,
			expect:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expect, shouldStopValidationForStage(tt.state, tt.stageHeight))
		})
	}
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

func TestGetCurrentPocStageHeight_ConfirmationPoCChangesActiveStageHeight(t *testing.T) {
	st := createTestEpochState(types.InferencePhase, 500, 100)
	assert.Equal(t, int64(100), GetCurrentPocStageHeight(st))

	st.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
		TriggerHeight: 400,
		Phase:         types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
	}
	assert.Equal(t, int64(400), GetCurrentPocStageHeight(st))

	st.ActiveConfirmationPoCEvent.Phase = types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION
	assert.Equal(t, int64(400), GetCurrentPocStageHeight(st))
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
			name:           "accept last height before deadline",
			phase:          types.PoCGenerateWindDownPhase,
			blockHeight:    249,
			pocStartHeight: 100,
			expectAccept:   true,
		},
		{
			name:           "reject at committed deadline (next block would be late)",
			phase:          types.PoCGenerateWindDownPhase,
			blockHeight:    250,
			pocStartHeight: 100,
			expectAccept:   false,
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

func TestShouldAcceptStoreCommit_ConfirmationPoC(t *testing.T) {
	const (
		trigger      int64 = 450
		genStart     int64 = 450
		exchangeEnd  int64 = 599 // genStart + PocStageDuration - 1 + PocExchangeDuration
		regularStart int64 = 100
	)
	tests := []struct {
		name         string
		blockHeight  int64
		pocHeight    int64
		eventPhase   types.ConfirmationPoCPhase
		expectAccept bool
	}{
		{
			name:         "accept during generation window",
			blockHeight:  500,
			pocHeight:    trigger,
			eventPhase:   types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
			expectAccept: true,
		},
		{
			name:         "accept last height before exchange deadline",
			blockHeight:  exchangeEnd - 1,
			pocHeight:    trigger,
			eventPhase:   types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
			expectAccept: true,
		},
		{
			name:         "reject at committed exchange deadline",
			blockHeight:  exchangeEnd,
			pocHeight:    trigger,
			eventPhase:   types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
			expectAccept: false,
		},
		{
			name:         "reject after exchange deadline",
			blockHeight:  exchangeEnd + 1,
			pocHeight:    trigger,
			eventPhase:   types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
			expectAccept: false,
		},
		{
			name:         "reject before generation start",
			blockHeight:  genStart - 1,
			pocHeight:    trigger,
			eventPhase:   types.ConfirmationPoCPhase_CONFIRMATION_POC_GRACE_PERIOD,
			expectAccept: false,
		},
		{
			name:         "reject confirmation validation",
			blockHeight:  650,
			pocHeight:    trigger,
			eventPhase:   types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION,
			expectAccept: false,
		},
		{
			name:         "reject regular poc height while confirmation is active",
			blockHeight:  500,
			pocHeight:    regularStart,
			eventPhase:   types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
			expectAccept: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			epochState := createTestEpochState(types.InferencePhase, tt.blockHeight, regularStart)
			epochState.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
				TriggerHeight:         trigger,
				GenerationStartHeight: genStart,
				Phase:                 tt.eventPhase,
			}
			result := ShouldAcceptStoreCommit(epochState, tt.pocHeight)
			assert.Equal(t, tt.expectAccept, result)
		})
	}
}

func TestStoreCommitTimeoutHeight_RegularPoC(t *testing.T) {
	epochState := createTestEpochState(types.PoCGeneratePhase, 110, 100)
	assert.Equal(t, uint64(250), StoreCommitTimeoutHeight(epochState, 100))
	assert.Equal(t, uint64(0), StoreCommitTimeoutHeight(nil, 100))
}

func TestStoreCommitTimeoutHeight_ConfirmationPoC(t *testing.T) {
	const (
		trigger     int64 = 450
		genStart    int64 = 450
		exchangeEnd       = uint64(599)
	)
	epochState := createTestEpochState(types.InferencePhase, 500, 100)
	epochState.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
		TriggerHeight:         trigger,
		GenerationStartHeight: genStart,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
	}

	assert.Equal(t, exchangeEnd, StoreCommitTimeoutHeight(epochState, trigger),
		"confirmation timeout_height is GetExchangeEnd, not the regular PoC deadline")
	assert.Equal(t, uint64(250), StoreCommitTimeoutHeight(epochState, 100),
		"regular stage height still uses epoch PoCExchangeDeadline")
	assert.NotEqual(t, uint64(250), exchangeEnd)
}

func TestShouldHaveDistributedWeights_AllPhases(t *testing.T) {
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
			epochState := createTestEpochState(tt.phase, tt.blockHeight, 100)
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
