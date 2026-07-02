package poc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"decentralized-api/logging"
	"decentralized-api/poc/earlyshare"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

// earlyShareQueryClient is the subset of the inference query client the guard
// needs for capture. Implemented by the client returned from
// CosmosMessageClient.NewInferenceQueryClient().
type earlyShareQueryClient interface {
	AllPoCV2StoreCommitsForStage(ctx context.Context, in *types.QueryAllPoCV2StoreCommitsForStageRequest, opts ...grpc.CallOption) (*types.QueryAllPoCV2StoreCommitsForStageResponse, error)
}

// earlyShareStore is the subset of *earlyshare.Store used by the guard. It is an
// interface so the guard can be unit-tested with a fake store.
type earlyShareStore interface {
	HasCompletedCapture(ctx context.Context, stageHeight int64) (bool, error)
	UpsertCheckpoints(ctx context.Context, checkpoints []earlyshare.Checkpoint) error
	MarkStageCaptured(ctx context.Context, stageHeight int64, target, capturedAt int64, perModelCounts map[string]int) error
	MarkCaptureRun(ctx context.Context, stageHeight int64, modelID string, target, capturedAt int64, count int, status string) error
	GetCheckpoints(ctx context.Context, stageHeight int64) ([]earlyshare.Checkpoint, error)
	GetGuardState(ctx context.Context, participant, modelID string) (earlyshare.GuardState, bool, error)
	UpsertGuardState(ctx context.Context, st earlyshare.GuardState) error
	DeleteStage(ctx context.Context, stageHeight int64) error
}

// EarlyShareGuard ties the early-share config, local store, and the validation
// path together. A nil *EarlyShareGuard is a valid disabled guard.
type EarlyShareGuard struct {
	cfg   earlyshare.Config
	store earlyShareStore
}

// NewEarlyShareGuard builds a guard. Returns nil when the config is disabled or
// the store is nil so callers can treat it as a no-op.
func NewEarlyShareGuard(cfg earlyshare.Config, store earlyShareStore) *EarlyShareGuard {
	cfg = cfg.Normalized()
	if !cfg.Enabled() || store == nil {
		return nil
	}
	return &EarlyShareGuard{
		cfg:   cfg,
		store: store,
	}
}

// Enabled reports whether the guard is active.
func (g *EarlyShareGuard) Enabled() bool {
	return g != nil && g.cfg.Enabled()
}

// FirstFraction returns the configured first-window fraction.
func (g *EarlyShareGuard) FirstFraction() float64 {
	if g == nil {
		return earlyshare.DefaultFirstFraction
	}
	return g.cfg.FirstFraction
}

// DeleteStage prunes early-share rows for a stage. Safe on a nil guard.
func (g *EarlyShareGuard) DeleteStage(ctx context.Context, stageHeight int64) {
	if g == nil || g.store == nil {
		return
	}
	if err := g.store.DeleteStage(ctx, stageHeight); err != nil {
		logging.Warn("EarlyShareGuard: failed to prune stage", types.PoC, "stage", stageHeight, "error", err)
	}
}

// MaybeCapture captures the early checkpoint for a stage if not already done.
// It is idempotent: the exact-match trigger fires at most once per stage, and a
// completed capture (e.g. after a restart) is never repeated.
func (g *EarlyShareGuard) MaybeCapture(ctx context.Context, qc earlyShareQueryClient, stageHeight, target, capturedAt int64) {
	if !g.Enabled() || g.store == nil {
		return
	}

	done, err := g.store.HasCompletedCapture(ctx, stageHeight)
	if err != nil {
		logging.Warn("EarlyShareGuard: capture idempotency check failed", types.PoC, "stage", stageHeight, "error", err)
		return
	}
	if done {
		return
	}

	resp, err := qc.AllPoCV2StoreCommitsForStage(ctx, &types.QueryAllPoCV2StoreCommitsForStageRequest{
		PocStageStartBlockHeight: stageHeight,
	})
	if err != nil {
		logging.Warn("EarlyShareGuard: early capture query failed", types.PoC, "stage", stageHeight, "error", err)
		_ = g.store.MarkCaptureRun(ctx, stageHeight, "", target, capturedAt, 0, earlyshare.StatusFailed)
		return
	}

	checkpoints := make([]earlyshare.Checkpoint, 0, len(resp.Commits))
	perModel := make(map[string]int)
	for _, c := range resp.Commits {
		if c == nil {
			continue
		}
		checkpoints = append(checkpoints, earlyshare.Checkpoint{
			StageHeight:           stageHeight,
			ParticipantAddress:    c.ParticipantAddress,
			ModelID:               c.ModelId,
			EarlyCount:            c.Count,
			EarlyRootHash:         c.RootHash,
			CheckpointBlockHeight: target,
			CapturedAtBlockHeight: capturedAt,
		})
		perModel[c.ModelId]++
	}

	if len(checkpoints) > 0 {
		if err := g.store.UpsertCheckpoints(ctx, checkpoints); err != nil {
			logging.Error("EarlyShareGuard: failed to persist early checkpoints", types.PoC, "stage", stageHeight, "error", err)
			return
		}
	}
	if err := g.store.MarkStageCaptured(ctx, stageHeight, target, capturedAt, perModel); err != nil {
		logging.Error("EarlyShareGuard: failed to mark capture run", types.PoC, "stage", stageHeight, "error", err)
		return
	}
	logging.Info("EarlyShareGuard: captured early checkpoints", types.PoC,
		"stage", stageHeight, "target", target, "capturedAt", capturedAt, "commits", len(checkpoints))
}

// earlyDecision is the precomputed guard outcome for one (participant, model).
type earlyDecision struct {
	shareVoteNo   bool
	requirePrefix bool
	earlyCount    uint32
	earlyRoot     []byte
	finalCount    uint32
	earlyShare    float64
	threshold     float64
}

// guardRuntime carries the per-ValidateAll guard state into the workers.
type guardRuntime struct {
	guard     *EarlyShareGuard
	decisions map[string]earlyDecision
	stage     int64
}

func earlyShareKey(participant, modelID string) string {
	return participant + "|" + modelID
}

// Evaluate computes per-participant guard decisions for a stage and advances the
// miss-streak state for the assigned participants. It returns nil when the guard
// is disabled or the stage cannot be evaluated (fail open).
//
//   - finalCommits: all latest commits for the stage (whole network), used for
//     the weighted median.
//   - modelVotingPowers: established per-model voting power from the validation
//     snapshot (model_id -> participant -> voting power).
//   - assigned: set of earlyShareKey(participant, model) this validator will
//     actually validate; only those get decisions and state updates.
//   - isConfirmation: true when this stage is a confirmation PoC (CPoC). Only a
//     passing CPoC clears the miss streak; a passing regular PoC does not.
func (g *EarlyShareGuard) Evaluate(
	ctx context.Context,
	stageHeight int64,
	isConfirmation bool,
	finalCommits []*types.PoCV2StoreCommitWithAddress,
	modelVotingPowers map[string]map[string]int64,
	assigned map[string]bool,
) map[string]earlyDecision {
	if !g.Enabled() || g.store == nil {
		return nil
	}

	captured, err := g.store.HasCompletedCapture(ctx, stageHeight)
	if err != nil {
		logging.Warn("EarlyShareGuard: capture lookup failed; skipping guard", types.PoC, "stage", stageHeight, "error", err)
		return nil
	}
	if !captured {
		logging.Info("EarlyShareGuard: no early capture for stage; skipping guard", types.PoC, "stage", stageHeight)
		return nil
	}

	checkpoints, err := g.store.GetCheckpoints(ctx, stageHeight)
	if err != nil {
		logging.Warn("EarlyShareGuard: checkpoint load failed; skipping guard", types.PoC, "stage", stageHeight, "error", err)
		return nil
	}
	cpByKey := make(map[string]earlyshare.Checkpoint, len(checkpoints))
	for _, c := range checkpoints {
		cpByKey[earlyShareKey(c.ParticipantAddress, c.ModelID)] = c
	}

	commitsByModel := make(map[string][]*types.PoCV2StoreCommitWithAddress)
	for _, c := range finalCommits {
		if c == nil {
			continue
		}
		commitsByModel[c.ModelId] = append(commitsByModel[c.ModelId], c)
	}

	decisions := make(map[string]earlyDecision)

	for modelID, commits := range commitsByModel {
		vp := modelVotingPowers[modelID]
		var totalVP int64
		for _, w := range vp {
			totalVP += w
		}
		if len(vp) == 0 || totalVP <= 0 {
			// No established weighting data for this model: skip (fail open).
			logging.Info("EarlyShareGuard: no voting power for model; skipping guard for model", types.PoC,
				"stage", stageHeight, "model", modelID)
			continue
		}

		type participantData struct {
			finalCount    uint32
			earlyCount    uint32
			earlyRoot     []byte
			share         float64
			requirePrefix bool
			shareFail     bool
			excluded      bool
		}
		pdata := make(map[string]participantData, len(commits))
		points := make([]earlyshare.SharePoint, 0, len(commits))

		for _, commit := range commits {
			addr := commit.ParticipantAddress
			fc := commit.Count
			if fc == 0 {
				pdata[addr] = participantData{excluded: true}
				continue
			}
			cp, hasCP := cpByKey[earlyShareKey(addr, modelID)]
			d := participantData{finalCount: fc}
			switch {
			case !hasCP:
				// Present in final, absent from early snapshot -> early_share 0.
				d.earlyCount = 0
				d.share = 0
				points = append(points, earlyshare.SharePoint{Share: 0, Weight: vp[addr]})
			case len(cp.EarlyRootHash) == 0:
				// Unusable captured data: drop from distribution and skip this
				// participant entirely (fail open for them).
				d.excluded = true
			case cp.EarlyCount > fc:
				// Invalid: early exceeds final. Not a data point. Share fails;
				// prefix proof unavailable.
				d.earlyCount = cp.EarlyCount
				d.earlyRoot = cp.EarlyRootHash
				d.shareFail = true
			default:
				d.earlyCount = cp.EarlyCount
				d.earlyRoot = cp.EarlyRootHash
				d.share = float64(cp.EarlyCount) / float64(fc)
				d.requirePrefix = g.cfg.RequirePrefixProof && cp.EarlyCount > 0
				points = append(points, earlyshare.SharePoint{Share: d.share, Weight: vp[addr]})
			}
			pdata[addr] = d
		}

		median, ok := earlyshare.WeightedMedianShare(points)
		if !ok {
			logging.Info("EarlyShareGuard: no positive-weight data points; skipping guard for model", types.PoC,
				"stage", stageHeight, "model", modelID)
			continue
		}
		threshold := median * g.cfg.ThresholdRatio

		for addr, d := range pdata {
			key := earlyShareKey(addr, modelID)
			if !assigned[key] || d.excluded {
				continue
			}

			passed := !d.shareFail && d.share >= threshold

			prev, _, err := g.store.GetGuardState(ctx, addr, modelID)
			if err != nil {
				logging.Warn("EarlyShareGuard: guard-state load failed; skipping participant", types.PoC,
					"stage", stageHeight, "participant", addr, "model", modelID, "error", err)
				continue
			}
			outcome := earlyshare.ApplyMissStreak(prev, passed, isConfirmation, stageHeight)
			if err := g.store.UpsertGuardState(ctx, outcome.NewState); err != nil {
				logging.Warn("EarlyShareGuard: guard-state save failed", types.PoC,
					"stage", stageHeight, "participant", addr, "model", modelID, "error", err)
			}

			// Log every low-early-share miss as it happens, including the first
			// one that is still within grace (does not yet vote no). This makes
			// observe mode surface low early shares early instead of staying
			// silent until the miss streak trips. shareFail marks the invalid
			// early>final case where the share is not a usable data point.
			if !passed {
				logging.Info("EarlyShareGuard: low early share miss", types.PoC,
					"stage", stageHeight,
					"participant", addr,
					"modelId", modelID,
					"earlyShare", d.share,
					"threshold", threshold,
					"shareFail", d.shareFail,
					"consecutiveMisses", outcome.NewState.ConsecutiveMisses,
					"isConfirmation", isConfirmation,
					"wouldVoteNo", outcome.VoteNo,
					"enforcing", g.cfg.Enforcing())
			}

			decisions[key] = earlyDecision{
				shareVoteNo:   outcome.VoteNo,
				requirePrefix: d.requirePrefix,
				earlyCount:    d.earlyCount,
				earlyRoot:     d.earlyRoot,
				finalCount:    d.finalCount,
				earlyShare:    d.share,
				threshold:     threshold,
			}
		}
	}

	return decisions
}

// decide combines the prefix-proof check (immediate, no grace) with the
// precomputed low-early-share decision (miss-streak gated). It returns whether
// the participant should be voted no and a human-readable reason.
func (g *EarlyShareGuard) decide(ctx context.Context, pf proofFetcher, stage int64, work participantWork, dec earlyDecision) (bool, string) {
	if dec.requirePrefix {
		if ok, reason := g.checkPrefix(ctx, pf, stage, work, dec); !ok {
			return true, "prefix_mismatch:" + reason
		}
	}
	if dec.shareVoteNo {
		return true, fmt.Sprintf("low_early_share early_share=%.4f threshold=%.4f", dec.earlyShare, dec.threshold)
	}
	return false, ""
}

// checkPrefix verifies that a single shared leaf proves identically against both
// the early and final commitments. A cryptographic mismatch (or the early root
// being unable to prove the shared leaf) is a hard failure. Transient/network
// errors are treated as "cannot determine" and do not fail the participant.
func (g *EarlyShareGuard) checkPrefix(ctx context.Context, pf proofFetcher, stage int64, work participantWork, dec earlyDecision) (bool, string) {
	if dec.earlyCount == 0 || len(dec.earlyRoot) == 0 {
		return true, "" // nothing to compare; handled elsewhere
	}
	sharedLeaf := deterministicSharedLeaf(work.address, work.modelId, stage, dec.earlyCount)

	finalProofs, err := pf.FetchAndVerifyProofs(ctx, work.url, ProofRequest{
		PocStageStartBlockHeight: stage,
		ModelId:                  work.modelId,
		RootHash:                 work.rootHash,
		Count:                    work.count,
		LeafIndices:              []uint32{sharedLeaf},
		ParticipantAddress:       work.address,
	})
	if err != nil || len(finalProofs) != 1 {
		// Final side could not be fetched/verified for the shared leaf. The main
		// validation path already vets the final commitment, so do not fail the
		// participant on a transient hiccup here.
		logging.Debug("EarlyShareGuard: final shared-leaf proof unavailable", types.PoC,
			"participant", work.address, "leaf", sharedLeaf, "error", err)
		return true, ""
	}

	earlyProofs, err := pf.FetchAndVerifyProofs(ctx, work.url, ProofRequest{
		PocStageStartBlockHeight: stage,
		ModelId:                  work.modelId,
		RootHash:                 dec.earlyRoot,
		Count:                    dec.earlyCount,
		LeafIndices:              []uint32{sharedLeaf},
		ParticipantAddress:       work.address,
	})
	if err != nil {
		if isPermanentProofError(err) {
			// Early root cannot prove the shared leaf -> hard failure.
			return false, fmt.Sprintf("early_proof_invalid leaf=%d: %v", sharedLeaf, err)
		}
		logging.Debug("EarlyShareGuard: early shared-leaf proof unavailable (transient)", types.PoC,
			"participant", work.address, "leaf", sharedLeaf, "error", err)
		return true, ""
	}
	if len(earlyProofs) != 1 {
		return false, fmt.Sprintf("early_proof_missing leaf=%d", sharedLeaf)
	}

	fa := finalProofs[0]
	ea := earlyProofs[0]
	if fa.LeafIndex != ea.LeafIndex {
		return false, fmt.Sprintf("leaf_index mismatch final=%d early=%d", fa.LeafIndex, ea.LeafIndex)
	}
	if fa.Nonce != ea.Nonce {
		return false, fmt.Sprintf("nonce mismatch final=%d early=%d", fa.Nonce, ea.Nonce)
	}
	if fa.VectorB64 != ea.VectorB64 {
		return false, "vector mismatch"
	}
	return true, ""
}

func isPermanentProofError(err error) bool {
	return errors.Is(err, ErrProofVerificationFailed) ||
		errors.Is(err, ErrIncompleteCoverage) ||
		errors.Is(err, ErrInvalidVectorData)
}

// deterministicSharedLeaf picks a stable shared leaf index in [0, earlyCount)
// derived from (participant, model, stage). Determinism keeps the choice stable
// across retries.
func deterministicSharedLeaf(participant, modelID string, stage int64, earlyCount uint32) uint32 {
	if earlyCount == 0 {
		return 0
	}
	seed := fmt.Sprintf("early-share:%s:%s:%d", participant, modelID, stage)
	sum := sha256.Sum256([]byte(seed))
	v := binary.BigEndian.Uint64(sum[:8])
	return uint32(v % uint64(earlyCount))
}
