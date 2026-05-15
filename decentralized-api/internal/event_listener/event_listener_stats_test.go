package event_listener

import (
	"context"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/statsstorage"
	"testing"

	"github.com/stretchr/testify/require"
)

// capturingStatsStorage is a minimal mock that captures UpsertDevshardEscrow calls.
// All other methods are no-ops.
type capturingStatsStorage struct {
	onUpsert func(statsstorage.DevshardEscrow, []statsstorage.DevshardModelAggregate)
}

func (m *capturingStatsStorage) UpsertDevshardEscrow(_ context.Context, escrow statsstorage.DevshardEscrow, models []statsstorage.DevshardModelAggregate) error {
	if m.onUpsert != nil {
		m.onUpsert(escrow, models)
	}
	return nil
}
func (m *capturingStatsStorage) UpsertInference(_ context.Context, _ statsstorage.InferenceRecord) error {
	return nil
}
func (m *capturingStatsStorage) UpdateInferenceStatus(_ context.Context, _, _ string) error {
	return nil
}
func (m *capturingStatsStorage) GetDeveloperInferencesByTime(_ context.Context, _ string, _, _ statsstorage.UnixMillis) ([]statsstorage.InferenceRecord, error) {
	return nil, nil
}
func (m *capturingStatsStorage) GetMaxInferenceEpoch(_ context.Context) (uint64, error) {
	return 0, nil
}
func (m *capturingStatsStorage) GetMaxDevshardEpoch(_ context.Context) (uint64, error) {
	return 0, nil
}
func (m *capturingStatsStorage) GetSummaryByDeveloperEpochRange(_ context.Context, _ string, _, _ uint64) (statsstorage.Summary, error) {
	return statsstorage.Summary{}, nil
}
func (m *capturingStatsStorage) GetSummaryByEpochRange(_ context.Context, _, _ uint64) (statsstorage.Summary, error) {
	return statsstorage.Summary{}, nil
}
func (m *capturingStatsStorage) GetDevshardSummaryByDeveloperEpochRange(_ context.Context, _ string, _, _ uint64) (statsstorage.Summary, error) {
	return statsstorage.Summary{}, nil
}
func (m *capturingStatsStorage) GetDevshardSummaryByEpochRange(_ context.Context, _, _ uint64) (statsstorage.Summary, error) {
	return statsstorage.Summary{}, nil
}
func (m *capturingStatsStorage) GetDevshardSummaryByDeveloperEpochsBackwards(_ context.Context, _ string, _ int32) (statsstorage.Summary, error) {
	return statsstorage.Summary{}, nil
}
func (m *capturingStatsStorage) GetDevshardSummaryByEpochsBackwards(_ context.Context, _ int32) (statsstorage.Summary, error) {
	return statsstorage.Summary{}, nil
}
func (m *capturingStatsStorage) GetDevshardSummaryByTimePeriod(_ context.Context, _, _ statsstorage.UnixMillis) (statsstorage.Summary, error) {
	return statsstorage.Summary{}, nil
}
func (m *capturingStatsStorage) GetDevshardModelStatsByTime(_ context.Context, _, _ statsstorage.UnixMillis) ([]statsstorage.ModelSummary, error) {
	return nil, nil
}
func (m *capturingStatsStorage) GetSummaryByDeveloperEpochsBackwards(_ context.Context, _ string, _ int32) (statsstorage.Summary, error) {
	return statsstorage.Summary{}, nil
}
func (m *capturingStatsStorage) GetSummaryByEpochsBackwards(_ context.Context, _ int32) (statsstorage.Summary, error) {
	return statsstorage.Summary{}, nil
}
func (m *capturingStatsStorage) GetSummaryByTimePeriod(_ context.Context, _, _ statsstorage.UnixMillis) (statsstorage.Summary, error) {
	return statsstorage.Summary{}, nil
}
func (m *capturingStatsStorage) GetModelStatsByTime(_ context.Context, _, _ statsstorage.UnixMillis) ([]statsstorage.ModelSummary, error) {
	return nil, nil
}
func (m *capturingStatsStorage) GetDebugStats(_ context.Context) (statsstorage.DebugStats, error) {
	return statsstorage.DebugStats{}, nil
}
func (m *capturingStatsStorage) PruneOlderThan(_ context.Context, _ statsstorage.UnixMillis) error {
	return nil
}
func (m *capturingStatsStorage) Close() {}

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

func TestParseInferenceFinishedRecords_ZeroTimestamps(t *testing.T) {
	events := map[string][]string{
		"inference_finished.inference_id":           {"inf-1"},
		"inference_finished.requested_by":           {"gonka1dev"},
		"inference_finished.model":                  {"model-a"},
		"inference_finished.status":                 {"FINISHED"},
		"inference_finished.epoch_id":               {"42"},
		"inference_finished.prompt_token_count":     {"100"},
		"inference_finished.completion_token_count": {"50"},
		"inference_finished.actual_cost_in_coins":   {"12345"},
		"inference_finished.start_block_timestamp":  {"0"},
		"inference_finished.end_block_timestamp":    {"1700000001000"},
	}

	records, err := parseInferenceFinishedRecords(events)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, statsstorage.UnixMillis(0), records[0].StartBlockTimestamp)
	require.Equal(t, statsstorage.UnixMillis(1700000001000), records[0].EndBlockTimestamp)
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

func TestParseDevshardEscrowSettledRecords_Success(t *testing.T) {
	// model_stats is a JSON object keyed by model name (matches chain emission format)
	modelStats := `{"gpt-4":{"prompt_tokens":1000,"completion_tokens":2000,"inference_count":10,"cost":500}}`

	events := map[string][]string{
		"devshard_escrow_settled.escrow_id":              {"1001"},
		"devshard_escrow_settled.epoch_index":            {"42"},
		"devshard_escrow_settled.creator":                {"gonka1creator"},
		"devshard_escrow_settled.prompt_token_count":     {"1000"},
		"devshard_escrow_settled.completion_token_count": {"2000"},
		"devshard_escrow_settled.total_token_count":      {"3000"},
		"devshard_escrow_settled.inference_count":        {"10"},
		"devshard_escrow_settled.model_stats":            {modelStats},
	}

	records, parseErrors := parseDevshardEscrowSettledRecords(events)
	require.Empty(t, parseErrors)
	require.Len(t, records, 1)

	r := records[0]
	require.Equal(t, "1001", r.Escrow.EscrowID)
	require.Equal(t, uint64(42), r.Escrow.EpochID)
	require.Equal(t, "gonka1creator", r.Escrow.Participant)
	require.Equal(t, uint64(1000), r.Escrow.TotalPromptTokenCount)
	require.Equal(t, uint64(2000), r.Escrow.TotalCompletionTokenCount)
	require.Equal(t, uint64(3000), r.Escrow.TotalTokenCount)
	require.Equal(t, int32(10), r.Escrow.TotalInferences)
	require.Equal(t, int64(500), r.Escrow.TotalCostInCoins)

	require.Len(t, r.ModelStats, 1)
	require.Equal(t, "gpt-4", r.ModelStats[0].Model)
	require.Equal(t, uint64(1000), r.ModelStats[0].PromptTokenCount)
	require.Equal(t, uint64(2000), r.ModelStats[0].CompletionTokenCount)
	require.Equal(t, uint64(3000), r.ModelStats[0].TotalTokenCount)
}

func TestDevshardEscrowSettledHandler_ChainDerivedTimestamp(t *testing.T) {
	handler := &DevshardEscrowSettledEventHandler{}

	modelStats := `{"gpt-4":{"prompt_tokens":100,"completion_tokens":200,"inference_count":5,"cost":50}}`
	chainTimestampMs := "1700000000000"

	event := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Data: chainevents.Data{Type: "tendermint/event/Tx"},
			Events: map[string][]string{
				"devshard_escrow_settled.escrow_id":               {"9001"},
				"devshard_escrow_settled.epoch_index":             {"10"},
				"devshard_escrow_settled.creator":                 {"gonka1test"},
				"devshard_escrow_settled.prompt_token_count":      {"100"},
				"devshard_escrow_settled.completion_token_count":  {"200"},
				"devshard_escrow_settled.total_token_count":       {"300"},
				"devshard_escrow_settled.inference_count":         {"5"},
				"devshard_escrow_settled.model_stats":             {modelStats},
				"devshard_escrow_settled.settlement_timestamp_ms": {chainTimestampMs},
				"tx.height": {"12345"},
			},
		},
	}

	var captured *statsstorage.DevshardEscrow
	mockStorage := &capturingStatsStorage{
		onUpsert: func(escrow statsstorage.DevshardEscrow, _ []statsstorage.DevshardModelAggregate) {
			captured = &escrow
		},
	}

	el := &EventListener{statsStorage: mockStorage}
	err := handler.Handle(event, el)
	require.NoError(t, err)
	require.NotNil(t, captured)
	require.Equal(t, statsstorage.UnixMillis(1700000000000), captured.SettlementTimestamp)
}

func TestParseDevshardEscrowSettledRecords_MissingEpochIndex_Ignored(t *testing.T) {
	events := map[string][]string{
		"devshard_escrow_settled.escrow_id": {"1002"},
		"devshard_escrow_settled.creator":   {"gonka1creator"},
		// missing epoch_index — treated as legacy event, silently skipped
	}

	records, parseErrors := parseDevshardEscrowSettledRecords(events)
	require.Empty(t, parseErrors)
	require.Len(t, records, 0)
}

func TestParseDevshardEscrowSettledRecords_MalformedEpochIndex_Error(t *testing.T) {
	events := map[string][]string{
		"devshard_escrow_settled.escrow_id":   {"1010"},
		"devshard_escrow_settled.epoch_index": {"abc"},
		"devshard_escrow_settled.creator":     {"gonka1creator"},
	}

	records, parseErrors := parseDevshardEscrowSettledRecords(events)
	require.Len(t, records, 0)
	require.Len(t, parseErrors, 1)
	require.Contains(t, parseErrors[0].Error(), "parse epoch_index")
}

func TestParseDevshardEscrowSettledRecords_MissingRequiredFields(t *testing.T) {
	events := map[string][]string{
		"devshard_escrow_settled.escrow_id":   {"1003"},
		"devshard_escrow_settled.epoch_index": {"42"},
		// missing creator
		"devshard_escrow_settled.prompt_token_count":     {"1000"},
		"devshard_escrow_settled.completion_token_count": {"2000"},
		"devshard_escrow_settled.total_token_count":      {"3000"},
		"devshard_escrow_settled.inference_count":        {"10"},
		"devshard_escrow_settled.model_stats":            {`{}`},
	}

	records, parseErrors := parseDevshardEscrowSettledRecords(events)
	require.Len(t, records, 0)
	require.Len(t, parseErrors, 1)
	require.Contains(t, parseErrors[0].Error(), "missing creator")
}

func TestParseDevshardEscrowSettledRecords_PartialBatchSuccess(t *testing.T) {
	modelStats := `{"gpt-4":{"prompt_tokens":100,"completion_tokens":200,"inference_count":5,"cost":50}}`
	events := map[string][]string{
		"devshard_escrow_settled.escrow_id":              {"2001", "2002"},
		"devshard_escrow_settled.epoch_index":            {"42"}, // only one value — second record will fail
		"devshard_escrow_settled.creator":                {"gonka1a", "gonka1b"},
		"devshard_escrow_settled.prompt_token_count":     {"100", "100"},
		"devshard_escrow_settled.completion_token_count": {"200", "200"},
		"devshard_escrow_settled.total_token_count":      {"300", "300"},
		"devshard_escrow_settled.inference_count":        {"5", "5"},
		"devshard_escrow_settled.model_stats":            {modelStats, modelStats},
	}

	records, parseErrors := parseDevshardEscrowSettledRecords(events)
	require.Len(t, records, 1)
	require.Equal(t, "2001", records[0].Escrow.EscrowID)
	require.Len(t, parseErrors, 1)
	require.Contains(t, parseErrors[0].Error(), "escrow 2002")
}

func TestParseDevshardEscrowSettledRecords_InferenceCountOverflow(t *testing.T) {
	modelStats := `{"gpt-4":{"prompt_tokens":100,"completion_tokens":200,"inference_count":5,"cost":50}}`
	events := map[string][]string{
		"devshard_escrow_settled.escrow_id":              {"3001"},
		"devshard_escrow_settled.epoch_index":            {"42"},
		"devshard_escrow_settled.creator":                {"gonka1creator"},
		"devshard_escrow_settled.prompt_token_count":     {"100"},
		"devshard_escrow_settled.completion_token_count": {"200"},
		"devshard_escrow_settled.total_token_count":      {"300"},
		"devshard_escrow_settled.inference_count":        {"3000000000"},
		"devshard_escrow_settled.model_stats":            {modelStats},
	}

	records, parseErrors := parseDevshardEscrowSettledRecords(events)
	require.Len(t, records, 0)
	require.Len(t, parseErrors, 1)
	require.Contains(t, parseErrors[0].Error(), "overflows int32 range")
}

func TestParseDevshardEscrowSettledRecords_EmptyModelStats(t *testing.T) {
	events := map[string][]string{
		"devshard_escrow_settled.escrow_id":              {"5001"},
		"devshard_escrow_settled.epoch_index":            {"42"},
		"devshard_escrow_settled.creator":                {"gonka1creator"},
		"devshard_escrow_settled.prompt_token_count":     {"0"},
		"devshard_escrow_settled.completion_token_count": {"0"},
		"devshard_escrow_settled.total_token_count":      {"0"},
		"devshard_escrow_settled.inference_count":        {"0"},
		"devshard_escrow_settled.model_stats":            {"{}"},
	}

	records, parseErrors := parseDevshardEscrowSettledRecords(events)
	require.Empty(t, parseErrors)
	require.Len(t, records, 1)
	require.Equal(t, int64(0), records[0].Escrow.TotalCostInCoins)
	require.Empty(t, records[0].ModelStats)
}

func TestParseDevshardEscrowSettledRecords_MultiModel(t *testing.T) {
	modelStats := `{"gpt-4":{"prompt_tokens":600,"completion_tokens":1200,"inference_count":6,"cost":300},"llama-3":{"prompt_tokens":400,"completion_tokens":800,"inference_count":4,"cost":200}}`
	events := map[string][]string{
		"devshard_escrow_settled.escrow_id":              {"6001"},
		"devshard_escrow_settled.epoch_index":            {"42"},
		"devshard_escrow_settled.creator":                {"gonka1creator"},
		"devshard_escrow_settled.prompt_token_count":     {"1000"},
		"devshard_escrow_settled.completion_token_count": {"2000"},
		"devshard_escrow_settled.total_token_count":      {"3000"},
		"devshard_escrow_settled.inference_count":        {"10"},
		"devshard_escrow_settled.model_stats":            {modelStats},
	}

	records, parseErrors := parseDevshardEscrowSettledRecords(events)
	require.Empty(t, parseErrors)
	require.Len(t, records, 1)
	require.Len(t, records[0].ModelStats, 2)
	require.Equal(t, int64(500), records[0].Escrow.TotalCostInCoins)
}

func TestDevshardEscrowSettledHandler_CanHandle(t *testing.T) {
	handler := &DevshardEscrowSettledEventHandler{}

	devshardEvent := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"devshard_escrow_settled.escrow_id": {"1"},
			},
		},
	}
	require.True(t, handler.CanHandle(devshardEvent))

	inferenceEvent := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
				"inference_finished.inference_id": {"inf-1"},
			},
		},
	}
	require.False(t, handler.CanHandle(inferenceEvent))
}

func TestHandlerDoesNotInterfereWithInferenceEvents(t *testing.T) {
	inferenceEvent := &chainevents.JSONRPCResponse{
		Result: chainevents.Result{
			Events: map[string][]string{
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
			},
		},
	}

	devshardHandler := &DevshardEscrowSettledEventHandler{}
	inferenceHandler := &InferenceFinishedEventHandler{}

	require.False(t, devshardHandler.CanHandle(inferenceEvent))
	require.True(t, inferenceHandler.CanHandle(inferenceEvent))
}
