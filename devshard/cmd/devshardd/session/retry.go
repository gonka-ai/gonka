package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"common/chain"
	commonvalidation "common/validation"
	devshardpkg "devshard"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"
)

// sessionManager abstracts *HostManager for testing.
type sessionManager interface {
	ActiveEscrowIDs() []string
	existingServer(escrowID string) (*transport.Server, bool)
}

// staleLeaseStore abstracts storage.LeaseStore for testing.
type staleLeaseStore interface {
	AcquireOneStale(ctx context.Context, escrowId, instanceAddr string, ttl time.Duration) (uint64, uint64, error)
	SetResult(ctx context.Context, escrowId string, inferenceId, epochID uint64, status storage.LeaseStatus, instanceAddr string) error
	OwnsPendingLease(ctx context.Context, escrowId string, inferenceId, epochID uint64, instanceAddr string) (bool, error)
}

// hostSnap abstracts *host.Host state reads for testing.
type hostSnap interface {
	SnapshotState() types.EscrowState
	Group() []types.SlotAssignment
}

const (
	DefaultRetryInterval = 5 * time.Minute
	DefaultLeaseTTL      = 32 * time.Minute
)

// RetryLoop scans for stale validation leases and re-runs validation for each
// active in-memory session. A lease is stale when status = 'pending' and
// claimed_at < now() - leaseTTL (default 32m). FOR UPDATE SKIP LOCKED in the
// underlying query ensures concurrent instances each pick a different row.
type RetryLoop struct {
	leases       staleLeaseStore
	inner        devshardpkg.ValidationEngine // no lease wrapping: lease already held
	manager      sessionManager
	phase        *chain.Phase
	instanceAddr string
	leaseTTL     time.Duration
	interval     time.Duration
}

// NewRetryLoop creates a RetryLoop. inner must be a Validator without lease
// wrapping so it does not re-attempt to acquire (the retry loop holds the lease already).
func NewRetryLoop(
	leases storage.LeaseStore,
	inner devshardpkg.ValidationEngine,
	manager *HostManager,
	phase *chain.Phase,
	instanceAddr string,
) *RetryLoop {
	return &RetryLoop{
		leases:       leases,
		inner:        inner,
		manager:      manager,
		phase:        phase,
		instanceAddr: instanceAddr,
		leaseTTL:     DefaultLeaseTTL,
		interval:     DefaultRetryInterval,
	}
}

// WithInterval overrides the default retry interval. Used in tests and config-driven tuning.
func (r *RetryLoop) WithInterval(d time.Duration) *RetryLoop {
	r.interval = d
	return r
}

// WithLeaseTTL overrides the default lease TTL. Used in tests and config-driven tuning.
func (r *RetryLoop) WithLeaseTTL(d time.Duration) *RetryLoop {
	r.leaseTTL = d
	return r
}

// Run starts the retry ticker. Blocks until ctx is cancelled.
func (r *RetryLoop) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

func (r *RetryLoop) runOnce(ctx context.Context) {
	for _, escrowID := range r.manager.ActiveEscrowIDs() {
		r.retryForEscrow(ctx, escrowID)
	}
}

// retryForEscrow loops until no more stale leases exist for this escrow.
func (r *RetryLoop) retryForEscrow(ctx context.Context, escrowID string) {
	srv, loaded := r.manager.existingServer(escrowID)
	if !loaded {
		return
	}
	epochID := srv.Host().EpochID()
	var models map[string]struct{}
	for {
		if !r.canValidateEpoch(epochID) {
			return
		}
		if gate, ok := r.inner.(devshardpkg.ValidationAvailability); ok {
			if models == nil {
				models = make(map[string]struct{})
				for _, rec := range srv.Host().SnapshotState().Inferences {
					models[rec.Model] = struct{}{}
				}
			}
			available := false
			for model := range models {
				if gate.CanValidate(model) {
					available = true
					break
				}
			}
			if !available {
				return
			}
		}
		inferenceID, leaseEpochID, err := r.leases.AcquireOneStale(ctx, escrowID, r.instanceAddr, r.leaseTTL)
		if err != nil {
			slog.Warn("devshardd: retry: acquire stale validation failed",
				"escrow", escrowID, "error", err)
			return
		}
		if inferenceID == 0 {
			return // no more stale leases for this escrow
		}

		if err := r.retryOne(ctx, escrowID, inferenceID, leaseEpochID); err != nil {
			if errors.Is(err, devshardpkg.ErrValidationDeferred) || errors.Is(err, devshardpkg.ErrValidationEpochUnavailable) {
				continue
			}
			slog.Warn("devshardd: retry: validation failed",
				"escrow", escrowID, "inference", inferenceID, "error", err)
			// Leave lease pending; another instance can acquire it after TTL.
		}
	}
}

func (r *RetryLoop) markLeaseResult(ctx context.Context, escrowID string, inferenceID, epochID uint64, status storage.LeaseStatus) {
	if err := r.leases.SetResult(ctx, escrowID, inferenceID, epochID, status, r.instanceAddr); err != nil {
		if errors.Is(err, storage.ErrLeaseNotOwned) {
			slog.Info("devshardd: retry: mark result skipped; lease not owned",
				"escrow", escrowID, "inference", inferenceID, "status", status)
			return
		}
		slog.Warn("devshardd: retry: mark result failed",
			"escrow", escrowID, "inference", inferenceID, "status", status, "error", err)
	}
}

func (r *RetryLoop) canValidateEpoch(epochID uint64) bool {
	return r.phase != nil && epochID != 0 && r.phase.EpochID() == epochID
}

// retryOne reconstructs a ValidateRequest from in-memory session state, runs
// validation via the inner engine, submits the result to the host's mempool,
// and marks the lease complete.
func (r *RetryLoop) retryOne(ctx context.Context, escrowID string, inferenceID, epochID uint64) error {
	acquiredAt := time.Now()
	srv, ok := r.manager.existingServer(escrowID)
	if !ok {
		return fmt.Errorf("session %s not loaded", escrowID)
	}
	h := srv.Host()
	if !r.canValidateEpoch(h.EpochID()) {
		return devshardpkg.ErrValidationEpochUnavailable
	}
	if epochID != h.EpochID() {
		r.markLeaseResult(ctx, escrowID, inferenceID, epochID, storage.LeaseStatusSkipped)
		return nil
	}
	eligible, queued := h.ValidationStatus(inferenceID)
	if !eligible {
		r.markLeaseResult(ctx, escrowID, inferenceID, epochID, storage.LeaseStatusSkipped)
		return nil
	}
	if queued {
		r.markLeaseResult(ctx, escrowID, inferenceID, epochID, storage.LeaseStatusSubmitted)
		return nil
	}

	req, ok := buildValidateRequest(h, escrowID, inferenceID, epochID)
	if !ok {
		// State may have changed since the eligibility check.
		slog.Warn("devshardd: retry: inference no longer validatable, skipping",
			"escrow", escrowID, "inference", inferenceID)
		r.markLeaseResult(ctx, escrowID, inferenceID, epochID, storage.LeaseStatusSkipped)
		return nil
	}

	result, err := r.inner.Validate(ctx, req)
	if err != nil {
		if errors.Is(err, commonvalidation.ErrHashMismatch) {
			slog.Warn("devshardd: retry: hash mismatch — submitting immediate invalidation",
				"escrow", escrowID, "inference", inferenceID)
			result = &devshardpkg.ValidateResult{Valid: false}
		} else {
			return fmt.Errorf("validate: %w", err)
		}
	}

	if time.Since(acquiredAt) > r.leaseTTL {
		slog.Info("devshardd: retry: lease TTL exceeded after validate; abandon submit",
			"escrow", escrowID, "inference", inferenceID, "lease_ttl", r.leaseTTL)
		// Leave pending for another instance after TTL from this claim.
		return nil
	}
	owned, err := r.leases.OwnsPendingLease(ctx, escrowID, inferenceID, epochID, r.instanceAddr)
	if err != nil {
		return fmt.Errorf("owns pending lease: %w", err)
	}
	if !owned {
		slog.Info("devshardd: retry: lease no longer owned after validate; abandon submit",
			"escrow", escrowID, "inference", inferenceID)
		return nil
	}

	kind, err := h.PublishValidation(req.InferenceID, result.Valid, func() error {
		if !r.canValidateEpoch(epochID) {
			return devshardpkg.ErrValidationEpochUnavailable
		}
		if time.Since(acquiredAt) > r.leaseTTL {
			return devshardpkg.ErrValidationLeaseAbandoned
		}
		return nil
	})
	if err != nil {
		return err
	}
	if kind == "" {
		r.markLeaseResult(ctx, escrowID, inferenceID, epochID, storage.LeaseStatusSkipped)
		return nil
	}

	r.markLeaseResult(ctx, escrowID, inferenceID, epochID, storage.LeaseStatusSubmitted)
	slog.Info("devshardd: retry: validation submitted",
		"escrow", escrowID, "inference", inferenceID, "valid", result.Valid)
	return nil
}

// buildValidateRequest reconstructs a ValidateRequest from the host's current in-memory
// state snapshot. Returns (req, false) if the inference is not finished or challenged.
func buildValidateRequest(h hostSnap, escrowID string, inferenceID, epochID uint64) (devshardpkg.ValidateRequest, bool) {
	st := h.SnapshotState()
	rec, ok := st.Inferences[inferenceID]
	if !ok || (rec.Status != types.StatusFinished && rec.Status != types.StatusChallenged) {
		return devshardpkg.ValidateRequest{}, false
	}

	slotToAddr := make(map[uint32]string, len(h.Group()))
	for _, s := range h.Group() {
		slotToAddr[s.SlotID] = s.ValidatorAddress
	}

	return devshardpkg.ValidateRequest{
		InferenceID:     inferenceID,
		Model:           rec.Model,
		PromptHash:      rec.PromptHash,
		ResponseHash:    rec.ResponseHash,
		InputTokens:     rec.InputTokens,
		OutputTokens:    rec.OutputTokens,
		EscrowID:        escrowID,
		EpochID:         epochID,
		ExecutorAddress: slotToAddr[rec.ExecutorSlot],
	}, true
}
