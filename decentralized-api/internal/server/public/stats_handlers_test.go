package public

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"decentralized-api/statsstorage"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

type mockStatsStorage struct {
	modelStats []statsstorage.ModelSummary
	summary    statsstorage.Summary
}

func (m *mockStatsStorage) UpsertInference(_ context.Context, _ statsstorage.InferenceRecord) error {
	return nil
}

func (m *mockStatsStorage) UpdateInferenceStatus(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockStatsStorage) GetDeveloperInferencesByTime(_ context.Context, _ string, _, _ statsstorage.UnixMillis) ([]statsstorage.InferenceRecord, error) {
	return []statsstorage.InferenceRecord{}, nil
}

func (m *mockStatsStorage) GetSummaryByDeveloperEpochsBackwards(_ context.Context, _ string, _ int32) (statsstorage.Summary, error) {
	return m.summary, nil
}

func (m *mockStatsStorage) GetSummaryByEpochsBackwards(_ context.Context, _ int32) (statsstorage.Summary, error) {
	return m.summary, nil
}

func (m *mockStatsStorage) GetSummaryByTimePeriod(_ context.Context, _, _ statsstorage.UnixMillis) (statsstorage.Summary, error) {
	return m.summary, nil
}

func (m *mockStatsStorage) GetModelStatsByTime(_ context.Context, _, _ statsstorage.UnixMillis) ([]statsstorage.ModelSummary, error) {
	return m.modelStats, nil
}

func (m *mockStatsStorage) GetDebugStats(_ context.Context) (statsstorage.DebugStats, error) {
	return statsstorage.DebugStats{}, nil
}

func (m *mockStatsStorage) PruneOlderThan(_ context.Context, _ statsstorage.UnixMillis) error {
	return nil
}

func (m *mockStatsStorage) Close() {}

func TestGetStatsModels(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/models?time_from=1700000000000&time_to=1700000001000", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	s := &Server{
		e: e,
		statsStorage: &mockStatsStorage{
			modelStats: []statsstorage.ModelSummary{
				{Model: "model-a", AiTokens: 111, Inferences: 2},
			},
		},
	}

	err := s.getStatsModels(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp StatsModelsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.StatsModels, 1)
	require.Equal(t, "model-a", resp.StatsModels[0].Model)
	require.Equal(t, int64(111), resp.StatsModels[0].AiTokens)
	require.Equal(t, int32(2), resp.StatsModels[0].Inferences)
}

func TestGetStatsDeveloperInferences(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/developers/gonka1dev/inferences?time_from=1700000000000&time_to=1700000001000", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("developer")
	c.SetParamValues("gonka1dev")

	s := &Server{
		e:            e,
		statsStorage: &mockStatsStorage{},
	}

	err := s.getStatsDeveloperInferences(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestGetStatsSummaryTime(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/summary/time?time_from=1700000000000&time_to=1700000001000", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	s := &Server{
		e: e,
		statsStorage: &mockStatsStorage{
			summary: statsstorage.Summary{
				AiTokens:   100,
				Inferences: 5,
			},
		},
	}

	err := s.getStatsSummaryTime(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp StatsSummaryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, int64(100), resp.AiTokens)
	require.Equal(t, int32(5), resp.Inferences)
}

func TestGetStatsSummaryEpochs_RequiresEpochsN(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/summary/epochs", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	s := &Server{
		e: e,
		statsStorage: &mockStatsStorage{
			summary: statsstorage.Summary{
				AiTokens:             10,
				Inferences:           1,
				ActualInferencesCost: 20,
			},
		},
	}

	err := s.getStatsSummaryEpochs(c)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}
