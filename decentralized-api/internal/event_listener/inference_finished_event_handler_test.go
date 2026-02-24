package event_listener

import (
	"database/sql"
	"testing"

	"decentralized-api/internal/event_listener/chainevents"

	_ "modernc.org/sqlite"
	"github.com/stretchr/testify/require"
)

func TestInferenceFinishedEventHandler_Handle_IgnoresStatsStoreFailure(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)

	store, err := NewInferenceFinishedStatsStore(db)
	require.NoError(t, err)
	require.NotNil(t, store)

	require.NoError(t, db.Close())

	event := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"inference_finished.inference_id":           {"inf-1"},
				"inference_finished.requested_by":           {"dev-1"},
				"inference_finished.executed_by":            {"exec-1"},
				"inference_finished.model":                  {"model-a"},
				"inference_finished.epoch_id":               {"11"},
				"inference_finished.prompt_token_count":     {"100"},
				"inference_finished.completion_token_count": {"40"},
				"inference_finished.actual_cost":            {"140000"},
				"inference_finished.end_block_timestamp":    {"1730000000000"},
			},
		},
	}

	handler := &InferenceFinishedEventHandler{}
	el := &EventListener{
		inferenceStatsStore: store,
	}

	err = handler.Handle(event, el)
	require.NoError(t, err)
}
