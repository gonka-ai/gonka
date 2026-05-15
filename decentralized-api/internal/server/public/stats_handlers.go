package public

import (
	"context"
	"errors"
	"net/http"
	"sort"
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

type DevshardStatsSummaryResponse struct {
	DevshardAiTokens             int64 `json:"devshard_ai_tokens"`
	DevshardInferences           int32 `json:"devshard_inferences"`
	DevshardActualInferencesCost int64 `json:"devshard_actual_inferences_cost"`
}

type DevshardStatsModelsResponse struct {
	DevshardStatsModels []DevshardStatsModel `json:"devshard_stats_models"`
}

type DevshardStatsModel struct {
	Model              string `json:"model"`
	DevshardAiTokens   int64  `json:"devshard_ai_tokens"`
	DevshardInferences int32  `json:"devshard_inferences"`
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
	devshardModelStats, err := s.statsStorage.GetDevshardModelStatsByTime(c.Request().Context(), timeFrom, timeTo)
	if err != nil && !errors.Is(err, statsstorage.ErrStatsDisabled) {
		return err
	}
	combinedModelStats := mergeModelStats(modelStats, devshardModelStats)

	resp := StatsModelsResponse{
		StatsModels: make([]StatsModel, 0, len(combinedModelStats)),
	}
	for _, stat := range combinedModelStats {
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
	maxEpoch, err := s.getCombinedMaxEpoch(c.Request().Context())
	if err != nil {
		return err
	}
	minEpochExclusive := windowMinEpochExclusiveForAPI(maxEpoch, epochsN)
	summary, err := s.statsStorage.GetSummaryByDeveloperEpochRange(c.Request().Context(), developer, minEpochExclusive, maxEpoch)
	if err != nil {
		return err
	}
	devshardSummary, err := s.statsStorage.GetDevshardSummaryByDeveloperEpochRange(c.Request().Context(), developer, minEpochExclusive, maxEpoch)
	if err != nil && !errors.Is(err, statsstorage.ErrStatsDisabled) {
		return err
	}
	return c.JSON(http.StatusOK, mapSummary(mergeSummary(summary, devshardSummary)))
}

func (s *Server) getStatsSummaryEpochs(c echo.Context) error {
	if s.statsStorage == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "stats storage is not configured")
	}
	epochsN, err := parseEpochsN(c.QueryParam("epochs_n"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	maxEpoch, err := s.getCombinedMaxEpoch(c.Request().Context())
	if err != nil {
		return err
	}
	minEpochExclusive := windowMinEpochExclusiveForAPI(maxEpoch, epochsN)
	summary, err := s.statsStorage.GetSummaryByEpochRange(c.Request().Context(), minEpochExclusive, maxEpoch)
	if err != nil {
		return err
	}
	devshardSummary, err := s.statsStorage.GetDevshardSummaryByEpochRange(c.Request().Context(), minEpochExclusive, maxEpoch)
	if err != nil && !errors.Is(err, statsstorage.ErrStatsDisabled) {
		return err
	}
	return c.JSON(http.StatusOK, mapSummary(mergeSummary(summary, devshardSummary)))
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
	devshardSummary, err := s.statsStorage.GetDevshardSummaryByTimePeriod(c.Request().Context(), timeFrom, timeTo)
	if err != nil && !errors.Is(err, statsstorage.ErrStatsDisabled) {
		return err
	}
	return c.JSON(http.StatusOK, mapSummary(mergeSummary(summary, devshardSummary)))
}

func (s *Server) getStatsDevshardModels(c echo.Context) error {
	if s.statsStorage == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "stats storage is not configured")
	}

	timeFrom, timeTo, err := parseStatsTimeRange(c.QueryParam("time_from"), c.QueryParam("time_to"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}

	modelStats, err := s.statsStorage.GetDevshardModelStatsByTime(c.Request().Context(), timeFrom, timeTo)
	if err != nil {
		if errors.Is(err, statsstorage.ErrStatsDisabled) {
			return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
		}
		return err
	}

	resp := DevshardStatsModelsResponse{
		DevshardStatsModels: make([]DevshardStatsModel, 0, len(modelStats)),
	}
	for _, stat := range modelStats {
		resp.DevshardStatsModels = append(resp.DevshardStatsModels, mapDevshardModelSummary(stat))
	}
	return c.JSON(http.StatusOK, resp)
}

func (s *Server) getStatsDevshardDeveloperSummaryEpochs(c echo.Context) error {
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
	summary, err := s.statsStorage.GetDevshardSummaryByDeveloperEpochsBackwards(c.Request().Context(), developer, epochsN)
	if err != nil {
		if errors.Is(err, statsstorage.ErrStatsDisabled) {
			return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
		}
		return err
	}
	return c.JSON(http.StatusOK, mapDevshardSummary(summary))
}

func (s *Server) getStatsDevshardSummaryEpochs(c echo.Context) error {
	if s.statsStorage == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "stats storage is not configured")
	}
	epochsN, err := parseEpochsN(c.QueryParam("epochs_n"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	summary, err := s.statsStorage.GetDevshardSummaryByEpochsBackwards(c.Request().Context(), epochsN)
	if err != nil {
		if errors.Is(err, statsstorage.ErrStatsDisabled) {
			return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
		}
		return err
	}
	return c.JSON(http.StatusOK, mapDevshardSummary(summary))
}

func (s *Server) getStatsDevshardSummaryTime(c echo.Context) error {
	if s.statsStorage == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "stats storage is not configured")
	}
	timeFrom, timeTo, err := parseStatsTimeRange(c.QueryParam("time_from"), c.QueryParam("time_to"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	summary, err := s.statsStorage.GetDevshardSummaryByTimePeriod(c.Request().Context(), timeFrom, timeTo)
	if err != nil {
		if errors.Is(err, statsstorage.ErrStatsDisabled) {
			return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
		}
		return err
	}
	return c.JSON(http.StatusOK, mapDevshardSummary(summary))
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

func mapDevshardSummary(s statsstorage.Summary) DevshardStatsSummaryResponse {
	return DevshardStatsSummaryResponse{
		DevshardAiTokens:             s.AiTokens,
		DevshardInferences:           s.Inferences,
		DevshardActualInferencesCost: s.ActualInferencesCost,
	}
}

func mapDevshardModelSummary(s statsstorage.ModelSummary) DevshardStatsModel {
	return DevshardStatsModel{
		Model:              s.Model,
		DevshardAiTokens:   s.AiTokens,
		DevshardInferences: s.Inferences,
	}
}

func mergeSummary(base, addon statsstorage.Summary) statsstorage.Summary {
	return statsstorage.Summary{
		AiTokens:             base.AiTokens + addon.AiTokens,
		Inferences:           base.Inferences + addon.Inferences,
		ActualInferencesCost: base.ActualInferencesCost + addon.ActualInferencesCost,
	}
}

func mergeModelStats(base, addon []statsstorage.ModelSummary) []statsstorage.ModelSummary {
	combined := make(map[string]statsstorage.ModelSummary, len(base)+len(addon))
	for _, stat := range base {
		combined[stat.Model] = stat
	}
	for _, stat := range addon {
		existing := combined[stat.Model]
		existing.Model = stat.Model
		existing.AiTokens += stat.AiTokens
		existing.Inferences += stat.Inferences
		combined[stat.Model] = existing
	}
	models := make([]string, 0, len(combined))
	for model := range combined {
		models = append(models, model)
	}
	sort.Strings(models)
	result := make([]statsstorage.ModelSummary, 0, len(models))
	for _, model := range models {
		result = append(result, combined[model])
	}
	return result
}

func (s *Server) getCombinedMaxEpoch(ctx context.Context) (uint64, error) {
	maxInferenceEpoch, err := s.statsStorage.GetMaxInferenceEpoch(ctx)
	if err != nil {
		return 0, err
	}
	maxDevshardEpoch, err := s.statsStorage.GetMaxDevshardEpoch(ctx)
	if err != nil && !errors.Is(err, statsstorage.ErrStatsDisabled) {
		return 0, err
	}
	if maxDevshardEpoch > maxInferenceEpoch {
		return maxDevshardEpoch, nil
	}
	return maxInferenceEpoch, nil
}

func windowMinEpochExclusiveForAPI(maxEpoch uint64, epochsN int32) uint64 {
	if maxEpoch < uint64(epochsN) {
		return 0
	}
	return maxEpoch - uint64(epochsN)
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
