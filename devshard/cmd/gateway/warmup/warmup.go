// Package warmup spends one nonce on a newly published escrow so every host in its group learns the
// escrow exists, and settles that nonce itself: nothing else ever will.
package warmup

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/registry"
	"devshard/host"
	"devshard/state"
	"devshard/user"
)

const (
	probeTimeout   = 2 * time.Minute
	catchUpTimeout = 2 * time.Minute
	budget         = 20 * time.Minute
	probeMaxTokens = uint64(state.MinReservationTokens)
)

var probePrompt = fmt.Appendf(nil, `{"messages":[{"role":"user","content":"."}],"max_tokens":%d}`, probeMaxTokens)

// Posters resolves the vote poster for one escrow and the params its nonce committed.
type Posters func(escrowID string, params any) (engine.TimeoutPoster, bool)

type Escrows interface {
	Acquire(escrowID string) (registry.EscrowSession, func(), bool)
}

type ledger interface {
	OpenEscrow(metadata accounting.EscrowMetadata) error
}

// Probes is satisfied by *journal.Journal: the probe's nonce reaches the ledger the way a race's does.
type Probes interface {
	ProbeRecorded(escrowID string, attempt accounting.Attempt)
}

// warmupNarrator is satisfied by *journal.Journal. See README.md, "Boundaries".
type warmupNarrator interface {
	WarmupFoundNoNonce(escrowID string, probeErr error)
	EscrowWarmed(escrowID, model string, nonce uint64, served bool, catchUpErr error)
	WarmupLedgerOpenFailed(escrowID string, err error)
	WarmupVoted(escrowID string, nonce uint64, action, reason string)
}

// Epochs stamps the escrow with the epoch the ledger first saw it in, exactly as its own sweep would.
type Epochs interface {
	Snapshot() chain.PhaseSnapshot
}

// EscrowPublished is announced under the registry's lock, so it must return without doing work.
type Prober struct {
	escrows  Escrows
	ledger   ledger
	epochs   Epochs
	posters  Posters
	timeouts Timeouts
	probes   Probes
	narrator warmupNarrator
	probe    func(ctx context.Context, session registry.EscrowSession, params user.InferenceParams, nonceCommitted func()) (uint64, bool, error)
	catchUp  func(ctx context.Context, session registry.EscrowSession) error
	stop     <-chan struct{}
	now      func() time.Time
}

func New(configuration *config.Holder, ledger *accounting.Book, epochs Epochs, now func() time.Time) *Prober {
	if configuration == nil || !configuration.Load().Scheduler.WarmNewEscrows {
		return nil
	}
	warmup := &Prober{probe: dispatchProbe, catchUp: catchUpAllHosts, epochs: epochs, now: now}
	if ledger != nil {
		warmup.ledger = ledger
	}
	return warmup
}

func (w *Prober) Start(ctx context.Context) {
	if w != nil {
		w.stop = ctx.Done()
	}
}

func (w *Prober) EscrowPublished(escrowID, model string) {
	if w == nil || w.escrows == nil {
		return
	}
	go w.warm(escrowID, model)
}

func (w *Prober) warm(escrowID, model string) {
	session, release, live := w.escrows.Acquire(escrowID)
	if !live {
		return
	}
	defer release()
	if session.Nonce() != 0 {
		return
	}

	// The budget bounds the wait for the group and the probe; the vote is due after the execution deadline, past it.
	lifetime, endLifetime := context.WithCancel(context.Background())
	defer endLifetime()
	go func() {
		select {
		case <-w.stop:
			endLifetime()
		case <-lifetime.Done():
		}
	}()
	ctx, cancel := context.WithTimeout(lifetime, budget)
	defer cancel()

	// A probe teaches nobody before the group is reachable and before the escrow's log carries a height.
	if err := session.WaitRouterCatalog(ctx); err != nil {
		return
	}
	if err := session.WaitHeightSeedReady(ctx); err != nil {
		return
	}

	// The probe's diff is signed before its send, so the group can be taught now instead of an inference later.
	var catchUp chan error
	onNonceCommitted := func() {
		catchUp = make(chan error, 1)
		go func(done chan<- error) {
			catchUpCtx, cancelCatchUp := context.WithTimeout(ctx, catchUpTimeout)
			defer cancelCatchUp()
			done <- w.catchUp(catchUpCtx, session)
		}(catchUp)
	}

	params := probeParams(model, w.now().Unix())
	nonce, acknowledged, probeErr := w.probe(ctx, session, params, onNonceCommitted)

	var catchUpErr error
	if catchUp != nil {
		catchUpErr = <-catchUp
	}

	if nonce == 0 {
		if w.narrator != nil {
			w.narrator.WarmupFoundNoNonce(escrowID, probeErr)
		}
		return
	}
	w.openLedger(escrowID, model, session)
	w.record(escrowID, nonce, acknowledged, probeErr)
	if probeErr != nil {
		w.settleUnfinishedProbe(lifetime, escrowID, model, params, nonce, acknowledged)
	}
	// A fresh escrow may take no request for minutes, a reserve none at all, so nothing else would carry the probe's Finish.
	session.ArmQuietSettle()

	if w.narrator != nil {
		w.narrator.EscrowWarmed(escrowID, model, nonce, probeErr == nil, catchUpErr)
	}
}

func catchUpAllHosts(ctx context.Context, session registry.EscrowSession) error {
	return session.UserSession().CatchUpAllHosts(ctx)
}

func probeParams(model string, startedAt int64) user.InferenceParams {
	return user.InferenceParams{
		Model:       model,
		Prompt:      probePrompt,
		InputLength: uint64(len(probePrompt)),
		MaxTokens:   probeMaxTokens,
		StartedAt:   startedAt,
	}
}

func dispatchProbe(ctx context.Context, session registry.EscrowSession, params user.InferenceParams, nonceCommitted func()) (uint64, bool, error) {
	sendCtx, cancelSend := context.WithTimeout(ctx, probeTimeout)
	defer cancelSend()

	prepared, err := session.PrepareInferenceFn(func(user.HostBinding) (user.InferenceParams, bool, context.Context, error) {
		return params, false, nil, nil
	})
	if err != nil || prepared == nil {
		return 0, false, err
	}
	nonce, hostIdx := prepared.Nonce(), prepared.HostIdx()
	nonceCommitted()

	// Taken as it arrives: a host that receipts and then hangs never returns the reply to read it from.
	var acknowledged atomic.Bool
	response, sendErr := registry.SendRecovering(sendCtx, session.UserSession(), prepared, nil, func() { acknowledged.Store(true) })
	if sendErr != nil {
		return nonce, acknowledged.Load(), sendErr
	}
	return nonce, acknowledged.Load() || executorAcknowledged(response), session.UserSession().ProcessResponse(hostIdx, response, nonce)
}

func executorAcknowledged(response *host.HostResponse) bool {
	return response != nil && len(response.Receipt) > 0
}

// Not left to the ledger's own sweep, which has not necessarily run on a just-published escrow.
func (w *Prober) openLedger(escrowID, model string, session registry.EscrowSession) {
	if w.ledger == nil || w.epochs == nil {
		return
	}
	epoch := w.epochs.Snapshot().EpochIndex
	if epoch == 0 {
		return
	}
	if err := w.ledger.OpenEscrow(accounting.EscrowMetadata{
		EscrowID:      escrowID,
		Model:         model,
		CreationEpoch: epoch,
		Slots:         session.SnapshotState().Group,
	}); err != nil && w.narrator != nil {
		w.narrator.WarmupLedgerOpenFailed(escrowID, err)
	}
}

func (w *Prober) record(escrowID string, nonce uint64, acknowledged bool, probeErr error) {
	if w.probes == nil {
		return
	}
	w.probes.ProbeRecorded(escrowID, accounting.Attempt{
		Nonce:        nonce,
		Sent:         true,
		Acknowledged: acknowledged,
		Usage:        accounting.UsageLoser,
		Phase:        accounting.PhaseNormal,
		Terminal:     accounting.TerminalWarmupProbe,
	})
}

// Serve and Settle are late bindings. See README.md, "Boundaries".
func (w *Prober) Serve(escrows Escrows) {
	if w != nil {
		w.escrows = escrows
	}
}

func (w *Prober) Settle(posters Posters, timeouts Timeouts, probes Probes) {
	if w != nil {
		w.posters, w.timeouts, w.probes = posters, timeouts, probes
	}
}

// SetNarrator binds the journal the warmup's transitions are written through; call it before the first publication.
func (w *Prober) SetNarrator(narrator warmupNarrator) {
	if w != nil {
		w.narrator = narrator
	}
}
