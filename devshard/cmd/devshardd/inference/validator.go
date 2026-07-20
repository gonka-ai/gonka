package inference

import (
	"bytes"
	"common/chain"
	"common/completionapi"
	commonvalidation "common/validation"
	"context"
	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/logging"
	"devshard/observability"
	"devshard/storage"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// leaseOps is satisfied by storage.LeaseStore; extracted as interface for testing.
type leaseOps interface {
	Acquire(ctx context.Context, escrowId string, inferenceId uint64, epochId uint64, instanceAddr string) (bool, error)
	SetResult(ctx context.Context, escrowId string, inferenceId uint64, status storage.LeaseStatus, instanceAddr string) error
	OwnsPendingLease(ctx context.Context, escrowId string, inferenceId uint64, instanceAddr string) (bool, error)
}

type acquireKey struct {
	escrowID    string
	inferenceID uint64
}

// Validator implements devshard.ValidationEngine for the standalone devshardd binary.
// It performs ML-based inference validation without lease deduplication.
// Use LeaseValidator to add Postgres-based lease deduplication on top.
type Validator struct {
	bridge       bridge.MainnetBridge
	recorder     PayloadAuthClient
	engine       *Engine
	phase        *chain.Phase
	boundVersion string
	chainParams  ChainParamsProvider
	thresholds   ValidationThresholdResolver
}

// NewValidator creates a Validator. boundVersion is the runtime version string used
// to construct the payload request path. thresholds resolves the per-model
// similarity pass threshold (long-poll snapshot first, chain fallback).
func NewValidator(
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
	engine *Engine,
	phase *chain.Phase,
	boundVersion string,
	chainParams ChainParamsProvider,
	thresholds ValidationThresholdResolver,
) *Validator {
	return &Validator{
		bridge:       br,
		recorder:     recorder,
		engine:       engine,
		phase:        phase,
		boundVersion: boundVersion,
		chainParams:  chainParams,
		thresholds:   thresholds,
	}
}

func (v *Validator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	inferenceID := strconv.FormatUint(req.InferenceID, 10)

	epochID := req.EpochID
	if epochID == 0 {
		epochID = v.phase.EpochID()
	}
	promptPayload, responsePayload, err := fetchPayloadsFromExecutor(
		ctx, v.bridge, v.recorder, req, inferenceID, epochID, devshardpkg.VersionedSessionPayloadPath(v.boundVersion, req.EscrowID),
	)
	if err != nil {
		if errors.Is(err, commonvalidation.ErrPayloadGone) {
			logging.Info("devshard validation skipped: payload pruned on executor",
				types.Validation,
				"inferenceId", inferenceID,
				"executor", req.ExecutorAddress,
				"epoch", epochID,
			)
			return nil, fmt.Errorf("%w: %v", devshardpkg.ErrValidationSkipped, err)
		}
		return nil, observability.Classify(observability.ReasonPayloadFetchErr, observability.WhereRuntimeValidate, fmt.Errorf("fetch payloads from executor: %w", err))
	}

	if _, err := completionapi.ModifyRequestBodyWithLogprobsMode(promptPayload, int32(req.InferenceID), v.chainParams.LogprobsMode()); err != nil {
		return nil, observability.Classify(observability.ReasonValidationBuildErr, observability.WhereRuntimeValidate, fmt.Errorf("modify request body for validation: %w", err))
	}
	if _, err := commonvalidation.UnmarshalResponsePayload(responsePayload); err != nil {
		return nil, observability.Classify(observability.ReasonOriginalParseErr, observability.WhereRuntimeValidate, fmt.Errorf("parse original response: %w", err))
	}

	result, err := commonvalidation.ExecuteValidation(
		ctx,
		inferenceID,
		promptPayload,
		responsePayload,
		func(ctx context.Context, body []byte) (*http.Response, error) {
			return v.executeMLRequest(ctx, req.Model, body)
		},
		req.InputTokens, req.OutputTokens,
		v.chainParams.LogprobsMode(),
	)
	if err != nil {
		return nil, classifyExecuteValidationErr(err)
	}

	valid, err := evaluateValidationResult(ctx, result, epochID, req.Model, v.thresholds)
	if err != nil {
		return nil, observability.Classify(observability.ReasonValidationBuildErr, observability.WhereRuntimeValidate,
			fmt.Errorf("evaluate validation result: %w", err))
	}
	return &devshardpkg.ValidateResult{Valid: valid}, nil
}

// evaluateValidationResult decides pass/fail from a validation outcome. Similarity
// results use the per-model threshold from chain/runtime config; length, token, and
// invalid outcomes fail directly without a threshold lookup.
func evaluateValidationResult(
	ctx context.Context,
	result commonvalidation.ValidationResult,
	epochID uint64,
	model string,
	thresholds ValidationThresholdResolver,
) (bool, error) {
	switch r := result.(type) {
	case *commonvalidation.SimilarityValidationResult:
		threshold, err := thresholds.Resolve(ctx, epochID, model)
		if err != nil {
			return false, err
		}
		return commonvalidation.SimilarityPassesThreshold(r.Value, threshold), nil
	case *commonvalidation.DifferentLengthValidationResult,
		*commonvalidation.DifferentTokensValidationResult,
		*commonvalidation.InvalidInferenceResult:
		return false, nil
	default:
		return false, fmt.Errorf("unknown validation result type %T", result)
	}
}

func (v *Validator) executeMLRequest(ctx context.Context, model string, body []byte) (*http.Response, error) {
	resp, err := v.engine.doWithLockedNode(ctx, observability.PathValidate, model, func(endpoint string) (*http.Response, error) {
		url := endpoint + "/v1/chat/completions"
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, observability.Classify(observability.ReasonApplicationErr, observability.WhereEngineMLNodeCall, reqErr)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		observability.InjectRequestContext(ctx, httpReq.Header)
		observability.AttachRequestID(httpReq)
		return v.engine.httpClient.Do(httpReq)
	})
	if err != nil {
		return nil, fmt.Errorf("validate inference: %w", err)
	}
	return resp, nil
}

var _ devshardpkg.ValidationEngine = (*Validator)(nil)

// LeaseValidator wraps a ValidationEngine with Postgres-based lease deduplication so
// that only one devshardd instance validates each (escrow_id, inference_id) pair.
// The retry loop uses the inner Validator directly because it already holds the lease.
type LeaseValidator struct {
	validator    devshardpkg.ValidationEngine
	phase        *chain.Phase
	leases       leaseOps
	instanceAddr string
	leaseTTL     time.Duration
	acquires     sync.Map // acquireKey -> time.Time
}

// NewLeaseValidator wraps v with Postgres lease deduplication.
func NewLeaseValidator(v devshardpkg.ValidationEngine, phase *chain.Phase, leases leaseOps, instanceAddr string, leaseTTL time.Duration) *LeaseValidator {
	if leaseTTL <= 0 {
		leaseTTL = 30 * time.Minute
	}
	return &LeaseValidator{
		validator:    v,
		phase:        phase,
		leases:       leases,
		instanceAddr: instanceAddr,
		leaseTTL:     leaseTTL,
	}
}

func (c *LeaseValidator) rememberAcquire(escrowID string, inferenceID uint64, at time.Time) {
	c.acquires.Store(acquireKey{escrowID: escrowID, inferenceID: inferenceID}, at)
}

func (c *LeaseValidator) forgetAcquire(escrowID string, inferenceID uint64) {
	c.acquires.Delete(acquireKey{escrowID: escrowID, inferenceID: inferenceID})
}

func (c *LeaseValidator) acquiredAt(escrowID string, inferenceID uint64) (time.Time, bool) {
	v, ok := c.acquires.Load(acquireKey{escrowID: escrowID, inferenceID: inferenceID})
	if !ok {
		return time.Time{}, false
	}
	at, ok := v.(time.Time)
	return at, ok
}

func (c *LeaseValidator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	epochID := c.phase.EpochID()
	acquired, err := c.leases.Acquire(ctx, req.EscrowID, req.InferenceID, epochID, c.instanceAddr)
	if err != nil {
		slog.Warn("devshardd: validation lease failed",
			"escrow", req.EscrowID, "inference", req.InferenceID, "error", err)
		return nil, fmt.Errorf("acquire validation: %w", err)
	} else if !acquired {
		return nil, devshardpkg.ErrValidationAlreadyLeased
	}
	c.rememberAcquire(req.EscrowID, req.InferenceID, time.Now())

	result, err := c.validator.Validate(ctx, req)
	if err != nil {
		if errors.Is(err, commonvalidation.ErrHashMismatch) {
			// Executor served wrong payload with valid signature: immediate invalidation, no retry.
			slog.Warn("devshardd: hash mismatch — submitting immediate invalidation",
				"escrow", req.EscrowID, "inference", req.InferenceID)
			return &devshardpkg.ValidateResult{Valid: false}, nil
		}
		c.forgetAcquire(req.EscrowID, req.InferenceID)
		return nil, err
	}

	return result, nil
}

// AllowValidationSubmit gates MsgValidation publish: TTL since acquire and
// current pending ownership must both still hold.
func (c *LeaseValidator) AllowValidationSubmit(ctx context.Context, escrowID string, inferenceID uint64) error {
	if err := c.ensureLeaseStillValid(ctx, escrowID, inferenceID); err != nil {
		c.forgetAcquire(escrowID, inferenceID)
		return err
	}
	return nil
}

func (c *LeaseValidator) MarkValidationSubmitted(ctx context.Context, escrowID string, inferenceID uint64) error {
	if err := c.ensureLeaseStillValid(ctx, escrowID, inferenceID); err != nil {
		c.forgetAcquire(escrowID, inferenceID)
		return err
	}
	err := c.leases.SetResult(ctx, escrowID, inferenceID, storage.LeaseStatusSubmitted, c.instanceAddr)
	c.forgetAcquire(escrowID, inferenceID)
	if errors.Is(err, storage.ErrLeaseNotOwned) {
		return fmt.Errorf("%w: %v", devshardpkg.ErrValidationLeaseAbandoned, err)
	}
	return err
}

func (c *LeaseValidator) ensureLeaseStillValid(ctx context.Context, escrowID string, inferenceID uint64) error {
	at, ok := c.acquiredAt(escrowID, inferenceID)
	if !ok {
		return fmt.Errorf("%w: missing local acquire time", devshardpkg.ErrValidationLeaseAbandoned)
	}
	if time.Since(at) > c.leaseTTL {
		slog.Info("devshardd: validation lease TTL exceeded; abandon submit",
			"escrow", escrowID, "inference", inferenceID, "lease_ttl", c.leaseTTL)
		return fmt.Errorf("%w: elapsed since acquire exceeds lease TTL", devshardpkg.ErrValidationLeaseAbandoned)
	}
	owned, err := c.leases.OwnsPendingLease(ctx, escrowID, inferenceID, c.instanceAddr)
	if err != nil {
		return err
	}
	if !owned {
		slog.Info("devshardd: validation lease no longer owned; abandon submit",
			"escrow", escrowID, "inference", inferenceID)
		return fmt.Errorf("%w: pending lease not owned", devshardpkg.ErrValidationLeaseAbandoned)
	}
	return nil
}

var _ devshardpkg.ValidationEngine = (*LeaseValidator)(nil)
var _ devshardpkg.ValidationCompletionRecorder = (*LeaseValidator)(nil)
