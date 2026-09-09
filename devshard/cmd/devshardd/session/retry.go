package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	"common/chain"
	commonvalidation "common/validation"
	devshardpkg "devshard"
	"devshard/host"
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
	SetResult(ctx context.Context, escrowId string, inferenceId uint64, status storage.LeaseStatus, instanceAddr string) error
	OwnsPendingLease(ctx context.Context, escrowId string, inferenceId uint64, instanceAddr string) (bool, error)
}

// hostSnap abstracts *host.Host state reads for testing.
type hostSnap interface {
	SnapshotState() types.EscrowState
	Group() []types.SlotAssignment
}

const (
	DefaultRetryInterval = 5 * time.Minute
	DefaultLeaseTTL      = 30 * time.Minute
)

// RetryLoop scans for stale validation leases and re-runs validation for each
// active in-memory session. A lease is stale when status = 'pending' and
// claimed_at < now() - leaseTTL (default 30m). FOR UPDATE SKIP LOCKED in the
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
	for {
		inferenceID, leaseEpochID, err := r.leases.AcquireOneStale(ctx, escrowID, r.instanceAddr, r.leaseTTL)
		if err != nil {
			slog.Warn("devshardd: retry: acquire stale validation failed",
				"escrow", escrowID, "error", err)
			return
		}
		if inferenceID == 0 {
			return // no more stale leases for this escrow
		}

		// Sessions are epoch-bounded: validation is no longer useful once the
		// chain advances beyond the inference epoch. Rows may be retained longer
		// for history and cleanup, but retry should stop at epoch+1.
		if r.phase != nil && r.phase.EpochID() > leaseEpochID {
			slog.Info("devshardd: retry: epoch stale, skipping validation",
				"escrow", escrowID, "inference", inferenceID,
				"lease_epoch", leaseEpochID, "current_epoch", r.phase.EpochID())
			r.markLeaseResult(ctx, escrowID, inferenceID, storage.LeaseStatusSkipped)
			continue
		}

		if err := r.retryOne(ctx, escrowID, inferenceID, leaseEpochID); err != nil {
			slog.Warn("devshardd: retry: validation failed",
				"escrow", escrowID, "inference", inferenceID, "error", err)
			// Leave lease pending; another instance can acquire it after TTL.
		}
	}
}

func (r *RetryLoop) markLeaseResult(ctx context.Context, escrowID string, inferenceID uint64, status storage.LeaseStatus) {
	if err := r.leases.SetResult(ctx, escrowID, inferenceID, status, r.instanceAddr); err != nil {
		if errors.Is(err, storage.ErrLeaseNotOwned) {
			slog.Info("devshardd: retry: mark result skipped; lease not owned",
				"escrow", escrowID, "inference", inferenceID, "status", status)
			return
		}
		slog.Warn("devshardd: retry: mark result failed",
			"escrow", escrowID, "inference", inferenceID, "status", status, "error", err)
	}
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

	req, ok := buildValidateRequest(h, escrowID, inferenceID, epochID)
	if !ok {
		// Inference is not in StatusFinished — session state is inconsistent with
		// the lease. Mark skipped so the loop doesn't cycle on it forever.
		slog.Warn("devshardd: retry: inference not in finished state, skipping",
			"escrow", escrowID, "inference", inferenceID)
		r.markLeaseResult(ctx, escrowID, inferenceID, storage.LeaseStatusSkipped)
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
	owned, err := r.leases.OwnsPendingLease(ctx, escrowID, inferenceID, r.instanceAddr)
	if err != nil {
		return fmt.Errorf("owns pending lease: %w", err)
	}
	if !owned {
		slog.Info("devshardd: retry: lease no longer owned after validate; abandon submit",
			"escrow", escrowID, "inference", inferenceID)
		return nil
	}

	if err := submitValidationToMempool(h, req.InferenceID, result.Valid); err != nil {
		return fmt.Errorf("submit to mempool: %w", err)
	}

	r.markLeaseResult(ctx, escrowID, inferenceID, storage.LeaseStatusSubmitted)
	slog.Info("devshardd: retry: validation submitted",
		"escrow", escrowID, "inference", inferenceID, "valid", result.Valid)
	return nil
}

// buildValidateRequest reconstructs a ValidateRequest from the host's current in-memory
// state snapshot. Returns (req, false) if the inference is not in StatusFinished.
func buildValidateRequest(h hostSnap, escrowID string, inferenceID, epochID uint64) (devshardpkg.ValidateRequest, bool) {
	st := h.SnapshotState()
	rec, ok := st.Inferences[inferenceID]
	if !ok || rec.Status != types.StatusFinished {
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

// submitValidationToMempool signs and inserts a MsgValidation into the host's
// in-memory mempool using exported APIs only (no modification to host.go).
// The tx is delivered to the client in the next HostResponse.Mempool.
func submitValidationToMempool(h *host.Host, inferenceID uint64, valid bool) error {
	msg := &types.MsgValidation{
		InferenceId:   inferenceID,
		ValidatorSlot: h.PrimarySlot(),
		Valid:         valid,
		EscrowId:      h.EscrowID(),
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal MsgValidation: %w", err)
	}
	sig, err := h.Signer().Sign(data)
	if err != nil {
		return fmt.Errorf("sign MsgValidation: %w", err)
	}
	msg.ProposerSig = sig

	h.HostMempool().Add(host.MempoolEntry{
		Tx:         &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: msg}},
		ProposedAt: h.LatestNonce(),
	})
	return nil
}
