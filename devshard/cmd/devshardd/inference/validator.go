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
	SetResult(ctx context.Context, escrowId string, inferenceId, epochId uint64, status storage.LeaseStatus, instanceAddr string) error
	OwnsPendingLease(ctx context.Context, escrowId string, inferenceId, epochId uint64, instanceAddr string) (bool, error)
	Release(ctx context.Context, escrowId string, inferenceId, epochId uint64, instanceAddr string) error
}

type acquireKey struct {
	escrowID    string
	inferenceID uint64
}

// Validator implements devshard.ValidationEngine for the standalone devshardd binary.
// It performs ML-based inference validation without lease deduplication.
// Use LeaseValidator to add Postgres-based lease deduplication on top.
type Validator struct {
	bridge                  bridge.MainnetBridge
	recorder                PayloadAuthClient
	engine                  *Engine
	phase                   *chain.Phase
	boundVersion            string
	chainParams             ChainParamsProvider
	thresholds              ValidationThresholdResolver
	voteFalseOnFetchFailure bool
	payloadHTTPClient       *http.Client
	fetchPayloads           payloadFetchFunc
	executeML               mlExecuteFunc
}

type payloadFetchFunc func(ctx context.Context, req devshardpkg.ValidateRequest, inferenceID string, epochID uint64) ([]byte, []byte, error)

type mlExecuteFunc func(ctx context.Context, model, escrowID string, body []byte) (*http.Response, error)

// NewValidator creates a Validator. boundVersion is the runtime version string used
// to construct the payload request path. thresholds resolves the per-model
// similarity pass threshold (long-poll snapshot first, chain fallback).
// voteFalseOnFetchFailure converts executor-attributable payload failures into
// Valid:false instead of abandoning the attempt.
func NewValidator(
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
	engine *Engine,
	phase *chain.Phase,
	boundVersion string,
	chainParams ChainParamsProvider,
	thresholds ValidationThresholdResolver,
	voteFalseOnFetchFailure bool,
) *Validator {
	return &Validator{
		bridge:                  br,
		recorder:                recorder,
		engine:                  engine,
		phase:                   phase,
		boundVersion:            boundVersion,
		chainParams:             chainParams,
		thresholds:              thresholds,
		voteFalseOnFetchFailure: voteFalseOnFetchFailure,
		payloadHTTPClient:       newPayloadFetchClient(),
	}
}

func (v *Validator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	inferenceID := strconv.FormatUint(req.InferenceID, 10)

	epochID := resolveValidationEpoch(v.phase, req.EpochID)
	promptPayload, responsePayload, err := v.fetchPayloadsFor(ctx, req, inferenceID, epochID)
	if err != nil {
		if verdict := executorFaultVerdict(ctx, v.phase, req, epochID, err, v.voteFalseOnFetchFailure); verdict != nil {
			return verdict, nil
		}
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
		tagged := tagExecutorPayloadFault(err)
		if verdict := executorFaultVerdict(ctx, v.phase, req, epochID, tagged, v.voteFalseOnFetchFailure); verdict != nil {
			return verdict, nil
		}
		return nil, observability.Classify(observability.ReasonValidationBuildErr, observability.WhereRuntimeValidate, fmt.Errorf("modify request body for validation: %w", err))
	}
	if _, err := commonvalidation.UnmarshalResponsePayload(responsePayload); err != nil {
		tagged := tagExecutorPayloadFault(err)
		if verdict := executorFaultVerdict(ctx, v.phase, req, epochID, tagged, v.voteFalseOnFetchFailure); verdict != nil {
			return verdict, nil
		}
		return nil, observability.Classify(observability.ReasonOriginalParseErr, observability.WhereRuntimeValidate, fmt.Errorf("parse original response: %w", err))
	}

	result, err := commonvalidation.ExecuteValidation(
		ctx,
		inferenceID,
		promptPayload,
		responsePayload,
		func(ctx context.Context, body []byte) (*http.Response, error) {
			return v.executeMLRequest(ctx, req.Model, req.EscrowID, body)
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

func (v *Validator) fetchPayloadsFor(ctx context.Context, req devshardpkg.ValidateRequest, inferenceID string, epochID uint64) ([]byte, []byte, error) {
	if v.fetchPayloads != nil {
		return v.fetchPayloads(ctx, req, inferenceID, epochID)
	}
	return fetchPayloadsFromExecutor(
		ctx, v.bridge, v.recorder, req, inferenceID, epochID,
		devshardpkg.VersionedSessionPayloadPath(v.boundVersion, req.EscrowID),
		v.payloadHTTPClient,
	)
}

const executorPayloadUnavailableReason = "executor_payload_unavailable"

// resolveValidationEpoch is the single source of the epoch used for both the
// payload fetch and the validation lease. Prefer the request (the inference's
// escrow epoch); fall back to the current phase only when the caller left it
// unset.
func resolveValidationEpoch(phase *chain.Phase, reqEpoch uint64) uint64 {
	if reqEpoch != 0 {
		return reqEpoch
	}
	return phase.EpochID()
}

// maxVerdictCauseBytes caps the cause string carried in ValidateResult.Details.
// The underlying error already carries a bounded executor body, but the details
// are logged on every publish, so keep them short.
const maxVerdictCauseBytes = 512

// executorFaultVerdict converts an executor-attributable failure into a
// Valid:false verdict. epochID is the epoch the payload was actually requested
// for (resolved by the caller), not req.EpochID, which may be unset.
func executorFaultVerdict(ctx context.Context, phase *chain.Phase, req devshardpkg.ValidateRequest, epochID uint64, err error, enabled bool) *devshardpkg.ValidateResult {
	if !enabled || err == nil || ctx.Err() != nil {
		return nil
	}
	inWindow := true
	if phase != nil {
		inWindow = phase.EpochID() <= epochID
	}
	reason := observability.ReasonPayloadFetchErr
	switch {
	case errors.Is(err, commonvalidation.ErrPayloadGone):
		if !inWindow {
			return nil
		}
		reason = observability.ReasonPayloadNotFound
	case errors.Is(err, commonvalidation.ErrPayloadTooLarge):
		reason = observability.ReasonPayloadTooLarge
	case errors.Is(err, errExecutorPayloadFault):
		// executor-attributable
	default:
		return nil
	}

	// The cause can quote an executor-supplied body, so both the log line and
	// the published details get the same bounded leading span.
	cause := truncateCause(err.Error())

	// No StageValidationFinished counter here: the caller publishes this verdict
	// and counts the stage as OK, so an error there would double-count.
	observability.IncValidationExecutorFault(reason)
	observability.Log(ctx, observability.LevelWarn, "validation voting false: executor payload unavailable",
		observability.StageValidationFinished, observability.WhereRuntimeValidate, req.EscrowID, reason, nil,
		"inference_id", req.InferenceID,
		"executor_address", req.ExecutorAddress,
		"epoch_id", epochID,
		"cause", cause,
	)
	return &devshardpkg.ValidateResult{
		Valid:  false,
		Reason: executorPayloadUnavailableReason,
		Details: []any{
			"cause", cause,
			"classified_reason", string(reason),
		},
	}
}

func truncateCause(s string) string {
	if len(s) <= maxVerdictCauseBytes {
		return s
	}
	return s[:maxVerdictCauseBytes] + "...(truncated)"
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

func (v *Validator) executeMLRequest(ctx context.Context, model, escrowID string, body []byte) (*http.Response, error) {
	if v.executeML != nil {
		return v.executeML(ctx, model, escrowID, body)
	}
	// Replays the original prompt with no session (no affinity/cache_salt) so it verifies exactly.
	resp, err := v.engine.doWithLockedNode(ctx, observability.PathValidate, model, escrowID, "", func(endpoint string) (*http.Response, error) {
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
	acquires     sync.Map // acquireKey -> acquireRec
}

type acquireRec struct {
	epochID uint64
	at      time.Time
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

func (c *LeaseValidator) rememberAcquire(escrowID string, inferenceID, epochID uint64, at time.Time) {
	c.acquires.Store(acquireKey{escrowID: escrowID, inferenceID: inferenceID}, acquireRec{epochID: epochID, at: at})
}

func (c *LeaseValidator) forgetAcquire(escrowID string, inferenceID uint64) {
	c.acquires.Delete(acquireKey{escrowID: escrowID, inferenceID: inferenceID})
}

func (c *LeaseValidator) loadAcquire(escrowID string, inferenceID uint64) (acquireRec, bool) {
	v, ok := c.acquires.Load(acquireKey{escrowID: escrowID, inferenceID: inferenceID})
	if !ok {
		return acquireRec{}, false
	}
	rec, ok := v.(acquireRec)
	return rec, ok
}

func (c *LeaseValidator) Validate(ctx context.Context, req devshardpkg.ValidateRequest) (*devshardpkg.ValidateResult, error) {
	epochID := resolveValidationEpoch(c.phase, req.EpochID)
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
		c.releaseAndForget(ctx, req.EscrowID, req.InferenceID, epochID)
		return nil, err
	}

	return result, nil
}

// AllowValidationSubmit gates MsgValidation publish: TTL since acquire and
// current pending ownership must both still hold. On failure the caller must
// ReleaseValidationLease; this method leaves the local acquire recorded so
// release can see the epoch.
func (c *LeaseValidator) AllowValidationSubmit(ctx context.Context, escrowID string, inferenceID uint64) error {
	_, err := c.ensureLeaseStillValid(ctx, escrowID, inferenceID)
	return err
}

func (c *LeaseValidator) MarkValidationSubmitted(ctx context.Context, escrowID string, inferenceID uint64) error {
	rec, err := c.ensureLeaseStillValid(ctx, escrowID, inferenceID)
	if err != nil {
		c.forgetAcquire(escrowID, inferenceID)
		return err
	}
	err = c.leases.SetResult(ctx, escrowID, inferenceID, rec.epochID, storage.LeaseStatusSubmitted, c.instanceAddr)
	c.forgetAcquire(escrowID, inferenceID)
	if errors.Is(err, storage.ErrLeaseNotOwned) {
		return fmt.Errorf("%w: %v", devshardpkg.ErrValidationLeaseAbandoned, err)
	}
	return err
}

func (c *LeaseValidator) ReleaseValidationLease(ctx context.Context, escrowID string, inferenceID uint64) error {
	rec, ok := c.loadAcquire(escrowID, inferenceID)
	if !ok {
		return nil
	}
	err := c.leases.Release(ctx, escrowID, inferenceID, rec.epochID, c.instanceAddr)
	c.forgetAcquire(escrowID, inferenceID)
	return err
}

func (c *LeaseValidator) releaseAndForget(ctx context.Context, escrowID string, inferenceID, epochID uint64) {
	if err := c.leases.Release(ctx, escrowID, inferenceID, epochID, c.instanceAddr); err != nil {
		slog.Warn("devshardd: validation lease release failed",
			"escrow", escrowID, "inference", inferenceID, "error", err)
	}
	c.forgetAcquire(escrowID, inferenceID)
}

func (c *LeaseValidator) ensureLeaseStillValid(ctx context.Context, escrowID string, inferenceID uint64) (acquireRec, error) {
	rec, ok := c.loadAcquire(escrowID, inferenceID)
	if !ok {
		return acquireRec{}, fmt.Errorf("%w: missing local acquire time", devshardpkg.ErrValidationLeaseAbandoned)
	}
	if time.Since(rec.at) > c.leaseTTL {
		slog.Info("devshardd: validation lease TTL exceeded; abandon submit",
			"escrow", escrowID, "inference", inferenceID, "lease_ttl", c.leaseTTL)
		return rec, fmt.Errorf("%w: %w", devshardpkg.ErrValidationLeaseAbandoned, devshardpkg.ErrValidationLeaseTTLExceeded)
	}
	owned, err := c.leases.OwnsPendingLease(ctx, escrowID, inferenceID, rec.epochID, c.instanceAddr)
	if err != nil {
		return rec, err
	}
	if !owned {
		slog.Info("devshardd: validation lease no longer owned; abandon submit",
			"escrow", escrowID, "inference", inferenceID)
		return rec, fmt.Errorf("%w: pending lease not owned", devshardpkg.ErrValidationLeaseAbandoned)
	}
	return rec, nil
}

var _ devshardpkg.ValidationEngine = (*LeaseValidator)(nil)
var _ devshardpkg.ValidationCompletionRecorder = (*LeaseValidator)(nil)
