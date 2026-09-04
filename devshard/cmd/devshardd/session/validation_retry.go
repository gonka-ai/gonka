package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"common/chain"
	"google.golang.org/protobuf/proto"

	devshardpkg "devshard"
	"devshard/host"
	"devshard/storage"
	"devshard/types"
)

// sessionManager abstracts *HostManager for testing.
type sessionManager interface {
	ActiveEscrowIDs() []string
	hostSnapshot(escrowID string) (hostSnap, bool)
}

// staleLeaseStore abstracts storage.LeaseStore for testing.
type staleLeaseStore interface {
	AcquireOneStale(ctx context.Context, escrowID, instanceAddr string, ttl time.Duration) (uint64, uint64, error)
	SetResult(ctx context.Context, escrowID string, inferenceID, epochID uint64, status storage.LeaseStatus, instanceAddr string) error
	OwnsPendingLease(ctx context.Context, escrowID string, inferenceID, epochID uint64, instanceAddr string) (bool, error)
	Release(ctx context.Context, escrowID string, inferenceID, epochID uint64, instanceAddr string) error
}

// hostSnap abstracts *host.Host state reads for testing.
type hostSnap interface {
	SnapshotState() types.EscrowState
	Group() []types.SlotAssignment
}

const (
	DefaultValidationRetryInterval = 5 * time.Minute
	DefaultValidationLeaseTTL      = 30 * time.Minute
)

// ValidationRetryLoop scans for stale validation leases and re-runs validation for each
// active in-memory session. A lease is stale when status is pending/submitted and
// claimed_at < now() - leaseTTL (default 30m). FOR UPDATE SKIP LOCKED in the
// underlying query ensures concurrent instances each pick a different row.
type ValidationRetryLoop struct {
	leases       staleLeaseStore
	inner        devshardpkg.ValidationEngine // no lease wrapping: lease already held
	manager      sessionManager
	phase        *chain.Phase
	instanceAddr string
	leaseTTL     time.Duration
	interval     time.Duration
}

// NewValidationRetryLoop recovers stale Postgres validation leases for loaded
// sessions. inner must be a Validator without lease wrapping: this loop already
// holds the lease via AcquireOneStale.
func NewValidationRetryLoop(
	leases storage.LeaseStore,
	inner devshardpkg.ValidationEngine,
	manager *HostManager,
	phase *chain.Phase,
	instanceAddr string,
) *ValidationRetryLoop {
	return &ValidationRetryLoop{
		leases:       leases,
		inner:        inner,
		manager:      manager,
		phase:        phase,
		instanceAddr: instanceAddr,
		leaseTTL:     DefaultValidationLeaseTTL,
		interval:     DefaultValidationRetryInterval,
	}
}

// WithInterval overrides the default retry interval. Used in tests and config-driven tuning.
func (r *ValidationRetryLoop) WithInterval(d time.Duration) *ValidationRetryLoop {
	r.interval = d
	return r
}

// WithLeaseTTL overrides the default lease TTL. Used in tests and config-driven tuning.
func (r *ValidationRetryLoop) WithLeaseTTL(d time.Duration) *ValidationRetryLoop {
	r.leaseTTL = d
	return r
}

// Run starts the retry ticker. Blocks until ctx is cancelled.
func (r *ValidationRetryLoop) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.retryStaleValidationsOnce(ctx)
		}
	}
}

func (r *ValidationRetryLoop) retryStaleValidationsOnce(ctx context.Context) {
	for _, escrowID := range r.manager.ActiveEscrowIDs() {
		r.retryStaleValidationsForEscrow(ctx, escrowID)
	}
}

// retryStaleValidationsForEscrow claims stale validation leases for this escrow
// until AcquireOneStale returns none.
func (r *ValidationRetryLoop) retryStaleValidationsForEscrow(ctx context.Context, escrowID string) {
	caughtUp := false
	for {
		// Don't claim work this process cannot do. The snapshot can still
		// unload between this check and AcquireOneStale; retryStaleValidation
		// releases if that happens.
		snap, ok := r.manager.hostSnapshot(escrowID)
		if !ok {
			return
		}
		if !caughtUp {
			if live, ok := snap.(*host.Host); ok {
				if err := live.CatchUpFromStore(ctx); err != nil {
					slog.Warn("devshardd: validation retry: catch-up from store failed",
						"escrow", escrowID, "error", err)
				}
			}
			caughtUp = true
		}

		inferenceID, leaseEpochID, err := r.leases.AcquireOneStale(ctx, escrowID, r.instanceAddr, r.leaseTTL)
		if err != nil {
			slog.Warn("devshardd: validation retry: acquire stale validation failed",
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
			slog.Info("devshardd: validation retry: epoch stale, skipping validation",
				"escrow", escrowID, "inference", inferenceID,
				"lease_epoch", leaseEpochID, "current_epoch", r.phase.EpochID())
			r.markLeaseResult(ctx, escrowID, inferenceID, leaseEpochID, storage.LeaseStatusSkipped)
			continue
		}

		if err := r.retryStaleValidation(ctx, escrowID, inferenceID, leaseEpochID); err != nil {
			slog.Warn("devshardd: validation retry: validation failed",
				"escrow", escrowID, "inference", inferenceID, "error", err)
		}
	}
}

func (r *ValidationRetryLoop) markLeaseResult(ctx context.Context, escrowID string, inferenceID, epochID uint64, status storage.LeaseStatus) {
	if err := r.leases.SetResult(ctx, escrowID, inferenceID, epochID, status, r.instanceAddr); err != nil {
		if errors.Is(err, storage.ErrLeaseNotOwned) {
			slog.Info("devshardd: validation retry: mark result skipped; lease not owned",
				"escrow", escrowID, "inference", inferenceID, "status", status)
			return
		}
		slog.Warn("devshardd: validation retry: mark result failed",
			"escrow", escrowID, "inference", inferenceID, "status", status, "error", err)
	}
}

func (r *ValidationRetryLoop) releaseOwnedLease(ctx context.Context, escrowID string, inferenceID, epochID uint64) {
	if err := r.leases.Release(ctx, escrowID, inferenceID, epochID, r.instanceAddr); err != nil {
		slog.Warn("devshardd: validation retry: release lease failed",
			"escrow", escrowID, "inference", inferenceID, "error", err)
	}
}

// retryStaleValidation reconstructs a ValidateRequest from in-memory session state, runs
// validation via the inner engine, submits the result to the host's mempool,
// and marks the lease submitted. Stale submitted leases remain retryable because
// the mempool tx is not durable until it is applied as a diff. Challenged
// inferences are released so the hot path can publish MsgValidationVote.
func (r *ValidationRetryLoop) retryStaleValidation(ctx context.Context, escrowID string, inferenceID, epochID uint64) error {
	acquiredAt := time.Now()
	h, ok := r.manager.hostSnapshot(escrowID)
	if !ok {
		r.releaseOwnedLease(ctx, escrowID, inferenceID, epochID)
		if ctx.Err() != nil {
			return fmt.Errorf("session %s not loaded: %w", escrowID, ctx.Err())
		}
		return fmt.Errorf("session %s not loaded", escrowID)
	}

	req, status, found := lookupValidateTarget(h, escrowID, inferenceID, epochID)
	if !found || (status != types.StatusFinished && status != types.StatusChallenged) {
		slog.Warn("devshardd: validation retry: inference not in finished or challenged state, skipping",
			"escrow", escrowID, "inference", inferenceID, "found", found, "status", status)
		r.markLeaseResult(ctx, escrowID, inferenceID, epochID, storage.LeaseStatusSkipped)
		return nil
	}
	if status == types.StatusChallenged {
		slog.Info("devshardd: validation retry: challenged inference, releasing lease for hot path",
			"escrow", escrowID, "inference", inferenceID)
		r.releaseOwnedLease(ctx, escrowID, inferenceID, epochID)
		return nil
	}

	liveHost, isLiveHost := h.(*host.Host)
	if isLiveHost {
		if validationAlreadyAppliedByHost(liveHost, inferenceID) {
			slog.Info("devshardd: validation retry: validation already applied, skipping",
				"escrow", escrowID, "inference", inferenceID)
			r.markLeaseResult(ctx, escrowID, inferenceID, epochID, storage.LeaseStatusSkipped)
			return nil
		}
		if validationAlreadyInMempool(liveHost, inferenceID) {
			slog.Info("devshardd: validation retry: validation already in mempool",
				"escrow", escrowID, "inference", inferenceID)
			r.markLeaseResult(ctx, escrowID, inferenceID, epochID, storage.LeaseStatusSubmitted)
			return nil
		}
	}

	result, err := r.inner.Validate(ctx, req)
	if err != nil {
		r.releaseOwnedLease(ctx, escrowID, inferenceID, epochID)
		return fmt.Errorf("validate: %w", err)
	}

	if time.Since(acquiredAt) > r.leaseTTL {
		slog.Info("devshardd: validation retry: lease TTL exceeded after validate; releasing",
			"escrow", escrowID, "inference", inferenceID, "lease_ttl", r.leaseTTL)
		r.releaseOwnedLease(ctx, escrowID, inferenceID, epochID)
		return nil
	}
	owned, err := r.leases.OwnsPendingLease(ctx, escrowID, inferenceID, epochID, r.instanceAddr)
	if err != nil {
		r.releaseOwnedLease(ctx, escrowID, inferenceID, epochID)
		return fmt.Errorf("owns pending lease: %w", err)
	}
	if !owned {
		slog.Info("devshardd: validation retry: lease no longer owned after validate; abandon submit",
			"escrow", escrowID, "inference", inferenceID)
		return nil
	}

	if !isLiveHost {
		r.releaseOwnedLease(ctx, escrowID, inferenceID, epochID)
		return fmt.Errorf("submit to mempool: host snapshot is not *host.Host")
	}
	if err := submitValidationToMempool(liveHost, req.InferenceID, result.Valid); err != nil {
		r.releaseOwnedLease(ctx, escrowID, inferenceID, epochID)
		return fmt.Errorf("submit to mempool: %w", err)
	}

	r.markLeaseResult(ctx, escrowID, inferenceID, epochID, storage.LeaseStatusSubmitted)
	slog.Info("devshardd: validation retry: validation submitted",
		"escrow", escrowID, "inference", inferenceID, "valid", result.Valid)
	return nil
}

func validationAlreadyAppliedByHost(h *host.Host, inferenceID uint64) bool {
	st := h.SnapshotState()
	rec, ok := st.Inferences[inferenceID]
	if !ok {
		return false
	}
	for slot := range h.SlotIDs() {
		if rec.ValidatedBy.IsSet(slot) {
			return true
		}
	}
	return false
}

func validationAlreadyInMempool(h *host.Host, inferenceID uint64) bool {
	for _, tx := range h.MempoolTxs() {
		if v := tx.GetValidation(); v != nil && v.GetInferenceId() == inferenceID {
			if h.SlotIDs()[v.GetValidatorSlot()] {
				return true
			}
		}
		if v := tx.GetValidationVote(); v != nil && v.GetInferenceId() == inferenceID {
			if h.SlotIDs()[v.GetVoterSlot()] {
				return true
			}
		}
	}
	return false
}

// lookupValidateTarget reconstructs a ValidateRequest from the host's current
// in-memory state snapshot. found is false when the inference is absent.
func lookupValidateTarget(h hostSnap, escrowID string, inferenceID, epochID uint64) (devshardpkg.ValidateRequest, types.InferenceStatus, bool) {
	st := h.SnapshotState()
	rec, ok := st.Inferences[inferenceID]
	if !ok {
		return devshardpkg.ValidateRequest{}, 0, false
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
	}, rec.Status, true
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
