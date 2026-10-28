package nonces

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/registry"
	"devshard/logging"
	"devshard/types"
)

// watchDiffs hands every composed diff to the journal, so the book's lock is never taken under the session's. See README.md, "Boundaries".
func (n *Recorder) watchDiffs(escrowID string, session registry.EscrowSession, diffs DiffJournal) {
	if diffs == nil {
		return
	}
	underlying := session.UserSession()
	if underlying == nil {
		return
	}
	if watched, already := n.observing.Swap(escrowID, underlying); already && watched == underlying {
		return
	}
	n.watchSessionDiffs(escrowID, underlying, diffs)
}

type diffSource interface {
	SetDiffObserver(observer func(types.Diff))
	Diffs() []types.Diff
}

type firstObservedDiff struct {
	seen  atomic.Bool
	nonce atomic.Uint64
}

func (first *firstObservedDiff) observe(nonce uint64) {
	if !first.seen.Load() {
		first.nonce.Store(nonce)
		first.seen.Store(true)
	}
}

func (n *Recorder) watchSessionDiffs(escrowID string, source diffSource, diffs DiffJournal) {
	first := &firstObservedDiff{}
	source.SetDiffObserver(func(diff types.Diff) {
		first.observe(diff.Nonce)
		diffs.DiffComposed(escrowID, &diff)
	})
	if n.service == nil {
		return
	}
	watermark, restored := n.service.Book.TakeRestoredWatermark(escrowID)
	if !restored {
		return
	}
	// A shutdown that abandoned work closes the journal while that work still composes diffs, so the last run's ledger never took them.
	// Every service diff it dropped lies above the watermark it saved; the replay hands over those still retained, up to the first the observer saw.
	retained := source.Diffs()
	seen, firstObserved := first.seen.Load(), first.nonce.Load()
	for index := range retained {
		diff := retained[index]
		if diff.Nonce > watermark && (!seen || diff.Nonce < firstObserved) && !startsInference(diff) {
			diffs.DiffComposed(escrowID, &diff)
		}
	}
}

func startsInference(diff types.Diff) bool {
	return slices.ContainsFunc(diff.Txs, func(tx *types.DevshardTx) bool { return tx.GetStartInference() != nil })
}

// Start serves the ledger's listener and sweeps until ctx ends; every escrow the sweep watches hands its composed diffs to diffs.
func (n *Recorder) Start(ctx context.Context, escrows EscrowSource, diffs DiffJournal) {
	if n == nil {
		return
	}
	n.serve()
	n.StartSweeping(ctx, escrows, diffs)
}

// StartSweeping sweeps until ctx ends or Stop is called, without serving the listener, for a process that must not open a port.
func (n *Recorder) StartSweeping(ctx context.Context, escrows EscrowSource, diffs DiffJournal) {
	if n == nil {
		return
	}
	sweepCtx, stopSweeping := context.WithCancel(ctx)
	swept := make(chan struct{})
	n.stopSweeping, n.swept = stopSweeping, swept
	go func() {
		defer close(swept)
		n.sweepUntil(sweepCtx, escrows, diffs)
	}()
}

// Stop ends the sweep and waits out the one in progress, so no sweep reads or retires an escrow once the sessions close. See README.md, "Boundaries".
func (n *Recorder) Stop() {
	if n == nil || n.stopSweeping == nil {
		return
	}
	n.stopSweeping()
	<-n.swept
}

func (n *Recorder) serve() {
	if n.listener == nil {
		return
	}
	logging.Info("nonce accounting listening", logkey.Addr, n.listener.Addr)
	go func() {
		if err := n.listener.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.Error("nonce accounting listener stopped", "error", err)
		}
	}()
}

func (n *Recorder) sweepUntil(ctx context.Context, escrows EscrowSource, diffs DiffJournal) {
	ticker := time.NewTicker(nonceAccountingSweepInterval)
	defer ticker.Stop()
	for {
		n.sweep(ctx, escrows, diffs)
		select {
		case <-ticker.C:
		case <-n.wake:
		case <-ctx.Done():
			return
		}
	}
}

func (n *Recorder) sweep(ctx context.Context, escrows EscrowSource, diffs DiffJournal) {
	states := escrows.Snapshot()
	published := make(map[string]struct{}, len(states))
	for _, state := range states {
		if _, routable := escrows.RoutableSession(state.ID); !routable {
			continue
		}
		epoch, known := n.epochOf(ctx, state.ID)
		session, read := n.takeReadingIfRoutable(escrows, state, epoch, known)
		if !read {
			continue
		}
		published[state.ID] = struct{}{}
		n.watchDiffs(state.ID, session, diffs)
	}
	for _, escrowID := range n.service.Book.EscrowIDs() {
		if _, still := published[escrowID]; !still {
			n.service.Book.RetireEscrow(escrowID)
			n.forgetEscrow(escrowID)
		}
	}
}

func (n *Recorder) takeReadingIfRoutable(escrows EscrowSource, state registry.EscrowState, epoch uint64, epochKnown bool) (registry.EscrowSession, bool) {
	n.readingLock <- struct{}{}
	defer func() { <-n.readingLock }()
	session, routable := escrows.RoutableSession(state.ID)
	if !routable {
		return nil, false
	}
	escrowState := session.SnapshotState()
	if epochKnown {
		n.report(n.service.Book.OpenEscrow(accounting.EscrowMetadata{
			EscrowID:      state.ID,
			Model:         state.Model,
			CreationEpoch: epoch,
			Slots:         escrowState.Group,
		}))
	}
	n.takeReading(state.ID, escrowState, session)
	return session, true
}

// What one sweep reads off an escrow: the watermark, what the chain charges per nonce, the chain's per-slot stats, and every nonce's money.
func (n *Recorder) observeEscrowState(escrowID string, escrowState types.EscrowState) {
	n.report(n.service.Book.ObserveLatestNonce(escrowID, escrowState.LatestNonce))
	n.report(n.service.Book.ObserveCharging(escrowID, escrowState.Config.FeePerNonce, escrowState.FinalizeNonce))
	for slotID, stats := range escrowState.HostStats {
		if stats != nil {
			n.report(n.service.Book.ObserveHostStats(escrowID, slotID, *stats))
		}
	}
	n.report(n.service.Book.ObserveInferences(escrowID, escrowState.Inferences))
}

// EscrowRetiring takes the reading the sweep will never take again, and asks the ledger's own goroutine to write it out.
// See docs/accounting.md, "Money and tokens".
func (n *Recorder) EscrowRetiring(escrowID string, session registry.EscrowSession) {
	if n == nil || n.service == nil {
		return
	}
	if session != nil {
		n.takeLockedReading(escrowID, session)
	}
	n.service.Book.RetireEscrow(escrowID)
	n.forgetEscrow(escrowID)
	n.service.RequestFlush()
}

func (n *Recorder) forgetEscrow(escrowID string) {
	n.observing.Delete(escrowID)
	n.readingLock <- struct{}{}
	defer func() { <-n.readingLock }()
	delete(n.timedOutAtSeal, escrowID)
}

// EscrowClosing takes the reading a shutdown would otherwise lose, before the session closes, and leaves the escrow open. See docs/accounting.md, "Storage".
func (n *Recorder) EscrowClosing(escrowID string, session registry.EscrowSession) {
	if n == nil || n.service == nil || session == nil {
		return
	}
	n.takeLockedReading(escrowID, session)
}

func (n *Recorder) takeLockedReading(escrowID string, session registry.EscrowSession) {
	n.readingLock <- struct{}{}
	defer func() { <-n.readingLock }()
	n.takeReading(escrowID, session.SnapshotState(), session)
}

func (n *Recorder) takeReading(escrowID string, snapshot types.EscrowState, session registry.EscrowSession) {
	sealed := newSealedRecords(session)
	n.observeSealedRecords(escrowID, snapshot, sealed)
	n.observeEscrowState(escrowID, snapshot)
	n.closeSealed(escrowID, snapshot, sealed)
}

func (n *Recorder) observeSealedRecords(escrowID string, snapshot types.EscrowState, sealed *sealedRecords) {
	records := make(map[uint64]types.InferenceRecord)
	for _, nonce := range n.service.Book.NoncesToFold(escrowID, snapshot.Inferences) {
		if record, kept := sealed.lookup(nonce); kept {
			records[nonce] = record
		}
	}
	n.report(n.service.Book.ObserveSealedRecords(escrowID, records))
}

func (n *Recorder) closeSealed(escrowID string, snapshot types.EscrowState, sealed *sealedRecords) {
	timedOutNonces := n.timedOutAtSealOf(escrowID)
	var finished []uint64
	for _, nonce := range n.service.Book.UnfinishedNonces(escrowID) {
		if _, live := snapshot.Inferences[nonce]; live || nonce > snapshot.LatestNonce {
			continue
		}
		if _, final := timedOutNonces[nonce]; final {
			continue
		}
		switch {
		case sealed.finished(nonce):
			finished = append(finished, nonce)
		case sealed.timedOut(nonce):
			timedOutNonces[nonce] = struct{}{}
		}
	}
	n.report(n.service.Book.MarkFinished(escrowID, finished))
}

func (n *Recorder) timedOutAtSealOf(escrowID string) map[uint64]struct{} {
	nonces, known := n.timedOutAtSeal[escrowID]
	if !known {
		nonces = make(map[uint64]struct{})
		n.timedOutAtSeal[escrowID] = nonces
	}
	return nonces
}

type sealedLookup struct {
	record types.InferenceRecord
	kept   bool
}

type sealedRecords struct {
	session registry.EscrowSession
	lookups map[uint64]sealedLookup
}

func newSealedRecords(session registry.EscrowSession) *sealedRecords {
	return &sealedRecords{session: session, lookups: make(map[uint64]sealedLookup)}
}

func (s *sealedRecords) lookup(nonce uint64) (types.InferenceRecord, bool) {
	if earlier, cached := s.lookups[nonce]; cached {
		return earlier.record, earlier.kept
	}
	record, kept := s.session.SealedInference(nonce)
	s.lookups[nonce] = sealedLookup{record: record, kept: kept}
	return record, kept
}

func (s *sealedRecords) finished(nonce uint64) bool {
	if record, kept := s.lookup(nonce); kept {
		return accounting.FinishApplied(record.Status)
	}
	underlying := s.session.UserSession()
	return underlying != nil && underlying.IsNonceFinished(nonce)
}

func (s *sealedRecords) timedOut(nonce uint64) bool {
	record, kept := s.lookup(nonce)
	return kept && record.Status == types.StatusTimedOut
}
