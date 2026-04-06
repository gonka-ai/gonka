package engineinf

import (
	"context"
	"errors"
	"fmt"
	"time"

	"common/mlnode"
)

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("not found")

// Inference is a minimal chain inference record.
type Inference struct {
	ID    string
	Model string
}

// PayloadResponse holds prompt/response payloads for validation.
type PayloadResponse struct {
	InferenceID       string
	PromptPayload     string
	ResponsePayload   string
	ExecutorSignature string
}

// ChainQuerier retrieves inference records and payloads from chain/storage.
type ChainQuerier interface {
	GetInference(ctx context.Context, id string) (*Inference, error)
	GetInferencePayload(ctx context.Context, inferenceID string, epochID uint64) (*PayloadResponse, error)
}

const (
	maxRequestAgeSeconds    = 60
	maxRequestFutureSeconds = 10
	maxRetries              = 3
)

// Engine wires the narrow interfaces together.
type Engine struct {
	lock     mlnode.NodeLock
	querier  ChainQuerier
	executor *Executor
	transfer *Transfer
}

func NewEngine(lock mlnode.NodeLock, querier ChainQuerier, storage PayloadWriter, chain InferenceChain) *Engine {
	return &Engine{
		lock:     lock,
		querier:  querier,
		executor: &Executor{Lock: lock, Storage: storage, Chain: chain},
		transfer: &Transfer{},
	}
}

// ValidateTimestamp checks that the nanosecond timestamp is within the
// acceptable recency window. Returns an error if the timestamp is too old
// or too far in the future.
func ValidateTimestamp(tsNano int64) error {
	now := time.Now().UnixNano()
	diff := now - tsNano
	if diff > maxRequestAgeSeconds*int64(time.Second) {
		return fmt.Errorf("timestamp too old: %d ns ago", diff)
	}
	if diff < -maxRequestFutureSeconds*int64(time.Second) {
		return fmt.Errorf("timestamp too far in future: %d ns ahead", -diff)
	}
	return nil
}
