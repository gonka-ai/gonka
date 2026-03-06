package event_listener

import (
	"decentralized-api/statsstorage"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseInferenceFinishedRecords_Success(t *testing.T) {
	events := map[string][]string{
		"inference_finished.inference_id":           {"inf-1"},
		"inference_finished.requested_by":           {"gonka1dev"},
		"inference_finished.model":                  {"model-a"},
		"inference_finished.status":                 {"FINISHED"},
		"inference_finished.epoch_id":               {"42"},
		"inference_finished.prompt_token_count":     {"100"},
		"inference_finished.completion_token_count": {"50"},
		"inference_finished.actual_cost_in_coins":   {"12345"},
		"inference_finished.start_block_timestamp":  {"1700000000000"},
		"inference_finished.end_block_timestamp":    {"1700000001000"},
	}

	records, err := parseInferenceFinishedRecords(events)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "inf-1", records[0].InferenceID)
	require.Equal(t, "gonka1dev", records[0].RequestedBy)
	require.Equal(t, "model-a", records[0].Model)
	require.Equal(t, uint64(42), records[0].EpochID)
	require.Equal(t, uint64(150), records[0].TotalTokenCount)
	require.Equal(t, int64(12345), records[0].ActualCostInCoins)
	require.Equal(t, statsstorage.UnixMillis(1700000001000), records[0].InferenceTimestamp)
}

func TestParseInferenceFinishedRecords_MissingRequiredField(t *testing.T) {
	events := map[string][]string{
		"inference_finished.inference_id": {"inf-1"},
		// missing requested_by
		"inference_finished.model":                  {"model-a"},
		"inference_finished.status":                 {"FINISHED"},
		"inference_finished.epoch_id":               {"42"},
		"inference_finished.prompt_token_count":     {"100"},
		"inference_finished.completion_token_count": {"50"},
		"inference_finished.actual_cost_in_coins":   {"12345"},
		"inference_finished.start_block_timestamp":  {"1700000000000"},
		"inference_finished.end_block_timestamp":    {"1700000001000"},
	}

	_, err := parseInferenceFinishedRecords(events)
	require.Error(t, err)
}

func TestParseInferenceStatusUpdatedRecords_Success(t *testing.T) {
	events := map[string][]string{
		"inference_status_updated.inference_id": {"inf-1"},
		"inference_status_updated.status":       {"INVALIDATED"},
	}

	records, err := parseInferenceStatusUpdatedRecords(events)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "inf-1", records[0].InferenceID)
	require.Equal(t, "INVALIDATED", records[0].Status)
}

func TestParseInferenceStatusUpdatedRecords_MissingStatus(t *testing.T) {
	events := map[string][]string{
		"inference_status_updated.inference_id": {"inf-1"},
	}

	_, err := parseInferenceStatusUpdatedRecords(events)
	require.Error(t, err)
}
