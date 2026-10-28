// Package nonces turns what the gateway did to a nonce -- the race it ran, the burn it took, the
// timeout it voted -- and what the chain later recorded about it into one ledger, and serves that ledger.
package nonces

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/registry"
	"devshard/logging"
	"devshard/types"
)

const (
	nonceAccountingDatabase = "accounting.db"

	nonceAccountingShutdownGrace     = 5 * time.Second
	nonceAccountingReadHeaderTimeout = 10 * time.Second

	nonceAccountingSweepInterval = 10 * time.Second
)

type EpochSource interface {
	Snapshot() chain.PhaseSnapshot
}

type EscrowSource interface {
	Snapshot() []registry.EscrowState
	RoutableSession(escrowID string) (registry.EscrowSession, bool)
}

// DiffJournal is satisfied by *journal.Journal; DiffComposed runs under the session lock, so it reads and appends only.
type DiffJournal interface {
	DiffComposed(escrowID string, diff *types.Diff)
}

// CreationEpochFunc resolves the chain epoch an escrow was created in. See README.md, "The judgements it does make".
type CreationEpochFunc func(ctx context.Context, escrowID string) (uint64, bool)

type Recorder struct {
	service  *accounting.Service
	listener *http.Server
	epochs   EpochSource

	capability    atomic.Pointer[accounting.CapabilityFunc]
	creationEpoch atomic.Pointer[CreationEpochFunc]

	pinnedEpochs sync.Map

	observing sync.Map
	wake      chan struct{}

	readingLock    chan struct{}
	timedOutAtSeal map[string]map[uint64]struct{}
	stopSweeping   context.CancelFunc
	swept          chan struct{}

	unknownEscrowFacts atomic.Uint64
}

// EscrowPublished wakes the sweep, so a new escrow is opened before the facts of its first requests arrive; it only marks and returns, as the registry calls it under its lock.
func (n *Recorder) EscrowPublished(string, string) {
	if n == nil {
		return
	}
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *Recorder) SetCapability(lookup accounting.CapabilityFunc) {
	if n != nil && lookup != nil {
		n.capability.Store(&lookup)
	}
}

// SetCreationEpoch wires the resolver the sweep stamps escrows with. See README.md, "The judgements it does make".
func (n *Recorder) SetCreationEpoch(resolve CreationEpochFunc) {
	if n != nil && resolve != nil {
		n.creationEpoch.Store(&resolve)
	}
}

// epochOf answers the epoch an escrow was created in. See README.md, "The judgements it does make".
func (n *Recorder) epochOf(ctx context.Context, escrowID string) (uint64, bool) {
	if pinned, memoised := n.pinnedEpochs.Load(escrowID); memoised {
		return pinned.(uint64), true
	}
	resolve := n.creationEpoch.Load()
	if resolve == nil {
		return 0, false
	}
	epoch, resolved := (*resolve)(ctx, escrowID)
	if !resolved || epoch == 0 {
		return 0, false
	}
	n.pinnedEpochs.Store(escrowID, epoch)
	return epoch, true
}

func (n *Recorder) hostCapability(participant, model string) accounting.HostCapability {
	if n == nil {
		return accounting.HostCapability{}
	}
	if lookup := n.capability.Load(); lookup != nil {
		return (*lookup)(participant, model)
	}
	return accounting.HostCapability{}
}

func newRecorder(service *accounting.Service, epochs EpochSource) *Recorder {
	return &Recorder{service: service, epochs: epochs, wake: make(chan struct{}, 1), readingLock: make(chan struct{}, 1), timedOutAtSeal: make(map[string]map[uint64]struct{})}
}

func Open(settings config.NonceAccounting, storageDir string, epochs EpochSource, now func() time.Time) *Recorder {
	if !settings.Enabled {
		return nil
	}
	ledger := newRecorder(nil, epochs)
	store, openErr := accounting.OpenStore(DatabasePath(storageDir))
	if openErr != nil {
		logging.Error("nonce accounting could not open its store", "error", openErr)
		return nil
	}
	service, restoreErr := accounting.NewService(accounting.Settings{
		Store:            store,
		SnapshotInterval: time.Duration(settings.SnapshotSeconds) * time.Second,
		RetentionEpochs:  uint64(settings.RetentionEpochs),
		CurrentEpoch:     ledger.currentEpoch,
		Now:              now,
	})
	if restoreErr != nil {
		logging.Warn("nonce accounting started without its stored counters", "error", restoreErr)
	}
	ledger.service = service
	ledger.listener = &http.Server{
		Addr:              fmt.Sprintf(":%d", settings.Port),
		Handler:           accounting.NewHandler(service.Book, ledger.currentEpoch, ledger.hostCapability),
		ReadHeaderTimeout: nonceAccountingReadHeaderTimeout,
	}
	return ledger
}

// DatabasePath returns the path of the ledger's store under storageDir.
func DatabasePath(storageDir string) string {
	return filepath.Join(storageDir, nonceAccountingDatabase)
}

func (n *Recorder) currentEpoch(context.Context) (uint64, error) {
	if n == nil || n.epochs == nil {
		return 0, errors.New("chain snapshot is unavailable")
	}
	snapshot := n.epochs.Snapshot()
	if snapshot.EpochIndex == 0 {
		return 0, errors.New("current epoch is not known yet")
	}
	return snapshot.EpochIndex, nil
}

func (n *Recorder) Book() *accounting.Book {
	if n == nil || n.service == nil {
		return nil
	}
	return n.service.Book
}

func (n *Recorder) ResetEpoch(epoch uint64) (int, error) {
	if n == nil {
		return 0, nil
	}
	return n.service.ResetEpoch(epoch)
}

func (n *Recorder) report(err error) {
	if err != nil && !errors.Is(err, accounting.ErrUnknownEscrow) {
		logging.Warn("nonce accounting refused a fact", "error", err)
	}
}

func (n *Recorder) reportFact(err error) {
	if errors.Is(err, accounting.ErrUnknownEscrow) {
		n.unknownEscrowFacts.Add(1)
		return
	}
	n.report(err)
}

// UnknownEscrowFacts counts the facts dropped for an escrow the ledger does not hold: one its sweep has not opened yet, or one retention pruned.
func (n *Recorder) UnknownEscrowFacts() uint64 {
	if n == nil {
		return 0
	}
	return n.unknownEscrowFacts.Load()
}

func (n *Recorder) Close() error {
	if n == nil {
		return nil
	}
	if n.listener == nil {
		return n.service.Close()
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), nonceAccountingShutdownGrace)
	defer cancelShutdown()
	return errors.Join(n.listener.Shutdown(shutdownCtx), n.service.Close())
}
