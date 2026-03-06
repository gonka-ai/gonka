package public

import (
	"net/http"
	"strconv"
	"time"

	"decentralized-api/statsstorage"

	"github.com/labstack/echo/v4"
)

type StatsModelsResponse struct {
	StatsModels []StatsModel `json:"stats_models"`
}

type StatsModel struct {
	Model      string `json:"model"`
	AiTokens   int64  `json:"ai_tokens"`
	Inferences int32  `json:"inferences"`
}

type StatsSummaryResponse struct {
	AiTokens             int64 `json:"ai_tokens"`
	Inferences           int32 `json:"inferences"`
	ActualInferencesCost int64 `json:"actual_inferences_cost"`
}

type DeveloperInferencesResponse struct {
	Stats []DeveloperStatsByTimeDto `json:"stats"`
}

type DeveloperStatsByTimeDto struct {
	EpochID   uint64                  `json:"epoch_id"`
	Timestamp statsstorage.UnixMillis `json:"timestamp"`
	Inference InferenceStatsDto       `json:"inference"`
}

type InferenceStatsDto struct {
	InferenceID       string `json:"inference_id"`
	EpochID           uint64 `json:"epoch_id"`
	Status            string `json:"status"`
	TotalTokenCount   uint64 `json:"total_token_count"`
	Model             string `json:"model"`
	ActualCostInCoins int64  `json:"actual_cost_in_coins"`
}

type DebugStatsResponse struct {
	StatsByTime  []DebugTimeStatDto  `json:"stats_by_time"`
	StatsByEpoch []DebugEpochStatDto `json:"stats_by_epoch"`
}

type DebugTimeStatDto struct {
	Developer string                    `json:"developer"`
	Stats     []DeveloperStatsByTimeDto `json:"stats"`
}

type DebugEpochStatDto struct {
	Developer string                     `json:"developer"`
	Stats     []DeveloperStatsByEpochDto `json:"stats"`
}

type DeveloperStatsByEpochDto struct {
	EpochID      uint64   `json:"epoch_id"`
	InferenceIDs []string `json:"inference_ids"`
}

func (s *Server) getStatsModels(c echo.Context) error {
	if s.statsStorage == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "stats storage is not configured")
	}

	timeFrom, timeTo, err := parseStatsTimeRange(c.QueryParam("time_from"), c.QueryParam("time_to"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	modelStats, err := s.statsStorage.GetModelStatsByTime(c.Request().Context(), timeFrom, timeTo)
	if err != nil {
		return err
	}

	resp := StatsModelsResponse{
		StatsModels: make([]StatsModel, 0, len(modelStats)),
	}
	for _, stat := range modelStats {
		resp.StatsModels = append(resp.StatsModels, StatsModel{
			Model:      stat.Model,
			AiTokens:   stat.AiTokens,
			Inferences: stat.Inferences,
		})
	}

	return c.JSON(http.StatusOK, resp)
}

func (s *Server) getStatsDeveloperInferences(c echo.Context) error {
	if s.statsStorage == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "stats storage is not configured")
	}
	developer := c.Param("developer")
	if developer == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "developer is required")
	}

	timeFrom, timeTo, err := parseStatsTimeRange(c.QueryParam("time_from"), c.QueryParam("time_to"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	stats, err := s.statsStorage.GetDeveloperInferencesByTime(c.Request().Context(), developer, timeFrom, timeTo)
	if err != nil {
		return err
	}
	resp := DeveloperInferencesResponse{
		Stats: make([]DeveloperStatsByTimeDto, 0, len(stats)),
	}
	for _, stat := range stats {
		resp.Stats = append(resp.Stats, mapInferenceRecordToByTime(stat))
	}
	return c.JSON(http.StatusOK, resp)
}

func (s *Server) getStatsDeveloperSummaryEpochs(c echo.Context) error {
	if s.statsStorage == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "stats storage is not configured")
	}
	developer := c.Param("developer")
	if developer == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "developer is required")
	}
	epochsN, err := parseEpochsN(c.QueryParam("epochs_n"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	summary, err := s.statsStorage.GetSummaryByDeveloperEpochsBackwards(c.Request().Context(), developer, epochsN)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, mapSummary(summary))
}

func (s *Server) getStatsSummaryEpochs(c echo.Context) error {
	if s.statsStorage == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "stats storage is not configured")
	}
	epochsN, err := parseEpochsN(c.QueryParam("epochs_n"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	summary, err := s.statsStorage.GetSummaryByEpochsBackwards(c.Request().Context(), epochsN)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, mapSummary(summary))
}

func (s *Server) getStatsSummaryTime(c echo.Context) error {
	if s.statsStorage == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "stats storage is not configured")
	}
	timeFrom, timeTo, err := parseStatsTimeRange(c.QueryParam("time_from"), c.QueryParam("time_to"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	summary, err := s.statsStorage.GetSummaryByTimePeriod(c.Request().Context(), timeFrom, timeTo)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, mapSummary(summary))
}

func (s *Server) getStatsDebugDevelopers(c echo.Context) error {
	if s.statsStorage == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "stats storage is not configured")
	}
	debugStats, err := s.statsStorage.GetDebugStats(c.Request().Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, mapDebugStats(debugStats))
}

func parseStatsTimeRange(timeFromStr, timeToStr string) (statsstorage.UnixMillis, statsstorage.UnixMillis, error) {
	now := statsstorage.UnixMillis(time.Now().UnixMilli())

	var (
		timeFrom statsstorage.UnixMillis
		timeTo   statsstorage.UnixMillis
	)

	if timeToStr == "" {
		timeTo = now
	} else {
		parsed, err := strconv.ParseInt(timeToStr, 10, 64)
		if err != nil {
			return 0, 0, err
		}
		timeTo = statsstorage.UnixMillis(parsed)
	}

	if timeFromStr == "" {
		timeFrom = timeTo - statsstorage.UnixMillis(24*time.Hour.Milliseconds())
	} else {
		parsed, err := strconv.ParseInt(timeFromStr, 10, 64)
		if err != nil {
			return 0, 0, err
		}
		timeFrom = statsstorage.UnixMillis(parsed)
	}

	if timeTo < timeFrom {
		return 0, 0, echo.NewHTTPError(http.StatusBadRequest, "invalid time period: time_to must be >= time_from")
	}
	if timeTo < statsstorage.UnixMillisTimestampThreshold || timeFrom < statsstorage.UnixMillisTimestampThreshold {
		return 0, 0, echo.NewHTTPError(http.StatusBadRequest, "invalid time period: time_to and time_from must be in milliseconds")
	}
	return timeFrom, timeTo, nil
}

func parseEpochsN(raw string) (int32, error) {
	if raw == "" {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "epochs_n is required")
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, echo.NewHTTPError(http.StatusBadRequest, "epochs_n must be > 0")
	}
	return int32(n), nil
}

func mapSummary(s statsstorage.Summary) StatsSummaryResponse {
	return StatsSummaryResponse{
		AiTokens:             s.AiTokens,
		Inferences:           s.Inferences,
		ActualInferencesCost: s.ActualInferencesCost,
	}
}

func mapInferenceRecordToByTime(r statsstorage.InferenceRecord) DeveloperStatsByTimeDto {
	return DeveloperStatsByTimeDto{
		EpochID:   r.EpochID,
		Timestamp: r.InferenceTimestamp,
		Inference: InferenceStatsDto{
			InferenceID:       r.InferenceID,
			EpochID:           r.EpochID,
			Status:            r.Status,
			TotalTokenCount:   r.TotalTokenCount,
			Model:             r.Model,
			ActualCostInCoins: r.ActualCostInCoins,
		},
	}
}

func mapDebugStats(stats statsstorage.DebugStats) DebugStatsResponse {
	resp := DebugStatsResponse{
		StatsByTime:  make([]DebugTimeStatDto, 0, len(stats.StatsByTime)),
		StatsByEpoch: make([]DebugEpochStatDto, 0),
	}
	for _, byTime := range stats.StatsByTime {
		entry := DebugTimeStatDto{
			Developer: byTime.Developer,
			Stats:     make([]DeveloperStatsByTimeDto, 0, len(byTime.Stats)),
		}
		for _, stat := range byTime.Stats {
			entry.Stats = append(entry.Stats, mapInferenceRecordToByTime(stat))
		}
		resp.StatsByTime = append(resp.StatsByTime, entry)
	}

	byEpochGrouped := make(map[string][]DeveloperStatsByEpochDto)
	order := make([]string, 0)
	seen := make(map[string]struct{})
	for _, byEpoch := range stats.StatsByEpoch {
		if _, ok := seen[byEpoch.Developer]; !ok {
			seen[byEpoch.Developer] = struct{}{}
			order = append(order, byEpoch.Developer)
		}
		byEpochGrouped[byEpoch.Developer] = append(byEpochGrouped[byEpoch.Developer], DeveloperStatsByEpochDto{
			EpochID:      byEpoch.EpochID,
			InferenceIDs: byEpoch.InferenceIDs,
		})
	}
	for _, developer := range order {
		resp.StatsByEpoch = append(resp.StatsByEpoch, DebugEpochStatDto{
			Developer: developer,
			Stats:     byEpochGrouped[developer],
		})
	}

	return resp
}
