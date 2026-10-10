package main

import (
	"context"
	"log"
	"math"
	"sync"
	"time"

	"devshard/accounting"
)

const (
	perfPruneBatchSize  = 2000
	perfPruneStartDelay = time.Minute
	perfPruneInterval   = 10 * time.Minute
	perfPrunePause      = 20 * time.Millisecond
)

// perfPrunerTiming paces the pruner: batches are small and spaced so request-path inserts sharing the store's single connection never wait long.
type perfPrunerTiming struct {
	startDelay time.Duration
	interval   time.Duration
	pause      time.Duration
}

// perfPruneResult counts the rows one pass deleted.
type perfPruneResult struct {
	samples    int64
	requestLog int64
	accounting int64
}

// perfPruner deletes what perf.db keeps but nothing reads; accounting is nil on a store several gateways share. See devshard/docs/host-health.md, "perf.db: what startup reads and what is pruned".
type perfPruner struct {
	store                 perfPruneStore
	accounting            accountingPruneStore
	retainsEscrow         func(escrowID string) bool
	currentEpoch          func() uint64
	timing                perfPrunerTiming
	stop                  chan struct{}
	done                  chan struct{}
	stopOnce              sync.Once
	accountingWalkedEpoch uint64
}

func newPerfPruner(store perfPruneStore, accounting accountingPruneStore, retainsEscrow func(string) bool, currentEpoch func() uint64, timing perfPrunerTiming) *perfPruner {
	return &perfPruner{
		store:         store,
		accounting:    accounting,
		retainsEscrow: retainsEscrow,
		currentEpoch:  currentEpoch,
		timing:        timing,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
}

func (p *perfPruner) start() {
	go p.run()
}

func (p *perfPruner) stopAndWait() {
	p.stopOnce.Do(func() { close(p.stop) })
	<-p.done
}

func (p *perfPruner) run() {
	defer close(p.done)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-p.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	timer := time.NewTimer(p.timing.startDelay)
	defer timer.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-timer.C:
		}
		started := time.Now()
		result := p.runOnce(ctx)
		if result.samples+result.requestLog+result.accounting > 0 {
			log.Printf("perf_prune samples_deleted=%d request_log_deleted=%d accounting_deleted=%d elapsed_ms=%d",
				result.samples, result.requestLog, result.accounting, time.Since(started).Milliseconds())
		}
		timer.Reset(p.timing.interval)
	}
}

func (p *perfPruner) runOnce(ctx context.Context) perfPruneResult {
	var result perfPruneResult
	if boundary, found := p.hostSampleBoundary(ctx); found {
		result.samples = p.deleteOldestRows(ctx, "perf_host_samples", p.store.deleteHostSamplesUpTo, boundary)
	}
	if boundary, err := p.store.requestLogRetentionBoundary(ctx); err != nil {
		log.Printf("perf_prune request log boundary: %v", err)
	} else {
		result.requestLog = p.deleteOldestRows(ctx, "perf_request_log", p.store.deleteRequestLogUpTo, boundary)
	}
	if p.accounting == nil || p.retainsEscrow == nil || p.currentEpoch == nil {
		return result
	}
	epoch := p.currentEpoch()
	if epoch == 0 || epoch == p.accountingWalkedEpoch {
		return result
	}
	deleted, complete := p.walkAccounting(ctx)
	result.accounting = deleted
	if complete {
		p.accountingWalkedEpoch = epoch
	}
	return result
}

func (p *perfPruner) hostSampleBoundary(ctx context.Context) (int64, bool) {
	boundary, found, err := findHostSampleBoundary(ctx, p.store, time.Now(), p.pause)
	if err != nil {
		log.Printf("perf_prune samples boundary: %v", err)
	}
	return boundary, found
}

// findHostSampleBoundary pages LoadSamples' newest-first walk until the live sample that ends it; pause between pages returns false to give up.
func findHostSampleBoundary(ctx context.Context, store perfPruneStore, now time.Time, pause func() bool) (int64, bool, error) {
	_, stopBefore := sampleWindow(now)
	if stopBefore.IsZero() {
		return 0, false, nil
	}
	beforeID := int64(math.MaxInt64)
	for {
		boundary, lastScannedID, scanned, err := store.hostSampleBoundaryBatch(ctx, stopBefore, beforeID, perfPruneBatchSize)
		if err != nil {
			return 0, false, err
		}
		if boundary > 0 {
			return boundary, true, nil
		}
		if scanned < perfPruneBatchSize || !pause() {
			return 0, false, nil
		}
		beforeID = lastScannedID
	}
}

func (p *perfPruner) deleteOldestRows(ctx context.Context, table string, deleteUpTo func(ctx context.Context, boundaryID int64, limit int) (int64, error), boundaryID int64) int64 {
	var total int64
	for boundaryID > 0 {
		deleted, err := deleteUpTo(ctx, boundaryID, perfPruneBatchSize)
		if err != nil {
			log.Printf("perf_prune %s: %v", table, err)
			return total
		}
		total += deleted
		if deleted < perfPruneBatchSize || !p.pause() {
			return total
		}
	}
	return total
}

func (p *perfPruner) walkAccounting(ctx context.Context) (int64, bool) {
	var total int64
	tables := []struct {
		name          string
		deleteBatchOf func(ctx context.Context, afterRowID int64, limit int, retainsEscrow func(string) bool) (int64, int, int64, error)
	}{
		{"request_accounting", p.accounting.deleteUnretainedAccountingRequests},
		{"request_accounting_attempts", p.accounting.deleteUnretainedAccountingAttempts},
		{"request_accounting_aliases", p.accounting.deleteUnretainedAccountingAliases},
	}
	for _, table := range tables {
		var afterRowID int64
		for {
			lastRowID, scanned, deleted, err := table.deleteBatchOf(ctx, afterRowID, perfPruneBatchSize, p.retainsEscrow)
			if err != nil {
				log.Printf("perf_prune %s: %v", table.name, err)
				return total, false
			}
			total += deleted
			afterRowID = lastRowID
			if scanned < perfPruneBatchSize {
				break
			}
			if !p.pause() {
				return total, false
			}
		}
	}
	return total, true
}

func (p *perfPruner) pause() bool {
	if p.timing.pause <= 0 {
		select {
		case <-p.stop:
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(p.timing.pause)
	defer timer.Stop()
	select {
	case <-p.stop:
		return false
	case <-timer.C:
		return true
	}
}

// startPerfPruner prunes accounting only when the ledger itself enforces DEVSHARD_STATS_RETENTION_EPOCHS, and never for a resident escrow.
func (g *Gateway) startPerfPruner(tracker *accounting.Tracker, retentionEpochs uint64) {
	if g.perfStore == nil {
		return
	}
	var retainsEscrow func(string) bool
	if tracker != nil && retentionEpochs > 0 {
		retainsEscrow = func(escrowID string) bool {
			if tracker.RetainsEscrow(escrowID) {
				return true
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			_, resident := g.runtimes[escrowID]
			return resident
		}
	}
	currentEpoch := func() uint64 {
		g.mu.Lock()
		phaseGate := g.phaseGate
		g.mu.Unlock()
		if phaseGate == nil {
			return 0
		}
		return phaseGate.Snapshot().EpochIndex
	}
	accountingStore, ownsAccounting := g.perfStore.(accountingPruneStore)
	if !ownsAccounting && retainsEscrow != nil {
		log.Printf("perf_prune request accounting is kept: the perf store is shared between gateways")
	}
	g.perfPruner = newPerfPruner(g.perfStore, accountingStore, retainsEscrow, currentEpoch, perfPrunerTiming{
		startDelay: perfPruneStartDelay, interval: perfPruneInterval, pause: perfPrunePause,
	})
	g.perfPruner.start()
}
