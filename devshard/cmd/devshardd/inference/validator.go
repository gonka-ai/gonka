package inference

import (
	"bytes"
	"common/chain"
	"common/storage/validationlease"
	commonvalidation "common/validation"
	"context"
	devshardpkg "devshard"
	"devshard/bridge"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
)

// leaseOps is satisfied by *validationlease.Store; extracted as interface for testing.
type leaseOps interface {
	Acquire(ctx context.Context, escrowId string, inferenceId uint64, epochId uint64, instanceAddr string) (bool, error)
	SetResult(ctx context.Context, escrowId string, inferenceId uint64, status validationlease.LeaseStatus) error
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
}

// NewValidator creates a Validator. boundVersion is the runtime version string used
// to construct the payload request path.
func NewValidator(
	br bridge.MainnetBridge,
	recorder PayloadAuthClient,
	engine *Engine,
	phase *chain.Phase,
	boundVersion string,
	chainParams ChainParamsProvider,
) *Validator {
	return &Validator{
		bridge:       br,
		recorder:     recorder,
		engine:       engine,
		phase:        phase,
		boundVersion: boundVersion,
		chainParams:  chainParams,
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
		return nil, fmt.Errorf("fetch payloads from executor: %w", err)
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
		return nil, err
	}
	return &devshardpkg.ValidateResult{Valid: result.IsSuccessful()}, nil
}

func (v *Validator) executeMLRequest(ctx context.Context, model string, body []byte) (*http.Response, error) {
	resp, err := v.engine.doWithLockedNode(ctx, model, func(endpoint string) (*http.Response, error) {
		url := endpoint + "/v1/chat/completions"
		httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if reqErr != nil {
			return nil, reqErr
		}
		httpReq.Header.Set("Content-Type", "application/json")
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
}

// NewLeaseValidator wraps v with Postgres lease deduplication.
func NewLeaseValidator(v devshardpkg.ValidationEngine, phase *chain.Phase, leases leaseOps, instanceAddr string) *LeaseValidator {
	return &LeaseValidator{
		validator:    v,
		phase:        phase,
		leases:       leases,
		instanceAddr: instanceAddr,
	}
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

	result, err := c.validator.Validate(ctx, req)
	if err != nil {
		if errors.Is(err, commonvalidation.ErrHashMismatch) {
			// Executor served wrong payload with valid signature: immediate invalidation, no retry.
			slog.Warn("devshardd: hash mismatch — submitting immediate invalidation",
				"escrow", req.EscrowID, "inference", req.InferenceID)
			return &devshardpkg.ValidateResult{Valid: false}, nil
		}
		return nil, err
	}

	return result, nil
}

func (c *LeaseValidator) MarkValidationSubmitted(ctx context.Context, escrowID string, inferenceID uint64) error {
	return c.leases.SetResult(ctx, escrowID, inferenceID, validationlease.StatusSubmitted)
}

var _ devshardpkg.ValidationEngine = (*LeaseValidator)(nil)
var _ devshardpkg.ValidationCompletionRecorder = (*LeaseValidator)(nil)
