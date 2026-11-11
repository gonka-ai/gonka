package bls

// Block BLS beacon, Step 2: export this participant's recovered slot shares to
// the chain node (proposals/block-bls-beacon/block-bls-beacon-vote-extensions-plan.md).
//
// The node signs per-block beacon partials inside ExtendVote, which must not
// call out to the dAPI, so the dAPI writes the shares to a file in the node
// home both containers mount (<home>/bls-beacon/epoch-<N>.json, mode 0600).
// The node checks them against committed state before use.
//
// Two triggers:
//   - storeVerificationResult: every completed (COMPLETED/SIGNED) result with a
//     group key is snapshotted in the handler (cheap) and written by a worker
//     goroutine, so the DKG event path never waits on disk or the chain id
//     query (review L6). Without a running worker the write is synchronous.
//   - a background reconcile loop: for the current and next epoch it makes the
//     file's content match this node's final result (from cache, or recovered
//     from chain once per epoch): a missing, corrupt, stale or foreign file is
//     rewritten, and a file for an epoch where this node holds no slots is
//     removed (review L2). This covers dAPI restarts and deleted files.

import (
	"context"
	"decentralized-api/apiconfig"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	// beaconExportQueue bounds pending async exports; when full, the job is
	// dropped and the reconcile loop writes the file on its next tick.
	beaconExportQueue = 8
)

// beaconExportJob is a self-contained snapshot of what one share file needs,
// taken in the event handler so the worker never reads a shared result.
type beaconExportJob struct {
	epochID        uint64
	slotStart      uint32
	slotEnd        uint32
	groupPublicKey []byte
	shares         [][sharefile.ScalarLen]byte // big-endian fr
}

// beaconKeyState is x/bls's block beacon key (rule R9): the epoch signing
// beacon rounds and the completed DKG waiting to take over.
type beaconKeyState struct {
	keyEpoch     uint64
	keySet       bool
	candidate    uint64
	hasCandidate bool
}

// beaconExportDeps are the chain-facing calls the exporter needs; tests replace them.
type beaconExportDeps struct {
	chainID      func(ctx context.Context) (string, error)
	currentEpoch func(ctx context.Context) (uint64, error)
	// beaconKey may be nil or fail (chain before the Step 3 upgrade): reconcile
	// then covers the current and next epoch only.
	beaconKey   func(ctx context.Context) (beaconKeyState, error)
	participant func() string
	recover     func(epochID uint64) (*VerificationResult, error)
	now         func() time.Time
}

type beaconExporter struct {
	mu          sync.Mutex
	enabled     bool
	dir         string
	deps        beaconExportDeps
	chainID     string
	lastAttempt map[uint64]time.Time
	jobs        chan *beaconExportJob // nil until StartBeaconShareExport
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
		beaconKey: func(ctx context.Context) (beaconKeyState, error) {
			res, err := bm.cosmosClient.NewBLSQueryClient().BeaconKey(ctx, &types.QueryBeaconKeyRequest{})
			if err != nil {
				return beaconKeyState{}, err
			}
			return beaconKeyState{keyEpoch: res.KeyEpoch, keySet: res.KeySet, candidate: res.Candidate.Epoch, hasCandidate: res.HasCandidate}, nil
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

// beaconJobFor snapshots result into an export job. It returns nil, nil when
// export is disabled or result is not a final participant result. Cheap: no
// I/O, no chain queries.
func (bm *BlsManager) beaconJobFor(result *VerificationResult) (*beaconExportJob, error) {
	if enabled, _ := bm.beaconExportEnabled(); !enabled || !beaconExportable(result) {
		return nil, nil
	}
	if err := bm.ensureConsensusSharesComplete(result); err != nil {
		return nil, fmt.Errorf("shares incomplete: %w", err)
	}
	if result.SlotRange[1] < result.SlotRange[0] {
		return nil, fmt.Errorf("invalid slot range %v", result.SlotRange)
	}
	if n := int(result.SlotRange[1]-result.SlotRange[0]) + 1; len(result.AggregatedShares) != n {
		return nil, fmt.Errorf("%d aggregated shares for %d slots", len(result.AggregatedShares), n)
	}
	job := &beaconExportJob{
		epochID:        result.EpochID,
		slotStart:      result.SlotRange[0],
		slotEnd:        result.SlotRange[1],
		groupPublicKey: append([]byte(nil), result.GroupPublicKey...),
		shares:         make([][sharefile.ScalarLen]byte, len(result.AggregatedShares)),
	}
	for i := range result.AggregatedShares {
		job.shares[i] = result.AggregatedShares[i].Bytes()
	}
	return job, nil
}

// beaconFileFor builds the file job would write (chain id and participant
// filled in). Its SHA256 is the content checksum to compare against.
func (bm *BlsManager) beaconFileFor(job *beaconExportJob) (*sharefile.File, error) {
	ctx, cancel := context.WithTimeout(bm.ctx, 10*time.Second)
	defer cancel()
	chainID, err := bm.beaconChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("chain id: %w", err)
	}
	bm.beacon.mu.Lock()
	deps := bm.beacon.deps
	bm.beacon.mu.Unlock()
	participant := ""
	if deps.participant != nil {
		participant = deps.participant()
	}
	return sharefile.New(chainID, job.epochID, participant, job.slotStart, job.slotEnd, job.groupPublicKey, job.shares, deps.now())
}

// writeBeaconJob writes job's file (idempotent by checksum). It returns
// whether a file was (re)written. Shares are never logged. Pruning is left to
// the reconcile loop, which knows the active beacon key epoch (rule R9): after
// failed DKGs the key can be older than any fixed window behind job.epochID.
func (bm *BlsManager) writeBeaconJob(job *beaconExportJob) (bool, error) {
	enabled, dir := bm.beaconExportEnabled()
	if !enabled {
		return false, nil
	}
	file, err := bm.beaconFileFor(job)
	if err != nil {
		return false, err
	}
	path, written, err := sharefile.WriteAtomic(dir, file)
	if err != nil {
		return false, err
	}
	if written {
		logging.Info(beaconExportLogTag+"beacon shares exported", inferenceTypes.BLS,
			"epoch", job.epochID,
			"slots", fmt.Sprintf("[%d,%d]", job.slotStart, job.slotEnd),
			"path", path)
	}
	return written, nil
}

// exportBeaconShares synchronously writes result's shares if it is a final
// participant result. It returns whether a file was (re)written.
func (bm *BlsManager) exportBeaconShares(result *VerificationResult) (bool, error) {
	job, err := bm.beaconJobFor(result)
	if job == nil || err != nil {
		return false, err
	}
	return bm.writeBeaconJob(job)
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
	job, err := bm.beaconJobFor(result)
	if err != nil {
		logging.Warn(beaconExportLogTag+"export failed", inferenceTypes.BLS, "epoch", result.EpochID, "err", err)
		return
	}
	if job == nil || bm.enqueueBeaconJob(job) {
		return
	}
	// No worker running (tests, early startup): write synchronously.
	if _, err := bm.writeBeaconJob(job); err != nil {
		logging.Warn(beaconExportLogTag+"export failed", inferenceTypes.BLS, "epoch", job.epochID, "err", err)
	}
}

// enqueueBeaconJob hands job to the export worker without blocking. It returns
// false when no worker is running; a full queue drops the job (the reconcile
// loop writes the file on its next tick) and returns true.
func (bm *BlsManager) enqueueBeaconJob(job *beaconExportJob) bool {
	bm.beacon.mu.Lock()
	jobs := bm.beacon.jobs
	bm.beacon.mu.Unlock()
	if jobs == nil {
		return false
	}
	select {
	case jobs <- job:
	default:
		logging.Warn(beaconExportLogTag+"export queue full, reconcile will write the file", inferenceTypes.BLS, "epoch", job.epochID)
	}
	return true
}

// runBeaconExportWorker writes queued jobs until ctx is done.
func (bm *BlsManager) runBeaconExportWorker(ctx context.Context, jobs <-chan *beaconExportJob) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-jobs:
			func() {
				defer func() {
					if r := recover(); r != nil {
						logging.Error(beaconExportLogTag+"panic in export worker", inferenceTypes.BLS, "panic", r)
					}
				}()
				if _, err := bm.writeBeaconJob(job); err != nil {
					logging.Warn(beaconExportLogTag+"export failed", inferenceTypes.BLS, "epoch", job.epochID, "err", err)
				}
			}()
		}
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
	jobs := make(chan *beaconExportJob, beaconExportQueue)
	bm.beacon.mu.Lock()
	bm.beacon.jobs = jobs
	bm.beacon.mu.Unlock()
	go bm.runBeaconExportWorker(ctx, jobs)
	go func() {
		// Prefetch the chain id so the first export does not wait for it (L6).
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if _, err := bm.beaconChainID(qctx); err != nil {
			logging.Debug(beaconExportLogTag+"chain id not available yet", inferenceTypes.BLS, "err", err)
		}
		cancel()
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

	epochs, keepFrom := beaconReconcileEpochs(ctx, deps, current)
	for _, epochID := range epochs {
		result := bm.cache.Get(epochID)
		if !beaconFinal(result) {
			// No final result cached (restart, not final yet): recover from
			// chain, rate-limited. Done once per epoch per process when it
			// succeeds, since recovery caches the result.
			if !bm.beaconAttemptDue(epochID, deps.now()) {
				continue
			}
			result, err = deps.recover(epochID)
			if err != nil {
				if status.Code(errors.Unwrap(err)) == codes.NotFound || status.Code(err) == codes.NotFound {
					logging.Debug(beaconExportLogTag+"no BLS data for epoch yet", inferenceTypes.BLS, "epoch", epochID)
				} else {
					logging.Warn(beaconExportLogTag+"recovery failed", inferenceTypes.BLS, "epoch", epochID, "err", err)
				}
				continue
			}
			if !beaconFinal(result) {
				continue // DKG not final yet: retry after the backoff
			}
		}
		bm.beaconAttemptDone(epochID)
		bm.reconcileBeaconFile(dir, epochID, result)
	}
	if keepFrom > 0 {
		bm.pruneBeaconShares(dir, keepFrom)
	}
}

// beaconReconcileEpochs returns the epochs whose files must exist, ascending,
// and the lowest epoch to keep: the current and next epoch, the beacon key
// epoch and the key candidate (rule R9), keeping everything from the oldest
// of current-1, the key and the candidate.
func beaconReconcileEpochs(ctx context.Context, deps beaconExportDeps, current uint64) (epochs []uint64, keepFrom uint64) {
	set := map[uint64]bool{}
	add := func(e uint64) {
		if e > 0 {
			set[e] = true
		}
	}
	add(current)
	add(current + 1)
	if current >= 1 {
		keepFrom = current - 1
	}
	if deps.beaconKey != nil {
		qctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		key, err := deps.beaconKey(qctx)
		cancel()
		if err != nil {
			logging.Debug(beaconExportLogTag+"beacon key unavailable; covering current and next epoch only", inferenceTypes.BLS, "err", err)
		} else {
			for _, e := range []struct {
				id uint64
				ok bool
			}{{key.keyEpoch, key.keySet}, {key.candidate, key.hasCandidate}} {
				if !e.ok || e.id == 0 {
					continue
				}
				add(e.id)
				if e.id < keepFrom {
					keepFrom = e.id
				}
			}
		}
	}
	for e := range set {
		epochs = append(epochs, e)
	}
	sort.Slice(epochs, func(i, j int) bool { return epochs[i] < epochs[j] })
	return epochs, keepFrom
}

// beaconFinal reports whether result is a final (COMPLETED/SIGNED) result.
func beaconFinal(result *VerificationResult) bool {
	return result != nil &&
		(result.DkgPhase == types.DKGPhase_DKG_PHASE_COMPLETED || result.DkgPhase == types.DKGPhase_DKG_PHASE_SIGNED)
}

// reconcileBeaconFile makes dir's file for epochID match the final result:
// written if missing or different (corrupt, stale, foreign), removed if this
// node holds no slots in the epoch (review L2).
func (bm *BlsManager) reconcileBeaconFile(dir string, epochID uint64, result *VerificationResult) {
	path := filepath.Join(dir, sharefile.FileName(epochID))
	if !result.IsParticipant {
		if _, err := os.Lstat(path); err == nil {
			if err := os.Remove(path); err != nil {
				logging.Warn(beaconExportLogTag+"cannot remove share file of a non-participant epoch", inferenceTypes.BLS, "epoch", epochID, "err", err)
			} else {
				logging.Warn(beaconExportLogTag+"removed share file: this node holds no slots in the epoch", inferenceTypes.BLS, "epoch", epochID)
			}
		}
		return
	}
	job, err := bm.beaconJobFor(result)
	if err != nil || job == nil {
		if err != nil {
			logging.Warn(beaconExportLogTag+"export failed", inferenceTypes.BLS, "epoch", epochID, "err", err)
		}
		return
	}
	want, err := bm.beaconFileFor(job)
	if err != nil {
		logging.Warn(beaconExportLogTag+"export failed", inferenceTypes.BLS, "epoch", epochID, "err", err)
		return
	}
	if have, err := sharefile.ReadFile(path, want.ChainID); err == nil && have.SHA256 == want.SHA256 {
		return
	} else if err == nil {
		logging.Warn(beaconExportLogTag+"share file content differs from this node's shares, rewriting", inferenceTypes.BLS,
			"epoch", epochID, "file_participant", have.ParticipantAddress,
			"file_slots", fmt.Sprintf("[%d,%d]", have.SlotStartIndex, have.SlotEndIndex))
	}
	_, written, err := sharefile.WriteAtomic(dir, want)
	if err != nil {
		logging.Warn(beaconExportLogTag+"export failed", inferenceTypes.BLS, "epoch", epochID, "err", err)
		return
	}
	if written {
		logging.Info(beaconExportLogTag+"beacon shares exported", inferenceTypes.BLS,
			"epoch", epochID, "slots", fmt.Sprintf("[%d,%d]", job.slotStart, job.slotEnd), "path", path)
	}
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
