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
	SetResult(ctx context.Context, escrowId string, inferenceId, epochID uint64, status storage.LeaseStatus, instanceAddr string) error
	OwnsPendingLease(ctx context.Context, escrowId string, inferenceId, epochID uint64, instanceAddr string) (bool, error)
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

func (v *Validator) CanValidate(model string) bool {
	return v.engine == nil || v.engine.validationBudget.available(model)
}

func (v *Validator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	req.EpochID = resolveValidationEpoch(v.phase, req.EpochID)
	if !v.CanValidateEpoch(req.EpochID) {
		return nil, devshardpkg.ErrValidationEpochUnavailable
	}
	if !v.CanValidate(req.Model) {
		return nil, devshardpkg.ErrValidationDeferred
	}
	inferenceID := strconv.FormatUint(req.InferenceID, 10)

	epochID := req.EpochID
	promptPayload, responsePayload, err := v.fetchPayloadsFromExecutor(
		ctx, req, inferenceID, epochID, devshardpkg.VersionedSessionPayloadPath(v.boundVersion, req.EscrowID),
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
			return v.executeMLRequest(ctx, req.Model, req.EscrowID, body, epochID)
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

func (v *Validator) executeMLRequest(ctx context.Context, model, escrowID string, body []byte, epochID uint64) (*http.Response, error) {
	guard := func() error {
		if !v.CanValidateEpoch(epochID) {
			return devshardpkg.ErrValidationEpochUnavailable
		}
		return nil
	}
	resp, err := v.engine.doWithLockedNode(ctx, observability.PathValidate, model, escrowID, func(endpoint string, refund func()) (*http.Response, error) {
		url := endpoint + "/v1/chat/completions"
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		// NewRequest also accepts URLs that the HTTP transport cannot dispatch.
		if reqErr == nil && (httpReq.URL.Host == "" || (httpReq.URL.Scheme != "http" && httpReq.URL.Scheme != "https")) {
			reqErr = fmt.Errorf("invalid ML node HTTP endpoint %q", endpoint)
		}
		if reqErr != nil {
			refund()
			return nil, observability.Classify(observability.ReasonApplicationErr, observability.WhereEngineMLNodeCall, reqErr)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		observability.InjectRequestContext(ctx, httpReq.Header)
		observability.AttachRequestID(httpReq)
		if err := ctx.Err(); err != nil {
			refund()
			return nil, err
		}
		if err := guard(); err != nil {
			refund()
			return nil, err
		}
		return v.engine.httpClient.Do(httpReq)
	}, guard)
	if err != nil {
		return nil, fmt.Errorf("validate inference: %w", err)
	}
	return resp, nil
}

// LeaseValidator wraps a ValidationEngine with Postgres-based lease deduplication so
// that only one devshardd instance validates each (escrow_id, inference_id) pair.
// The retry loop uses the inner Validator directly because it already holds the lease.
type LeaseValidator struct {
	validator    devshardpkg.ValidationEngine
	phase        *chain.Phase
	leases       leaseOps
	instanceAddr string
	leaseTTL     time.Duration
	acquires     sync.Map // acquireKey -> validationAcquire
}

// NewLeaseValidator wraps v with Postgres lease deduplication.
func NewLeaseValidator(v devshardpkg.ValidationEngine, phase *chain.Phase, leases leaseOps, instanceAddr string, leaseTTL time.Duration) *LeaseValidator {
	if leaseTTL <= 0 {
		leaseTTL = 32 * time.Minute
	}
	return &LeaseValidator{
		validator:    v,
		phase:        phase,
		leases:       leases,
		instanceAddr: instanceAddr,
		leaseTTL:     leaseTTL,
	}
}

type validationAcquire struct {
	epochID uint64
	at      time.Time
}

func (c *LeaseValidator) rememberAcquire(escrowID string, inferenceID, epochID uint64, at time.Time) {
	c.acquires.Store(acquireKey{escrowID, inferenceID}, validationAcquire{epochID, at})
}

func (c *LeaseValidator) ForgetValidation(escrowID string, inferenceID uint64) {
	c.forgetAcquire(escrowID, inferenceID)
}

func (c *LeaseValidator) forgetAcquire(escrowID string, inferenceID uint64) {
	c.acquires.Delete(acquireKey{escrowID, inferenceID})
}

func (c *LeaseValidator) acquired(escrowID string, inferenceID uint64) (validationAcquire, bool) {
	v, ok := c.acquires.Load(acquireKey{escrowID, inferenceID})
	if !ok {
		return validationAcquire{}, false
	}
	rec, ok := v.(validationAcquire)
	return rec, ok
}

func (c *LeaseValidator) CanValidateEpoch(epochID uint64) bool {
	return c.phase != nil && epochID != 0 && c.phase.EpochID() == epochID
}

func (v *Validator) CanValidateEpoch(epochID uint64) bool {
	return v.phase != nil && epochID != 0 && v.phase.EpochID() == epochID
}

func resolveValidationEpoch(phase *chain.Phase, epochID uint64) uint64 {
	if epochID == 0 && phase != nil {
		return phase.EpochID()
	}
	return epochID
}

func (c *LeaseValidator) CanValidate(model string) bool {
	return devshardpkg.CanValidate(c.validator, model)
}

func (c *LeaseValidator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	req.EpochID = resolveValidationEpoch(c.phase, req.EpochID)
	if !c.CanValidateEpoch(req.EpochID) {
		return nil, devshardpkg.ErrValidationEpochUnavailable
	}
	if !c.CanValidate(req.Model) {
		return nil, devshardpkg.ErrValidationDeferred
	}
	epochID := req.EpochID
	acquired, err := c.leases.Acquire(ctx, req.EscrowID, req.InferenceID, epochID, c.instanceAddr)
	if err != nil {
		slog.Warn("devshardd: validation lease failed",
			"escrow", req.EscrowID, "inference", req.InferenceID, "error", err)
		return nil, fmt.Errorf("acquire validation: %w", err)
	} else if !acquired {
		return nil, devshardpkg.ErrValidationAlreadyLeased
	}
	c.rememberAcquire(req.EscrowID, req.InferenceID, epochID, time.Now())

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
	rec, _ := c.acquired(escrowID, inferenceID)
	if err := c.ensureLeaseStillValid(ctx, escrowID, inferenceID); err != nil {
		c.forgetAcquire(escrowID, inferenceID)
		return err
	}
	err := c.leases.SetResult(ctx, escrowID, inferenceID, rec.epochID, storage.LeaseStatusSubmitted, c.instanceAddr)
	c.forgetAcquire(escrowID, inferenceID)
	if errors.Is(err, storage.ErrLeaseNotOwned) {
		return fmt.Errorf("%w: %v", devshardpkg.ErrValidationLeaseAbandoned, err)
	}
	return err
}

func (c *LeaseValidator) CheckValidationLease(escrowID string, inferenceID uint64) error {
	rec, ok := c.acquired(escrowID, inferenceID)
	if !ok {
		return fmt.Errorf("%w: missing local acquire time", devshardpkg.ErrValidationLeaseAbandoned)
	}
	if !c.CanValidateEpoch(rec.epochID) {
		return devshardpkg.ErrValidationEpochUnavailable
	}
	if time.Since(rec.at) > c.leaseTTL {
		slog.Info("devshardd: validation lease TTL exceeded; abandon submit",
			"escrow", escrowID, "inference", inferenceID, "lease_ttl", c.leaseTTL)
		return fmt.Errorf("%w: elapsed since acquire exceeds lease TTL", devshardpkg.ErrValidationLeaseAbandoned)
	}
	return nil
}

func (c *LeaseValidator) ensureLeaseStillValid(ctx context.Context, escrowID string, inferenceID uint64) error {
	if err := c.CheckValidationLease(escrowID, inferenceID); err != nil {
		return err
	}
	rec, _ := c.acquired(escrowID, inferenceID)
	owned, err := c.leases.OwnsPendingLease(ctx, escrowID, inferenceID, rec.epochID, c.instanceAddr)
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
