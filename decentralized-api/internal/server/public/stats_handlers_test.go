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
	modelStats         []statsstorage.ModelSummary
	summary            statsstorage.Summary
	devshardModelStats []statsstorage.ModelSummary
	devshardSummary    statsstorage.Summary
	devshardErr        error
	maxInferenceEpoch  uint64
	maxDevshardEpoch   uint64
}

func (m *mockStatsStorage) UpsertInference(_ context.Context, _ statsstorage.InferenceRecord) error {
	return nil
}

func (m *mockStatsStorage) UpsertDevshardEscrow(_ context.Context, _ statsstorage.DevshardEscrow, _ []statsstorage.DevshardModelAggregate) error {
	return nil
}

func (m *mockStatsStorage) UpdateInferenceStatus(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockStatsStorage) GetDevshardSummaryByDeveloperEpochsBackwards(_ context.Context, _ string, _ int32) (statsstorage.Summary, error) {
	return m.devshardSummary, m.devshardErr
}

func (m *mockStatsStorage) GetDevshardSummaryByEpochsBackwards(_ context.Context, _ int32) (statsstorage.Summary, error) {
	return m.devshardSummary, m.devshardErr
}

func (m *mockStatsStorage) GetDevshardSummaryByTimePeriod(_ context.Context, _, _ statsstorage.UnixMillis) (statsstorage.Summary, error) {
	return m.devshardSummary, m.devshardErr
}

func (m *mockStatsStorage) GetDevshardModelStatsByTime(_ context.Context, _, _ statsstorage.UnixMillis) ([]statsstorage.ModelSummary, error) {
	return m.devshardModelStats, m.devshardErr
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

func (m *mockStatsStorage) GetMaxInferenceEpoch(_ context.Context) (uint64, error) {
	return m.maxInferenceEpoch, nil
}

func (m *mockStatsStorage) GetMaxDevshardEpoch(_ context.Context) (uint64, error) {
	return m.maxDevshardEpoch, m.devshardErr
}

func (m *mockStatsStorage) GetSummaryByDeveloperEpochRange(_ context.Context, _ string, _, _ uint64) (statsstorage.Summary, error) {
	return m.summary, nil
}

func (m *mockStatsStorage) GetSummaryByEpochRange(_ context.Context, _, _ uint64) (statsstorage.Summary, error) {
	return m.summary, nil
}

func (m *mockStatsStorage) GetDevshardSummaryByDeveloperEpochRange(_ context.Context, _ string, _, _ uint64) (statsstorage.Summary, error) {
	return m.devshardSummary, m.devshardErr
}

func (m *mockStatsStorage) GetDevshardSummaryByEpochRange(_ context.Context, _, _ uint64) (statsstorage.Summary, error) {
	return m.devshardSummary, m.devshardErr
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

func TestGetStatsDevshardModels(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/devshard/models?time_from=1700000000000&time_to=1700000001000", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	s := &Server{
		e: e,
		statsStorage: &mockStatsStorage{
			devshardModelStats: []statsstorage.ModelSummary{
				{Model: "model-a", AiTokens: 111, Inferences: 2},
			},
		},
	}

	err := s.getStatsDevshardModels(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp DevshardStatsModelsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.DevshardStatsModels, 1)
	require.Equal(t, "model-a", resp.DevshardStatsModels[0].Model)
	require.Equal(t, int64(111), resp.DevshardStatsModels[0].DevshardAiTokens)
	require.Equal(t, int32(2), resp.DevshardStatsModels[0].DevshardInferences)
}

func TestGetStatsDevshardSummaryTime_BadTimeRange(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/devshard/summary/time?time_from=1700000001000&time_to=1700000000000", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	s := &Server{
		e:            e,
		statsStorage: &mockStatsStorage{},
	}

	err := s.getStatsDevshardSummaryTime(c)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestGetStatsDevshardSummaryEpochs_DisabledStorage(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/devshard/summary/epochs?epochs_n=5", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	s := &Server{
		e: e,
		statsStorage: &mockStatsStorage{
			devshardErr: statsstorage.ErrStatsDisabled,
		},
	}

	err := s.getStatsDevshardSummaryEpochs(c)
	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusServiceUnavailable, httpErr.Code)
}

func TestGetStatsDevshardDeveloperSummaryEpochs(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/v1/stats/devshard/developers/gonka1dev/summary/epochs?epochs_n=3", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("developer")
		c.SetParamValues("gonka1dev")

		s := &Server{
			e: e,
			statsStorage: &mockStatsStorage{
				devshardSummary: statsstorage.Summary{
					AiTokens:             1234,
					Inferences:           12,
					ActualInferencesCost: 77,
				},
			},
		}

		err := s.getStatsDevshardDeveloperSummaryEpochs(c)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)

		var resp DevshardStatsSummaryResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Equal(t, int64(1234), resp.DevshardAiTokens)
		require.Equal(t, int32(12), resp.DevshardInferences)
		require.Equal(t, int64(77), resp.DevshardActualInferencesCost)
	})

	t.Run("disabled storage", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/v1/stats/devshard/developers/gonka1dev/summary/epochs?epochs_n=3", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("developer")
		c.SetParamValues("gonka1dev")

		s := &Server{
			e: e,
			statsStorage: &mockStatsStorage{
				devshardErr: statsstorage.ErrStatsDisabled,
			},
		}

		err := s.getStatsDevshardDeveloperSummaryEpochs(c)
		require.Error(t, err)
		httpErr, ok := err.(*echo.HTTPError)
		require.True(t, ok)
		require.Equal(t, http.StatusServiceUnavailable, httpErr.Code)
	})
}

func TestGetStatsSummaryTime_IncludesDevshardAggregates(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/summary/time?time_from=1700000000000&time_to=1700000001000", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	s := &Server{
		e: e,
		statsStorage: &mockStatsStorage{
			summary: statsstorage.Summary{
				AiTokens:             100,
				Inferences:           5,
				ActualInferencesCost: 200,
			},
			devshardSummary: statsstorage.Summary{
				AiTokens:             50,
				Inferences:           2,
				ActualInferencesCost: 80,
			},
		},
	}

	err := s.getStatsSummaryTime(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp StatsSummaryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, int64(150), resp.AiTokens)
	require.Equal(t, int32(7), resp.Inferences)
	require.Equal(t, int64(280), resp.ActualInferencesCost)
}

func TestGetStatsSummaryEpochs_IncludesDevshardAggregates(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/summary/epochs?epochs_n=3", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	s := &Server{
		e: e,
		statsStorage: &mockStatsStorage{
			summary: statsstorage.Summary{
				AiTokens:             200,
				Inferences:           10,
				ActualInferencesCost: 300,
			},
			devshardSummary: statsstorage.Summary{
				AiTokens:             75,
				Inferences:           4,
				ActualInferencesCost: 90,
			},
		},
	}

	err := s.getStatsSummaryEpochs(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp StatsSummaryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, int64(275), resp.AiTokens)
	require.Equal(t, int32(14), resp.Inferences)
	require.Equal(t, int64(390), resp.ActualInferencesCost)
}

func TestGetStatsModels_IncludesDevshardAggregates(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/models?time_from=1700000000000&time_to=1700000001000", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	s := &Server{
		e: e,
		statsStorage: &mockStatsStorage{
			modelStats: []statsstorage.ModelSummary{
				{Model: "model-a", AiTokens: 100, Inferences: 4},
				{Model: "model-b", AiTokens: 25, Inferences: 1},
			},
			devshardModelStats: []statsstorage.ModelSummary{
				{Model: "model-a", AiTokens: 50, Inferences: 2},
				{Model: "model-c", AiTokens: 10, Inferences: 1},
			},
		},
	}

	err := s.getStatsModels(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp StatsModelsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.StatsModels, 3)
	require.Equal(t, "model-a", resp.StatsModels[0].Model)
	require.Equal(t, int64(150), resp.StatsModels[0].AiTokens)
	require.Equal(t, int32(6), resp.StatsModels[0].Inferences)
	require.Equal(t, "model-b", resp.StatsModels[1].Model)
	require.Equal(t, int64(25), resp.StatsModels[1].AiTokens)
	require.Equal(t, int32(1), resp.StatsModels[1].Inferences)
	require.Equal(t, "model-c", resp.StatsModels[2].Model)
	require.Equal(t, int64(10), resp.StatsModels[2].AiTokens)
	require.Equal(t, int32(1), resp.StatsModels[2].Inferences)
}

func TestGetStatsDeveloperSummaryEpochs_IncludesDevshardAggregates(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/developers/gonka1dev/summary/epochs?epochs_n=5", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("developer")
	c.SetParamValues("gonka1dev")

	s := &Server{
		e: e,
		statsStorage: &mockStatsStorage{
			summary: statsstorage.Summary{
				AiTokens:             300,
				Inferences:           9,
				ActualInferencesCost: 111,
			},
			devshardSummary: statsstorage.Summary{
				AiTokens:             60,
				Inferences:           3,
				ActualInferencesCost: 44,
			},
		},
	}

	err := s.getStatsDeveloperSummaryEpochs(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp StatsSummaryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, int64(360), resp.AiTokens)
	require.Equal(t, int32(12), resp.Inferences)
	require.Equal(t, int64(155), resp.ActualInferencesCost)
}

func TestGetStatsSummaryEpochs_AlignsCombinedEpochWindow_FileStorage(t *testing.T) {
	e := echo.New()
	store := statsstorage.NewFileStorage(t.TempDir())
	ctx := context.Background()

	require.NoError(t, store.UpsertInference(ctx, statsstorage.InferenceRecord{
		InferenceID:        "inf-epoch-100",
		RequestedBy:        "gonka1dev",
		Model:              "model-a",
		Status:             "FINISHED",
		EpochID:            100,
		TotalTokenCount:    100,
		ActualCostInCoins:  10,
		InferenceTimestamp: 1700000001000,
	}))
	require.NoError(t, store.UpsertDevshardEscrow(ctx, statsstorage.DevshardEscrow{
		EscrowID:            "esc-old-window",
		EpochID:             90,
		Participant:         "gonka1dev",
		TotalCostInCoins:    100,
		TotalInferences:     10,
		TotalTokenCount:     1000,
		SettlementTimestamp: 1700000002000,
	}, nil))
	require.NoError(t, store.UpsertDevshardEscrow(ctx, statsstorage.DevshardEscrow{
		EscrowID:            "esc-in-window",
		EpochID:             98,
		Participant:         "gonka1dev",
		TotalCostInCoins:    5,
		TotalInferences:     2,
		TotalTokenCount:     50,
		SettlementTimestamp: 1700000003000,
	}, nil))

	req := httptest.NewRequest(http.MethodGet, "/v1/stats/summary/epochs?epochs_n=5", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	s := &Server{e: e, statsStorage: store}

	err := s.getStatsSummaryEpochs(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp StatsSummaryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, int64(150), resp.AiTokens, "epoch 90 devshard data must be excluded from last 5 epochs anchored at global max 100")
	require.Equal(t, int32(3), resp.Inferences)
	require.Equal(t, int64(15), resp.ActualInferencesCost)
}

func TestGetStatsDeveloperSummaryEpochs_AlignsCombinedEpochWindow_FileStorage(t *testing.T) {
	e := echo.New()
	store := statsstorage.NewFileStorage(t.TempDir())
	ctx := context.Background()

	require.NoError(t, store.UpsertInference(ctx, statsstorage.InferenceRecord{
		InferenceID:        "inf-dev-epoch-100",
		RequestedBy:        "gonka1dev",
		Model:              "model-a",
		Status:             "FINISHED",
		EpochID:            100,
		TotalTokenCount:    120,
		ActualCostInCoins:  12,
		InferenceTimestamp: 1700000001000,
	}))
	require.NoError(t, store.UpsertDevshardEscrow(ctx, statsstorage.DevshardEscrow{
		EscrowID:            "esc-dev-old-window",
		EpochID:             90,
		Participant:         "gonka1dev",
		TotalCostInCoins:    70,
		TotalInferences:     7,
		TotalTokenCount:     700,
		SettlementTimestamp: 1700000002000,
	}, nil))
	require.NoError(t, store.UpsertDevshardEscrow(ctx, statsstorage.DevshardEscrow{
		EscrowID:            "esc-dev-in-window",
		EpochID:             99,
		Participant:         "gonka1dev",
		TotalCostInCoins:    9,
		TotalInferences:     1,
		TotalTokenCount:     90,
		SettlementTimestamp: 1700000003000,
	}, nil))

	req := httptest.NewRequest(http.MethodGet, "/v1/stats/developers/gonka1dev/summary/epochs?epochs_n=5", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("developer")
	c.SetParamValues("gonka1dev")
	s := &Server{e: e, statsStorage: store}

	err := s.getStatsDeveloperSummaryEpochs(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp StatsSummaryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, int64(210), resp.AiTokens, "developer summary should exclude epoch 90 devshard data")
	require.Equal(t, int32(2), resp.Inferences)
	require.Equal(t, int64(21), resp.ActualInferencesCost)
}
