package poc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"common/logging"
	"decentralized-api/poc/earlyshare"

	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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

	// captureMu collapses concurrent capture attempts for the retry window
	// (the dispatcher may fire MaybeCapture on every block past the target).
	captureMu sync.Mutex
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
// It is idempotent: a completed capture (e.g. after a restart) is never
// repeated, and concurrent attempts collapse to one.
//
// The query is pinned to the target block height (x-cosmos-block-height), so
// the captured snapshot is the consensus state at exactly that height. Every
// validator capturing the same stage therefore records identical checkpoints,
// regardless of when in the window its capture actually runs — the capture can
// be retried on later blocks and still lands on the same snapshot, as long as
// the queried node retains state for the target height.
func (g *EarlyShareGuard) MaybeCapture(ctx context.Context, qc earlyShareQueryClient, stageHeight, target, capturedAt int64) {
	if !g.Enabled() || g.store == nil {
		return
	}
	if !g.captureMu.TryLock() {
		return // another capture attempt for this process is already running
	}
	defer g.captureMu.Unlock()

	done, err := g.store.HasCompletedCapture(ctx, stageHeight)
	if err != nil {
		logging.Warn("EarlyShareGuard: capture idempotency check failed", types.PoC, "stage", stageHeight, "error", err)
		return
	}
	if done {
		return
	}

	// Pin the query to the target height so all validators see the same state.
	queryCtx := metadata.AppendToOutgoingContext(ctx,
		grpctypes.GRPCBlockHeightHeader, strconv.FormatInt(target, 10))
	queryCtx, cancel := context.WithTimeout(queryCtx, 30*1e9) // 30s
	defer cancel()

	resp, err := qc.AllPoCV2StoreCommitsForStage(queryCtx, &types.QueryAllPoCV2StoreCommitsForStageRequest{
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
	// The digest is a deterministic hash of the whole captured snapshot.
	// Because the capture query is height-pinned, every validator capturing
	// this stage must log the same digest; differing digests across
	// validators indicate capture divergence (version skew, pruned state)
	// and are the primary observe-mode health signal for the guard.
	logging.Info("EarlyShareGuard: captured early checkpoints", types.PoC,
		"stage", stageHeight, "target", target, "capturedAt", capturedAt,
		"commits", len(checkpoints), "digest", checkpointsDigest(checkpoints))
}

// checkpointsDigest computes an order-independent digest of a captured
// checkpoint set: entries are sorted by (participant, model) and hashed.
func checkpointsDigest(checkpoints []earlyshare.Checkpoint) string {
	sorted := make([]earlyshare.Checkpoint, len(checkpoints))
	copy(sorted, checkpoints)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ParticipantAddress != sorted[j].ParticipantAddress {
			return sorted[i].ParticipantAddress < sorted[j].ParticipantAddress
		}
		return sorted[i].ModelID < sorted[j].ModelID
	})

	h := sha256.New()
	for _, cp := range sorted {
		fmt.Fprintf(h, "%s|%s|%d|%x\n", cp.ParticipantAddress, cp.ModelID, cp.EarlyCount, cp.EarlyRootHash)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

// earlyDecision is the precomputed guard outcome for one (participant, model).
type earlyDecision struct {
	shareVoteNo      bool
	requireInclusion bool
	earlyCount       uint32
	earlyRoot        []byte
	finalCount       uint32
	earlyShare       float64
	threshold        float64
}

// guardRuntime carries the per-ValidateAll guard state into the workers.
type guardRuntime struct {
	guard             *EarlyShareGuard
	decisions         map[string]earlyDecision
	stage             int64
	validatorPubKey   string
	samplingBlockHash string
}

type earlyGuardOutcome int

const (
	earlyGuardPass earlyGuardOutcome = iota
	earlyGuardVoteNo
	earlyGuardRetry
)

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
			finalCount       uint32
			earlyCount       uint32
			earlyRoot        []byte
			share            float64
			requireInclusion bool
			shareFail        bool
			excluded         bool
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
				// inclusion proof unavailable.
				d.earlyCount = cp.EarlyCount
				d.earlyRoot = cp.EarlyRootHash
				d.shareFail = true
			default:
				d.earlyCount = cp.EarlyCount
				d.earlyRoot = cp.EarlyRootHash
				d.share = float64(cp.EarlyCount) / float64(fc)
				d.requireInclusion = g.cfg.RequireInclusionProof && cp.EarlyCount > 0
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
				shareVoteNo:      outcome.VoteNo,
				requireInclusion: d.requireInclusion,
				earlyCount:       d.earlyCount,
				earlyRoot:        d.earlyRoot,
				finalCount:       d.finalCount,
				earlyShare:       d.share,
				threshold:        threshold,
			}
		}
	}

	return decisions
}

// decide combines the inclusion check (immediate, no grace) with the precomputed
// low-early-share decision (miss-streak gated).
func (g *EarlyShareGuard) decide(
	ctx context.Context,
	pf proofFetcher,
	stage int64,
	work participantWork,
	dec earlyDecision,
	validatorPubKey string,
	samplingBlockHash string,
) (earlyGuardOutcome, string) {
	if dec.requireInclusion {
		outcome, reason := g.checkInclusion(ctx, pf, stage, work, dec, validatorPubKey, samplingBlockHash)
		if outcome != earlyGuardPass {
			return outcome, reason
		}
	}
	if dec.shareVoteNo {
		return earlyGuardVoteNo, fmt.Sprintf("low_early_share early_share=%.4f threshold=%.4f", dec.earlyShare, dec.threshold)
	}
	return earlyGuardPass, ""
}

// checkInclusion verifies that sampled early artifacts are present unchanged in
// the final commitment. Cryptographic mismatches are hard failures; transient
// network errors ask the validator to retry in enforce mode.
func (g *EarlyShareGuard) checkInclusion(
	ctx context.Context,
	pf proofFetcher,
	stage int64,
	work participantWork,
	dec earlyDecision,
	validatorPubKey string,
	samplingBlockHash string,
) (earlyGuardOutcome, string) {
	if dec.earlyCount == 0 || len(dec.earlyRoot) == 0 {
		return earlyGuardPass, "" // nothing to compare; handled elsewhere
	}
	leafIndices := sampleLeafIndices(
		validatorPubKey,
		samplingBlockHash,
		stage,
		work.modelId+"|early-inclusion",
		dec.earlyCount,
		g.cfg.InclusionSampleSize,
	)
	if len(leafIndices) == 0 {
		return earlyGuardPass, ""
	}

	earlyProofs, err := pf.FetchAndVerifyProofs(ctx, work.url, ProofRequest{
		PocStageStartBlockHeight: stage,
		ModelId:                  work.modelId,
		RootHash:                 dec.earlyRoot,
		Count:                    dec.earlyCount,
		LeafIndices:              leafIndices,
		ParticipantAddress:       work.address,
	})
	if err != nil {
		if isPermanentProofError(err) {
			return earlyGuardVoteNo, fmt.Sprintf("inclusion_early_proof_invalid: %v", err)
		}
		logging.Debug("EarlyShareGuard: early inclusion proof unavailable (transient)", types.PoC,
			"participant", work.address, "error", err)
		return earlyGuardRetry, fmt.Sprintf("inclusion_early_proof_unavailable: %v", err)
	}
	// Coverage, duplicate response items, and proof validity are enforced by the
	// proof fetchers. The guard only checks the cross-root invariant: same nonce,
	// same vector in the final tree.
	earlyByNonce := make(map[int32]VerifiedArtifact, len(earlyProofs))
	nonces := make([]int32, 0, len(earlyProofs))
	for _, artifact := range earlyProofs {
		earlyByNonce[artifact.Nonce] = artifact
		nonces = append(nonces, artifact.Nonce)
	}

	finalProofs, err := pf.FetchAndVerifyProofsByNonce(ctx, work.url, ProofByNonceRequest{
		PocStageStartBlockHeight: stage,
		ModelId:                  work.modelId,
		RootHash:                 work.rootHash,
		Count:                    work.count,
		Nonces:                   nonces,
		ParticipantAddress:       work.address,
	})
	if err != nil {
		if isPermanentProofError(err) {
			return earlyGuardVoteNo, fmt.Sprintf("inclusion_final_proof_invalid: %v", err)
		}
		logging.Debug("EarlyShareGuard: final by-nonce inclusion proof unavailable (transient)", types.PoC,
			"participant", work.address, "error", err)
		return earlyGuardRetry, fmt.Sprintf("inclusion_final_proof_unavailable: %v", err)
	}

	for _, finalArtifact := range finalProofs {
		earlyArtifact := earlyByNonce[finalArtifact.Nonce]
		if finalArtifact.VectorB64 != earlyArtifact.VectorB64 {
			return earlyGuardVoteNo, fmt.Sprintf("inclusion_vector_mismatch nonce=%d", finalArtifact.Nonce)
		}
	}

	return earlyGuardPass, ""
}

func isPermanentProofError(err error) bool {
	return errors.Is(err, ErrProofVerificationFailed) ||
		errors.Is(err, ErrIncompleteCoverage) ||
		errors.Is(err, ErrNonceAbsent) ||
		errors.Is(err, ErrInvalidVectorData)
}
