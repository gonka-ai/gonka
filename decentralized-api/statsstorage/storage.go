package statsstorage

import (
	"context"
	"errors"
)

var ErrInferenceRecordNotFound = errors.New("inference record not found")

type UnixMillis int64

const (
	// UnixMillisTimestampThreshold In millis, this is actually VERY long ago (1975), but in seconds it's very far in the future (7587).
	// this makes it a good threshold for detecting timestamps that are in seconds instead of millis
	UnixMillisTimestampThreshold = 177260313800
)

// InferenceRecord is the off-chain source-of-truth record for one inference.
type InferenceRecord struct {
	InferenceID          string
	RequestedBy          string
	Model                string
	Status               string
	EpochID              uint64
	PromptTokenCount     uint64
	CompletionTokenCount uint64
	TotalTokenCount      uint64
	ActualCostInCoins    int64
	StartBlockTimestamp  UnixMillis
	EndBlockTimestamp    UnixMillis
	InferenceTimestamp   UnixMillis
}

type Summary struct {
	AiTokens             int64
	Inferences           int32
	ActualInferencesCost int64
}

type ModelSummary struct {
	Model      string
	AiTokens   int64
	Inferences int32
}

type DeveloperTimeStats struct {
	Developer string
	Stats     []InferenceRecord
}

type DeveloperEpochStats struct {
	Developer    string
	EpochID      uint64
	InferenceIDs []string
}

type DebugStats struct {
	StatsByTime  []DeveloperTimeStats
	StatsByEpoch []DeveloperEpochStats
}

// StatsStorage defines storage and read models for off-chain developer stats.
type StatsStorage interface {
	UpsertInference(ctx context.Context, rec InferenceRecord) error
	UpdateInferenceStatus(ctx context.Context, inferenceID, status string) error
	GetDeveloperInferencesByTime(ctx context.Context, developer string, timeFrom, timeTo UnixMillis) ([]InferenceRecord, error)
	GetSummaryByDeveloperEpochsBackwards(ctx context.Context, developer string, epochsN int32) (Summary, error)
	GetSummaryByEpochsBackwards(ctx context.Context, epochsN int32) (Summary, error)
	GetSummaryByTimePeriod(ctx context.Context, timeFrom, timeTo UnixMillis) (Summary, error)
	GetModelStatsByTime(ctx context.Context, timeFrom, timeTo UnixMillis) ([]ModelSummary, error)
	GetDebugStats(ctx context.Context) (DebugStats, error)
	PruneOlderThan(ctx context.Context, cutoffTimestamp UnixMillis) error
	Close()
}
