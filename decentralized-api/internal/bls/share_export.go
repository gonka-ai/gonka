package bls

// Block BLS beacon, Step 2: export this participant's recovered slot shares to
// the chain node (devshard/docs/proposals/block-bls-beacon-vote-extensions-plan.md).
//
// The node signs per-block beacon partials inside ExtendVote, which must not
// call out to the dAPI, so the dAPI writes the shares to a file in the node
// home both containers mount (<home>/bls-beacon/epoch-<N>.json, mode 0600).
// The node checks them against committed state before use.
//
// Two triggers:
//   - storeVerificationResult: every completed (COMPLETED/SIGNED) result with a
//     group key is exported right away (DKG completion event and recovery).
//   - a background reconcile loop: for the current and next epoch, if the file
//     is missing it recovers the result from chain (GetOrRecoverVerificationResult)
//     and exports it. This covers dAPI restarts and deleted files.

import (
	"context"
	"decentralized-api/apiconfig"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"common/logging"

	"github.com/productscience/inference/app/beacon/sharefile"
	"github.com/productscience/inference/x/bls/types"
	inferenceTypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	beaconExportLogTag = "BLS beacon export: "
	// DefaultBeaconReconcileInterval is how often the exporter checks that the
	// current and next epoch's files exist.
	DefaultBeaconReconcileInterval = 30 * time.Second
	// beaconRecoverBackoff limits chain recovery attempts per epoch when the
	// file cannot be produced (not a participant, DKG not final yet).
	beaconRecoverBackoff = 2 * time.Minute
)

// beaconExportDeps are the chain-facing calls the exporter needs; tests replace them.
type beaconExportDeps struct {
	chainID      func(ctx context.Context) (string, error)
	currentEpoch func(ctx context.Context) (uint64, error)
	participant  func() string
	recover      func(epochID uint64) (*VerificationResult, error)
	now          func() time.Time
}

type beaconExporter struct {
	mu          sync.Mutex
	enabled     bool
	dir         string
	deps        beaconExportDeps
	chainID     string
	lastAttempt map[uint64]time.Time
}

// ConfigureBeaconShareExport turns on the share export with cfg. Call before
// the event listener starts. A disabled config leaves export off.
func (bm *BlsManager) ConfigureBeaconShareExport(cfg apiconfig.BlsBeaconConfig) {
	deps := beaconExportDeps{
		chainID: func(ctx context.Context) (string, error) {
			st, err := bm.cosmosClient.Status(ctx)
			if err != nil {
				return "", err
			}
			return st.NodeInfo.Network, nil
		},
		currentEpoch: func(ctx context.Context) (uint64, error) {
			res, err := bm.cosmosClient.NewInferenceQueryClient().GetCurrentEpoch(ctx, &inferenceTypes.QueryGetCurrentEpochRequest{})
			if err != nil {
				return 0, err
			}
			return res.Epoch, nil
		},
		participant: bm.cosmosClient.GetAccountAddress,
		recover:     bm.GetOrRecoverVerificationResult,
		now:         time.Now,
	}
	bm.configureBeaconExport(!cfg.ExportDisabled, cfg.EffectiveShareDir(), deps)
}

func (bm *BlsManager) configureBeaconExport(enabled bool, dir string, deps beaconExportDeps) {
	if deps.now == nil {
		deps.now = time.Now
	}
	bm.beacon.mu.Lock()
	defer bm.beacon.mu.Unlock()
	bm.beacon.enabled = enabled
	bm.beacon.dir = dir
	bm.beacon.deps = deps
	bm.beacon.lastAttempt = make(map[uint64]time.Time)
	if enabled {
		logging.Info(beaconExportLogTag+"enabled", inferenceTypes.BLS, "dir", dir)
	} else {
		logging.Info(beaconExportLogTag+"disabled by config", inferenceTypes.BLS)
	}
}

func (bm *BlsManager) beaconExportEnabled() (bool, string) {
	bm.beacon.mu.Lock()
	defer bm.beacon.mu.Unlock()
	return bm.beacon.enabled, bm.beacon.dir
}

// beaconChainID returns the chain id, cached after the first success.
func (bm *BlsManager) beaconChainID(ctx context.Context) (string, error) {
	bm.beacon.mu.Lock()
	cached, fn := bm.beacon.chainID, bm.beacon.deps.chainID
	bm.beacon.mu.Unlock()
	if cached != "" {
		return cached, nil
	}
	if fn == nil {
		return "", errors.New("chain id source not configured")
	}
	id, err := fn(ctx)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("empty chain id")
	}
	bm.beacon.mu.Lock()
	bm.beacon.chainID = id
	bm.beacon.mu.Unlock()
	return id, nil
}

// beaconExportable reports whether result holds final shares worth exporting.
func beaconExportable(result *VerificationResult) bool {
	if result == nil || !result.IsParticipant {
		return false
	}
	if result.DkgPhase != types.DKGPhase_DKG_PHASE_COMPLETED && result.DkgPhase != types.DKGPhase_DKG_PHASE_SIGNED {
		return false
	}
	return len(result.GroupPublicKey) == sharefile.GroupPublicKeyLen
}

// exportBeaconShares writes result's shares if it is a final participant
// result. It returns whether a file was (re)written. Shares are never logged.
func (bm *BlsManager) exportBeaconShares(result *VerificationResult) (bool, error) {
	enabled, dir := bm.beaconExportEnabled()
	if !enabled || !beaconExportable(result) {
		return false, nil
	}
	if err := bm.ensureConsensusSharesComplete(result); err != nil {
		return false, fmt.Errorf("shares incomplete: %w", err)
	}
	if result.SlotRange[1] < result.SlotRange[0] {
		return false, fmt.Errorf("invalid slot range %v", result.SlotRange)
	}
	if n := int(result.SlotRange[1]-result.SlotRange[0]) + 1; len(result.AggregatedShares) != n {
		return false, fmt.Errorf("%d aggregated shares for %d slots", len(result.AggregatedShares), n)
	}

	ctx, cancel := context.WithTimeout(bm.ctx, 10*time.Second)
	defer cancel()
	chainID, err := bm.beaconChainID(ctx)
	if err != nil {
		return false, fmt.Errorf("chain id: %w", err)
	}

	bm.beacon.mu.Lock()
	deps := bm.beacon.deps
	bm.beacon.mu.Unlock()
	participant := ""
	if deps.participant != nil {
		participant = deps.participant()
	}

	shares := make([][sharefile.ScalarLen]byte, len(result.AggregatedShares))
	for i := range result.AggregatedShares {
		shares[i] = result.AggregatedShares[i].Bytes()
	}
	file, err := sharefile.New(chainID, result.EpochID, participant, result.SlotRange[0], result.SlotRange[1],
		result.GroupPublicKey, shares, deps.now())
	if err != nil {
		return false, err
	}
	path, written, err := sharefile.WriteAtomic(dir, file)
	if err != nil {
		return false, err
	}
	if written {
		logging.Info(beaconExportLogTag+"beacon shares exported", inferenceTypes.BLS,
			"epoch", result.EpochID,
			"slots", fmt.Sprintf("[%d,%d]", result.SlotRange[0], result.SlotRange[1]),
			"path", path)
	}
	if result.EpochID >= 2 {
		bm.pruneBeaconShares(dir, result.EpochID-2)
	}
	return written, nil
}

func (bm *BlsManager) pruneBeaconShares(dir string, keepFrom uint64) {
	removed, err := sharefile.Prune(dir, keepFrom)
	if err != nil {
		logging.Warn(beaconExportLogTag+"prune failed", inferenceTypes.BLS, "dir", dir, "err", err)
	}
	if len(removed) > 0 {
		logging.Info(beaconExportLogTag+"pruned old share files", inferenceTypes.BLS, "epochs", removed)
	}
}

// maybeExportBeaconShares is the storeVerificationResult hook: failures are
// logged, never propagated (export must not affect DKG or signing).
func (bm *BlsManager) maybeExportBeaconShares(result *VerificationResult) {
	defer func() {
		if r := recover(); r != nil {
			logging.Error(beaconExportLogTag+"panic during export", inferenceTypes.BLS, "panic", r)
		}
	}()
	if _, err := bm.exportBeaconShares(result); err != nil {
		logging.Warn(beaconExportLogTag+"export failed", inferenceTypes.BLS, "epoch", result.EpochID, "err", err)
	}
}

// StartBeaconShareExport runs the reconcile loop until ctx is done.
func (bm *BlsManager) StartBeaconShareExport(ctx context.Context, interval time.Duration) {
	if enabled, _ := bm.beaconExportEnabled(); !enabled {
		return
	}
	if interval <= 0 {
		interval = DefaultBeaconReconcileInterval
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			bm.reconcileBeaconShares(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// reconcileBeaconShares makes sure the current and next epoch have a valid
// share file when this node is a participant, and prunes older files.
func (bm *BlsManager) reconcileBeaconShares(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			logging.Error(beaconExportLogTag+"panic during reconcile", inferenceTypes.BLS, "panic", r)
		}
	}()
	enabled, dir := bm.beaconExportEnabled()
	if !enabled {
		return
	}
	bm.beacon.mu.Lock()
	deps := bm.beacon.deps
	bm.beacon.mu.Unlock()
	if deps.currentEpoch == nil || deps.recover == nil {
		return
	}

	qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	current, err := deps.currentEpoch(qctx)
	cancel()
	if err != nil {
		logging.Debug(beaconExportLogTag+"current epoch unavailable", inferenceTypes.BLS, "err", err)
		return
	}

	for _, epochID := range []uint64{current, current + 1} {
		if epochID == 0 || bm.beaconFileValid(dir, epochID) {
			continue
		}
		if !bm.beaconAttemptDue(epochID, deps.now()) {
			continue
		}
		result := bm.cache.Get(epochID)
		if !beaconExportable(result) {
			// Not cached or not final in cache: recover from chain. Recovery
			// stores the result, and storeVerificationResult exports it.
			result, err = deps.recover(epochID)
			if err != nil {
				if status.Code(errors.Unwrap(err)) == codes.NotFound || status.Code(err) == codes.NotFound {
					logging.Debug(beaconExportLogTag+"no BLS data for epoch yet", inferenceTypes.BLS, "epoch", epochID)
				} else {
					logging.Warn(beaconExportLogTag+"recovery failed", inferenceTypes.BLS, "epoch", epochID, "err", err)
				}
				continue
			}
		}
		if _, err := bm.exportBeaconShares(result); err != nil {
			logging.Warn(beaconExportLogTag+"export failed", inferenceTypes.BLS, "epoch", epochID, "err", err)
			continue
		}
		if bm.beaconFileValid(dir, epochID) {
			bm.beaconAttemptDone(epochID)
		}
	}
	if current >= 1 {
		bm.pruneBeaconShares(dir, current-1)
	}
}

// beaconFileValid reports whether dir holds a valid file for epochID.
func (bm *BlsManager) beaconFileValid(dir string, epochID uint64) bool {
	path := filepath.Join(dir, sharefile.FileName(epochID))
	if _, err := os.Stat(path); err != nil {
		return false
	}
	bm.beacon.mu.Lock()
	chainID := bm.beacon.chainID
	bm.beacon.mu.Unlock()
	f, err := sharefile.ReadFile(path, chainID)
	return err == nil && f.EpochID == epochID
}

// beaconAttemptDue rate-limits recovery per epoch and records the attempt.
func (bm *BlsManager) beaconAttemptDue(epochID uint64, now time.Time) bool {
	bm.beacon.mu.Lock()
	defer bm.beacon.mu.Unlock()
	if last, ok := bm.beacon.lastAttempt[epochID]; ok && now.Sub(last) < beaconRecoverBackoff {
		return false
	}
	bm.beacon.lastAttempt[epochID] = now
	for id := range bm.beacon.lastAttempt {
		if id+2 < epochID {
			delete(bm.beacon.lastAttempt, id)
		}
	}
	return true
}

// beaconAttemptDone clears the backoff so a later deletion is repaired on the next tick.
func (bm *BlsManager) beaconAttemptDone(epochID uint64) {
	bm.beacon.mu.Lock()
	defer bm.beacon.mu.Unlock()
	delete(bm.beacon.lastAttempt, epochID)
}
