package event_listener

import (
	"decentralized-api/chainphase"
	"github.com/productscience/inference/x/inference/types"
	"testing"
	"time"

	"decentralized-api/internal/event_listener/chainevents"

	"github.com/stretchr/testify/assert"
)

func TestOnNewBlockDispatcher_ShouldTriggerReconciliation(t *testing.T) {
	testCases := []struct {
		name            string
		blockInterval   int
		timeInterval    time.Duration
		lastBlockHeight int64
		lastTime        time.Time
		epochState      *chainphase.EpochState
		expectedResult  bool
		description     string
	}{
		{
			name:            "should trigger due to block interval",
			blockInterval:   5,
			timeInterval:    30 * time.Second,
			lastBlockHeight: 10,
			lastTime:        time.Now().Add(-10 * time.Second), // Recent time
			epochState: &chainphase.EpochState{
				CurrentPhase: types.InferencePhase,
				CurrentBlock: chainphase.BlockInfo{
					Height: 16, // 16 - 10 = 6 blocks, >= 5
				},
			},
			expectedResult: true,
			description:    "6 blocks since last reconciliation, should trigger",
		},
		{
			name:            "should not trigger - too few blocks and recent time",
			blockInterval:   5,
			timeInterval:    30 * time.Second,
			lastBlockHeight: 10,
			lastTime:        time.Now().Add(-10 * time.Second), // Recent time
			epochState: &chainphase.EpochState{
				CurrentBlock: chainphase.BlockInfo{
					Height: 13, // 13 - 10 = 3 blocks, < 5
				},
			},
			expectedResult: false,
			description:    "Only 3 blocks since last reconciliation and time is recent",
		},
		{
			name:            "should trigger due to time interval",
			blockInterval:   5,
			timeInterval:    30 * time.Second,
			lastBlockHeight: 10,
			lastTime:        time.Now().Add(-40 * time.Second), // Old time
			epochState: &chainphase.EpochState{
				CurrentPhase: types.InferencePhase,
				CurrentBlock: chainphase.BlockInfo{
					Height: 12, // Only 2 blocks
				},
			},
			expectedResult: true,
			description:    "Time interval exceeded (40s > 30s)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a fresh dispatcher for each test case
			dispatcher := &OnNewBlockDispatcher{
				reconciliationConfig: MlNodeReconciliationConfig{
					Inference: &MlNodeStageReconciliationConfig{
						BlockInterval: tc.blockInterval,
						TimeInterval:  tc.timeInterval,
					},
					PoC: &MlNodeStageReconciliationConfig{
						BlockInterval: tc.blockInterval,
						TimeInterval:  tc.timeInterval,
					},
					LastBlockHeight: tc.lastBlockHeight,
					LastTime:        tc.lastTime,
				},
			}

			result := dispatcher.shouldTriggerReconciliation(*tc.epochState)
			assert.Equal(t, tc.expectedResult, result, tc.description)
		})
	}
}

func TestParseNewBlockInfo(t *testing.T) {
	// This test shows how we can test the parsing logic independently
	// without needing a real blockchain event

	testData := map[string]interface{}{
		"block": map[string]interface{}{
			"header": map[string]interface{}{
				"height": "12345",
			},
		},
		"block_id": map[string]interface{}{
			"hash": "ABCDEF123456",
		},
	}

	mockEvent := &chainevents.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      "test",
		Result: chainevents.Result{
			Query: "tm.event='NewBlock'",
			Data: chainevents.Data{
				Type:  "tendermint/event/NewBlock",
				Value: testData,
			},
			Events: make(map[string][]string),
		},
	}

	blockInfo, err := parseNewBlockInfo(mockEvent)

	assert.NoError(t, err)
	assert.Equal(t, int64(12345), blockInfo.Height)
	assert.Equal(t, "ABCDEF123456", blockInfo.Hash)
}
