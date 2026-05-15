package statsstorage

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// Directory structure: {baseDir}/{hex(inferenceId)}.json
type FileStorage struct {
	baseDir string
}

func NewFileStorage(baseDir string) *FileStorage {
	return &FileStorage{baseDir: baseDir}
}

func (f *FileStorage) UpsertDevshardEscrow(ctx context.Context, totals DevshardEscrow, modelStats []DevshardModelAggregate) error {
	_ = ctx
	if totals.EscrowID == "" {
		return fmt.Errorf("escrow_id is required")
	}
	rec := devshardFileRecord{
		Escrow:     totals,
		ModelStats: modelStats,
	}
	return f.writeDevshardRecord(rec)
}

func (f *FileStorage) GetMaxInferenceEpoch(ctx context.Context) (uint64, error) {
	records, err := f.readAllRecords(ctx)
	if err != nil {
		return 0, err
	}
	return findMaxEpoch(records), nil
}

func (f *FileStorage) GetMaxDevshardEpoch(ctx context.Context) (uint64, error) {
	_ = ctx
	records, err := f.readAllDevshardRecords()
	if err != nil {
		return 0, err
	}
	return findMaxDevshardEpoch(records), nil
}

func (f *FileStorage) GetDevshardSummaryByDeveloperEpochsBackwards(ctx context.Context, developer string, epochsN int32) (Summary, error) {
	if epochsN <= 0 {
		return Summary{}, nil
	}
	maxEpoch, err := f.GetMaxDevshardEpoch(ctx)
	if err != nil {
		return Summary{}, err
	}
	minEpochExclusive := windowMinEpochExclusive(maxEpoch, epochsN)
	return f.GetDevshardSummaryByDeveloperEpochRange(ctx, developer, minEpochExclusive, maxEpoch)
}

func (f *FileStorage) GetDevshardSummaryByEpochsBackwards(ctx context.Context, epochsN int32) (Summary, error) {
	if epochsN <= 0 {
		return Summary{}, nil
	}
	maxEpoch, err := f.GetMaxDevshardEpoch(ctx)
	if err != nil {
		return Summary{}, err
	}
	minEpochExclusive := windowMinEpochExclusive(maxEpoch, epochsN)
	return f.GetDevshardSummaryByEpochRange(ctx, minEpochExclusive, maxEpoch)
}

func (f *FileStorage) GetDevshardSummaryByDeveloperEpochRange(ctx context.Context, developer string, minEpochExclusive, maxEpochInclusive uint64) (Summary, error) {
	_ = ctx
	records, err := f.readAllDevshardRecords()
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{}
	for _, rec := range records {
		if rec.Escrow.Participant != developer {
			continue
		}
		if rec.Escrow.EpochID > minEpochExclusive && rec.Escrow.EpochID <= maxEpochInclusive {
			summary.AiTokens += int64(rec.Escrow.TotalTokenCount)
			summary.Inferences += rec.Escrow.TotalInferences
			summary.ActualInferencesCost += rec.Escrow.TotalCostInCoins
		}
	}
	return summary, nil
}

func (f *FileStorage) GetDevshardSummaryByEpochRange(ctx context.Context, minEpochExclusive, maxEpochInclusive uint64) (Summary, error) {
	_ = ctx
	records, err := f.readAllDevshardRecords()
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{}
	for _, rec := range records {
		if rec.Escrow.EpochID > minEpochExclusive && rec.Escrow.EpochID <= maxEpochInclusive {
			summary.AiTokens += int64(rec.Escrow.TotalTokenCount)
			summary.Inferences += rec.Escrow.TotalInferences
			summary.ActualInferencesCost += rec.Escrow.TotalCostInCoins
		}
	}
	return summary, nil
}

func (f *FileStorage) GetDevshardSummaryByTimePeriod(ctx context.Context, timeFrom, timeTo UnixMillis) (Summary, error) {
	_ = ctx
	records, err := f.readAllDevshardRecords()
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{}
	for _, rec := range records {
		if rec.Escrow.SettlementTimestamp >= timeFrom && rec.Escrow.SettlementTimestamp <= timeTo {
			summary.AiTokens += int64(rec.Escrow.TotalTokenCount)
			summary.Inferences += rec.Escrow.TotalInferences
			summary.ActualInferencesCost += rec.Escrow.TotalCostInCoins
		}
	}
	return summary, nil
}

func (f *FileStorage) GetDevshardModelStatsByTime(ctx context.Context, timeFrom, timeTo UnixMillis) ([]ModelSummary, error) {
	_ = ctx
	records, err := f.readAllDevshardRecords()
	if err != nil {
		return nil, err
	}
	type modelAgg struct {
		tokens int64
		count  int32
	}
	aggByModel := make(map[string]modelAgg)
	for _, rec := range records {
		if rec.Escrow.SettlementTimestamp < timeFrom || rec.Escrow.SettlementTimestamp > timeTo {
			continue
		}
		for _, modelStat := range rec.ModelStats {
			agg := aggByModel[modelStat.Model]
			agg.tokens += int64(modelStat.TotalTokenCount)
			agg.count += modelStat.Inferences
			aggByModel[modelStat.Model] = agg
		}
	}

	models := make([]string, 0, len(aggByModel))
	for model := range aggByModel {
		models = append(models, model)
	}
	sort.Strings(models)

	result := make([]ModelSummary, 0, len(models))
	for _, model := range models {
		agg := aggByModel[model]
		result = append(result, ModelSummary{
			Model:      model,
			AiTokens:   agg.tokens,
			Inferences: agg.count,
		})
	}
	return result, nil
}

func (f *FileStorage) UpsertInference(ctx context.Context, rec InferenceRecord) error {
	_ = ctx
	rec = normalizeRecord(rec)
	if rec.InferenceID == "" {
		return fmt.Errorf("inference_id is required")
	}
	return f.writeRecord(rec)
}

func (f *FileStorage) UpdateInferenceStatus(ctx context.Context, inferenceID, status string) error {
	_ = ctx
	if inferenceID == "" {
		return fmt.Errorf("inference_id is required")
	}
	targetPath := f.recordPath(inferenceID)
	data, err := os.ReadFile(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrInferenceRecordNotFound
		}
		return fmt.Errorf("read stats file for status update %s: %w", targetPath, err)
	}
	var rec InferenceRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("unmarshal stats file for status update %s: %w", targetPath, err)
	}
	rec = normalizeRecord(rec)
	rec.InferenceID = inferenceID
	rec.Status = status
	return f.writeRecord(rec)
}

func (f *FileStorage) writeRecord(rec InferenceRecord) error {
	if err := os.MkdirAll(f.baseDir, 0o755); err != nil {
		return fmt.Errorf("create stats dir: %w", err)
	}
	targetPath := f.recordPath(rec.InferenceID)
	tempPath := targetPath + ".tmp"

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal inference record: %w", err)
	}
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return fmt.Errorf("write stats temp file: %w", err)
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("rename stats temp file: %w", err)
	}
	return nil
}

func (f *FileStorage) recordPath(inferenceID string) string {
	filename := hex.EncodeToString([]byte(inferenceID)) + ".json"
	return filepath.Join(f.baseDir, filename)
}

func (f *FileStorage) GetDeveloperInferencesByTime(ctx context.Context, developer string, timeFrom, timeTo UnixMillis) ([]InferenceRecord, error) {
	records, err := f.readAllRecords(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]InferenceRecord, 0)
	for _, rec := range records {
		if rec.RequestedBy == developer && rec.InferenceTimestamp >= timeFrom && rec.InferenceTimestamp <= timeTo {
			filtered = append(filtered, rec)
		}
	}
	sortInferenceRecords(filtered)
	return filtered, nil
}

func (f *FileStorage) GetSummaryByDeveloperEpochsBackwards(ctx context.Context, developer string, epochsN int32) (Summary, error) {
	if epochsN <= 0 {
		return Summary{}, nil
	}
	maxEpoch, err := f.GetMaxInferenceEpoch(ctx)
	if err != nil {
		return Summary{}, err
	}
	minEpochExclusive := windowMinEpochExclusive(maxEpoch, epochsN)
	return f.GetSummaryByDeveloperEpochRange(ctx, developer, minEpochExclusive, maxEpoch)
}

func (f *FileStorage) GetSummaryByEpochsBackwards(ctx context.Context, epochsN int32) (Summary, error) {
	if epochsN <= 0 {
		return Summary{}, nil
	}
	maxEpoch, err := f.GetMaxInferenceEpoch(ctx)
	if err != nil {
		return Summary{}, err
	}
	minEpochExclusive := windowMinEpochExclusive(maxEpoch, epochsN)
	return f.GetSummaryByEpochRange(ctx, minEpochExclusive, maxEpoch)
}

func (f *FileStorage) GetSummaryByDeveloperEpochRange(ctx context.Context, developer string, minEpochExclusive, maxEpochInclusive uint64) (Summary, error) {
	records, err := f.readAllRecords(ctx)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{}
	for _, rec := range records {
		if rec.RequestedBy != developer {
			continue
		}
		if rec.EpochID > minEpochExclusive && rec.EpochID <= maxEpochInclusive {
			summary.AiTokens += int64(rec.TotalTokenCount)
			summary.Inferences++
			summary.ActualInferencesCost += rec.ActualCostInCoins
		}
	}
	return summary, nil
}

func (f *FileStorage) GetSummaryByEpochRange(ctx context.Context, minEpochExclusive, maxEpochInclusive uint64) (Summary, error) {
	records, err := f.readAllRecords(ctx)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{}
	for _, rec := range records {
		if rec.EpochID > minEpochExclusive && rec.EpochID <= maxEpochInclusive {
			summary.AiTokens += int64(rec.TotalTokenCount)
			summary.Inferences++
			summary.ActualInferencesCost += rec.ActualCostInCoins
		}
	}
	return summary, nil
}

func (f *FileStorage) GetSummaryByTimePeriod(ctx context.Context, timeFrom, timeTo UnixMillis) (Summary, error) {
	records, err := f.readAllRecords(ctx)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{}
	for _, rec := range records {
		if rec.InferenceTimestamp >= timeFrom && rec.InferenceTimestamp <= timeTo {
			summary.AiTokens += int64(rec.TotalTokenCount)
			summary.Inferences++
			summary.ActualInferencesCost += rec.ActualCostInCoins
		}
	}
	return summary, nil
}

func (f *FileStorage) GetModelStatsByTime(ctx context.Context, timeFrom, timeTo UnixMillis) ([]ModelSummary, error) {
	records, err := f.readAllRecords(ctx)
	if err != nil {
		return nil, err
	}
	type modelAgg struct {
		tokens int64
		count  int32
	}
	aggByModel := make(map[string]modelAgg)
	for _, rec := range records {
		if rec.InferenceTimestamp < timeFrom || rec.InferenceTimestamp > timeTo {
			continue
		}
		agg := aggByModel[rec.Model]
		agg.tokens += int64(rec.TotalTokenCount)
		agg.count++
		aggByModel[rec.Model] = agg
	}

	models := make([]string, 0, len(aggByModel))
	for model := range aggByModel {
		models = append(models, model)
	}
	sort.Strings(models)

	result := make([]ModelSummary, 0, len(models))
	for _, model := range models {
		agg := aggByModel[model]
		result = append(result, ModelSummary{
			Model:      model,
			AiTokens:   agg.tokens,
			Inferences: agg.count,
		})
	}
	return result, nil
}

func (f *FileStorage) GetDebugStats(ctx context.Context) (DebugStats, error) {
	records, err := f.readAllRecords(ctx)
	if err != nil {
		return DebugStats{}, err
	}
	sortInferenceRecords(records)

	byDeveloper := make(map[string][]InferenceRecord)
	for _, rec := range records {
		byDeveloper[rec.RequestedBy] = append(byDeveloper[rec.RequestedBy], rec)
	}

	developers := make([]string, 0, len(byDeveloper))
	for developer := range byDeveloper {
		developers = append(developers, developer)
	}
	sort.Strings(developers)

	statsByTime := make([]DeveloperTimeStats, 0, len(developers))
	for _, developer := range developers {
		statsByTime = append(statsByTime, DeveloperTimeStats{
			Developer: developer,
			Stats:     byDeveloper[developer],
		})
	}

	epochGroups := make([]DeveloperEpochStats, 0)
	for _, developer := range developers {
		byEpoch := make(map[uint64][]string)
		for _, rec := range byDeveloper[developer] {
			byEpoch[rec.EpochID] = append(byEpoch[rec.EpochID], rec.InferenceID)
		}
		epochs := make([]uint64, 0, len(byEpoch))
		for epoch := range byEpoch {
			epochs = append(epochs, epoch)
		}
		sort.Slice(epochs, func(i, j int) bool { return epochs[i] < epochs[j] })
		for _, epoch := range epochs {
			ids := byEpoch[epoch]
			sort.Strings(ids)
			epochGroups = append(epochGroups, DeveloperEpochStats{
				Developer:    developer,
				EpochID:      epoch,
				InferenceIDs: ids,
			})
		}
	}

	return DebugStats{
		StatsByTime:  statsByTime,
		StatsByEpoch: epochGroups,
	}, nil
}

func (f *FileStorage) PruneOlderThan(ctx context.Context, cutoffTimestamp UnixMillis) error {
	_ = ctx
	entries, err := os.ReadDir(f.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read stats dir for prune: %w", err)
	}

	for _, entry := range entries {
		if entry.Name() == "devshard" && entry.IsDir() {
			continue // handled separately below
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(f.baseDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read stats file for prune %s: %w", path, err)
		}
		var rec InferenceRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			// Ignore malformed files during pruning to avoid permanent loop failures.
			logging.Warn("Skipping malformed stats record in prune", types.System, "path", path, "error", err)
			continue
		}
		rec = normalizeRecord(rec)
		if rec.InferenceTimestamp < cutoffTimestamp {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove pruned stats file %s: %w", path, err)
			}
		}
	}

	// Prune devshard files using settlement_timestamp.
	if err := f.pruneDevshardOlderThan(cutoffTimestamp); err != nil {
		return err
	}
	return nil
}

func (f *FileStorage) Close() {}

func (f *FileStorage) readAllRecords(ctx context.Context) ([]InferenceRecord, error) {
	_ = ctx
	entries, err := os.ReadDir(f.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []InferenceRecord{}, nil
		}
		return nil, fmt.Errorf("read stats dir: %w", err)
	}

	records := make([]InferenceRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(f.baseDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read stats file %s: %w", path, err)
		}
		var rec InferenceRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			logging.Warn("Skipping malformed stats record file", types.System, "path", path, "error", err)
			continue
		}
		records = append(records, normalizeRecord(rec))
	}
	return records, nil
}

func normalizeRecord(rec InferenceRecord) InferenceRecord {
	if rec.TotalTokenCount == 0 {
		rec.TotalTokenCount = rec.PromptTokenCount + rec.CompletionTokenCount
	}
	if rec.InferenceTimestamp == 0 {
		rec.InferenceTimestamp = rec.EndBlockTimestamp
		if rec.InferenceTimestamp == 0 {
			rec.InferenceTimestamp = rec.StartBlockTimestamp
		}
	}
	return rec
}

func sortInferenceRecords(records []InferenceRecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].InferenceTimestamp != records[j].InferenceTimestamp {
			return records[i].InferenceTimestamp < records[j].InferenceTimestamp
		}
		return records[i].InferenceID < records[j].InferenceID
	})
}

func findMaxEpoch(records []InferenceRecord) uint64 {
	var maxEpoch uint64
	for _, rec := range records {
		if rec.EpochID > maxEpoch {
			maxEpoch = rec.EpochID
		}
	}
	return maxEpoch
}

func findMaxDevshardEpoch(records []devshardFileRecord) uint64 {
	var maxEpoch uint64
	for _, rec := range records {
		if rec.Escrow.EpochID > maxEpoch {
			maxEpoch = rec.Escrow.EpochID
		}
	}
	return maxEpoch
}

func windowMinEpochExclusive(maxEpoch uint64, epochsN int32) uint64 {
	if maxEpoch < uint64(epochsN) {
		return 0
	}
	return maxEpoch - uint64(epochsN)
}

var _ StatsStorage = (*FileStorage)(nil)

// --- Devshard file storage helpers ---

// devshardFileRecord is the on-disk envelope for one devshard escrow settlement.
// Stored as {baseDir}/devshard/{hex(escrowID_epochID)}.json.
type devshardFileRecord struct {
	Escrow     DevshardEscrow           `json:"escrow"`
	ModelStats []DevshardModelAggregate `json:"model_stats"`
}

func (f *FileStorage) devshardDir() string {
	return filepath.Join(f.baseDir, "devshard")
}

func (f *FileStorage) devshardRecordPath(escrowID string, epochID uint64) string {
	key := fmt.Sprintf("%s_%d", escrowID, epochID)
	filename := hex.EncodeToString([]byte(key)) + ".json"
	return filepath.Join(f.devshardDir(), filename)
}

func (f *FileStorage) writeDevshardRecord(rec devshardFileRecord) error {
	dir := f.devshardDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create devshard stats dir: %w", err)
	}
	targetPath := f.devshardRecordPath(rec.Escrow.EscrowID, rec.Escrow.EpochID)
	tempPath := targetPath + ".tmp"

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal devshard record: %w", err)
	}
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return fmt.Errorf("write devshard temp file: %w", err)
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("rename devshard temp file: %w", err)
	}
	return nil
}

func (f *FileStorage) readAllDevshardRecords() ([]devshardFileRecord, error) {
	dir := f.devshardDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read devshard stats dir: %w", err)
	}

	records := make([]devshardFileRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read devshard file %s: %w", path, err)
		}
		var rec devshardFileRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			logging.Warn("Skipping malformed devshard record file", types.System, "path", path, "error", err)
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}

func (f *FileStorage) pruneDevshardOlderThan(cutoff UnixMillis) error {
	dir := f.devshardDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read devshard dir for prune: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read devshard file for prune %s: %w", path, err)
		}
		var rec devshardFileRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			logging.Warn("Skipping malformed devshard record in prune", types.System, "path", path, "error", err)
			continue
		}
		if rec.Escrow.SettlementTimestamp < cutoff {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove pruned devshard file %s: %w", path, err)
			}
		}
	}
	return nil
}
