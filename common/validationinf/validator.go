package validationinf

import (
	"context"
	"log/slog"
	"time"
)

// claimStore is the narrow interface validator needs from storage.Storage.
type claimStore interface {
	Claim(ctx context.Context, inferenceID string, epochID uint64, instanceAddress string) (bool, error)
	ReclaimOneStale(ctx context.Context, instanceAddress, ttlInterval string) (string, error)
	SetTxHash(ctx context.Context, inferenceID, txHash string) error
	DeleteClaimsByEpoch(ctx context.Context, beforeEpochID uint64) error
}

// chainBridge is the narrow interface validator needs from the chain client.
type chainBridge interface {
	GetEpochSeed(ctx context.Context, epochIndex uint64) ([]byte, error)
	ReportValidation(ctx context.Context, inferenceID string, similarityScore float64) error
}

// Config holds tunable parameters for the validator.
type Config struct {
	// InstanceAddress is this instance's warm key address, used as the claim identity.
	InstanceAddress string
	// ClaimTTL is how long a claim can be held before it is considered stale.
	ClaimTTL time.Duration
	// RetryInterval is how often to scan for stale claims.
	RetryInterval time.Duration
}

// Validator orchestrates inference validation triggered by chain events.
type Validator struct {
	cfg     Config
	claims  claimStore
	chain   chainBridge
	payload *PayloadRetriever
}

// New creates a Validator. cfg.InstanceAddress must be non-empty.
func New(cfg Config, claims claimStore, chain chainBridge) *Validator {
	return &Validator{
		cfg:     cfg,
		claims:  claims,
		chain:   chain,
		payload: &PayloadRetriever{},
	}
}

// HandleInferenceFinished is called when an inference_finished chain event is received.
// It determines whether this instance should validate, claims the work, and runs it.
func (v *Validator) HandleInferenceFinished(ctx context.Context, inferenceID string, epochID uint64) {
	// TODO: query chain for epoch seed + validation params
	// TODO: ShouldValidate(seed, inferenceID, executorPower, params)
	should := v.shouldValidate(inferenceID, epochID)
	if !should {
		return
	}

	won, err := v.claims.Claim(ctx, inferenceID, epochID, v.cfg.InstanceAddress)
	if err != nil {
		slog.Error("validation: claim failed", "inference_id", inferenceID, "error", err)
		return
	}
	if !won {
		return // another instance claimed it
	}

	v.runValidation(ctx, inferenceID, epochID)
}

// HandleEpochChanged is called when an epoch_changed chain event is received.
// It cleans up validation claims for old epochs.
func (v *Validator) HandleEpochChanged(ctx context.Context, newEpochID uint64) {
	if newEpochID < 2 {
		return
	}
	if err := v.claims.DeleteClaimsByEpoch(ctx, newEpochID-2); err != nil {
		slog.Error("validation: cleanup failed", "epoch_id", newEpochID, "error", err)
	}
}

// RunStaleRecovery scans for stale claims and re-runs validation for each.
// Intended to be called on a timer (RetryInterval).
func (v *Validator) RunStaleRecovery(ctx context.Context) {
	ttl := v.cfg.ClaimTTL.String()
	for {
		inferenceID, err := v.claims.ReclaimOneStale(ctx, v.cfg.InstanceAddress, ttl)
		if err != nil {
			slog.Error("validation: stale reclaim failed", "error", err)
			return
		}
		if inferenceID == "" {
			return // no more stale claims
		}
		// TODO: look up epochID for this inferenceID from chain
		v.runValidation(ctx, inferenceID, 0)
	}
}

// shouldValidate is a deterministic sampling function.
// TODO: replace stub with real BLS-seed-based sampling.
func (v *Validator) shouldValidate(inferenceID string, epochID uint64) bool {
	_ = inferenceID
	_ = epochID
	return true // stub: always validate
}

// runValidation executes the full validation flow for a claimed inference.
func (v *Validator) runValidation(ctx context.Context, inferenceID string, epochID uint64) {
	slog.Info("validation: running", "inference_id", inferenceID, "epoch_id", epochID)

	// TODO: retrieve payload from executor's /v1/inference/payloads
	// TODO: verify executor signature
	// TODO: verify prompt/response hashes match on-chain commitment
	// TODO: acquire ML node, re-run inference, release
	// TODO: compare logprobs token-by-token
	// TODO: submit MsgValidation

	// Stub: mark as done with a placeholder tx hash
	if err := v.claims.SetTxHash(ctx, inferenceID, "stub-tx-hash"); err != nil {
		slog.Error("validation: set tx hash failed", "inference_id", inferenceID, "error", err)
	}
}
