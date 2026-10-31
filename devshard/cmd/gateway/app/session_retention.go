package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
	"devshard/logging"
	"devshard/storage"
)

// See ../docs/operations.md, "Session storage".
const (
	sessionEpochFilePrefix      = "epoch_"
	sessionEpochFileSuffix      = ".db"
	sessionRetentionPassTimeout = time.Minute
	hostHorizonPreviousEpochs   = storage.DefaultEpochRetain - 1
)

type escrowSnapshots interface {
	Snapshot() []registry.EscrowState
}

// sessionRetention removes escrow session directories once the hosts' horizon has passed them. See ../docs/operations.md, "Session storage".
type sessionRetention struct {
	storageDir     string
	previousEpochs uint64
	devshards      devshardLookup
	registered     escrowSnapshots
	prunedThrough  atomic.Uint64
	running        atomic.Bool
}

// newSessionRetention returns nil for 0, which keeps every session; anything below the host horizon is raised to it.
func newSessionRetention(storageDir string, configured int64, devshards devshardLookup, registered escrowSnapshots) *sessionRetention {
	if configured <= 0 {
		return nil
	}
	previousEpochs := uint64(configured)
	if previousEpochs < hostHorizonPreviousEpochs {
		logging.Warn("session retention raised to the host horizon",
			logkey.Configured, configured, logkey.Used, hostHorizonPreviousEpochs)
		previousEpochs = hostHorizonPreviousEpochs
	}
	return &sessionRetention{storageDir: storageDir, previousEpochs: previousEpochs, devshards: devshards, registered: registered}
}

// observe runs one pass per effective epoch off the observer's goroutine, which notifies subscribers synchronously.
func (r *sessionRetention) observe(ctx context.Context, snapshot chain.PhaseSnapshot) {
	if r == nil {
		return
	}
	effective := snapshot.EffectiveEpochIndex
	if effective == 0 {
		effective = snapshot.EpochIndex
	}
	if effective == 0 || effective <= r.prunedThrough.Load() || !r.running.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer r.running.Store(false)
		passContext, cancel := context.WithTimeout(ctx, sessionRetentionPassTimeout)
		defer cancel()
		removed, err := r.prune(passContext, effective)
		if err != nil {
			if ctx.Err() == nil {
				logging.Error("session retention pass failed", logkey.Epoch, effective, logkey.Removed, len(removed), logkey.Error, err)
			}
			return
		}
		r.prunedThrough.Store(effective)
		if len(removed) > 0 {
			logging.Info("session storage removed past the host horizon",
				logkey.Epoch, effective, logkey.Removed, len(removed), logkey.StorageDir, r.storageDir)
		}
	}()
}

// prune removes every escrow directory dated before the cutoff that neither serving nor settlement still owns.
func (r *sessionRetention) prune(ctx context.Context, effectiveEpoch uint64) ([]string, error) {
	cutoff := storage.RetentionCutoff(effectiveEpoch, r.previousEpochs+1)
	if cutoff == 0 {
		return nil, nil
	}
	records, err := r.devshards.ListDevshards(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing devshards: %w", err)
	}
	owned := make(map[string]bool, len(records))
	for _, record := range records {
		owned[record.EscrowID] = sessionStillOwned(record)
	}
	for _, state := range r.registered.Snapshot() {
		owned[state.ID] = true
	}
	entries, err := os.ReadDir(r.storageDir)
	if err != nil {
		return nil, fmt.Errorf("reading storage dir %s: %w", r.storageDir, err)
	}
	var removed []string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		escrowID, isEscrow := strings.CutPrefix(entry.Name(), api.DevshardStoragePrefix)
		if !entry.IsDir() || !isEscrow || escrowID == "" || owned[escrowID] {
			continue
		}
		escrowDir := filepath.Join(r.storageDir, entry.Name())
		epoch, dated, err := sessionEpoch(escrowDir)
		if err != nil {
			logging.Warn("session storage left in place", logkey.Escrow, escrowID, logkey.Error, err)
			continue
		}
		if !dated || epoch >= cutoff {
			continue
		}
		if err := os.RemoveAll(escrowDir); err != nil {
			logging.Warn("session storage left in place", logkey.Escrow, escrowID, logkey.Error, err)
			continue
		}
		removed = append(removed, escrowID)
	}
	return removed, nil
}

// sessionStillOwned keeps a serving session and one whose settle transaction is still being reconciled; past the cutoff the chain refuses any new settle.
func sessionStillOwned(record store.DevshardRecord) bool {
	return record.Active || record.SettleTxHash != ""
}

// sessionEpoch reads the epoch a session directory belongs to from its epoch_<N>.db file.
func sessionEpoch(escrowDir string) (uint64, bool, error) {
	entries, err := os.ReadDir(escrowDir)
	if err != nil {
		return 0, false, fmt.Errorf("reading session storage %s: %w", escrowDir, err)
	}
	var latest uint64
	dated := false
	for _, entry := range entries {
		digits, hasPrefix := strings.CutPrefix(entry.Name(), sessionEpochFilePrefix)
		digits, hasSuffix := strings.CutSuffix(digits, sessionEpochFileSuffix)
		if !hasPrefix || !hasSuffix {
			continue
		}
		epoch, err := strconv.ParseUint(digits, 10, 64)
		if err != nil {
			continue
		}
		latest, dated = max(latest, epoch), true
	}
	return latest, dated, nil
}
