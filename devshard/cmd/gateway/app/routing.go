package app

import (
	"errors"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/journal"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/metrics"
	"devshard/cmd/gateway/nonces"
	"devshard/cmd/gateway/perf"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/warmup"
	"devshard/transport"
)

type routingDeps struct {
	Sessions     registry.SessionFactory
	ReadOnly     registry.SessionFactory
	Capacity     *limits.Capacity
	Participants *limits.ParticipantLimiter
	Hosts        *perf.Tracker
	Snapshots    *chain.PhaseObserver
	Config       *config.Holder
	Depletion    *depletionNotice
	Dispatches   *metrics.DispatchRecorder
	Steps        *metrics.StepRecorder
	Served       *metrics.ServedBindingRecorder
	Finishes     *finishNotice
	PeerRPC      *transport.PeerRPCAdoption
	Ledger       *nonces.Recorder
	Journal      *journal.Journal
	Now          func() time.Time
}

// newRouting joins the escrow set to the picker through the capacity model; an unjoined escrow serves nothing.
func newRouting(deps routingDeps) (*registry.Registry, *scheduler.Scheduler, *warmup.Prober, error) {
	if deps.Journal == nil {
		return nil, nil, nil, errors.New("routing: Journal is required")
	}
	if deps.Finishes == nil {
		return nil, nil, nil, errors.New("routing: Finishes is required")
	}
	registryDeps := registry.Deps{
		ServingSessions:  deps.Sessions,
		ReadOnlySessions: deps.ReadOnly,
		Membership:       deps.Capacity,
		Exhaustion:       deps.Depletion,
		Narrator:         deps.Journal,
		Retiring:         deps.Ledger.EscrowRetiring,
		Closing:          deps.Ledger.EscrowClosing,
		ServedBindings:   servedBindingStrikes(deps.Hosts, deps.Journal, deps.Served, deps.Finishes),
		PeerRPC:          deps.PeerRPC,
		Now:              deps.Now,
	}
	prober := warmup.New(deps.Config, deps.Ledger.Book(), deps.Snapshots, deps.Now)
	listeners := escrowPublications{deps.Ledger}
	if prober != nil {
		listeners = append(listeners, prober)
	}
	registryDeps.Publications = listeners
	escrows := registry.New(registryDeps)
	prober.Serve(escrows)
	prober.SetNarrator(deps.Journal)
	router, err := scheduler.NewScheduler(scheduler.Deps{
		Escrows:           escrows,
		Capacity:          deps.Capacity,
		Limiter:           deps.Participants,
		Perf:              deps.Hosts,
		Snapshots:         deps.Snapshots,
		Config:            deps.Config,
		Observer:          tracedDispatches{recorder: deps.Dispatches, steps: deps.Steps, events: deps.Journal, ledger: deps.Ledger},
		Now:               deps.Now,
		OnEscrowExhausted: escrows.Exhausted,
		OnReserveTaken:    escrows.ReserveTaken,
		OnMoneyShort:      deps.Depletion.OnMoneyShort,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return escrows, router, prober, nil
}

// escrowExhaustion reads the scheduler's verdict on a live escrow no request reached, for the escrow manager's tick.
type escrowExhaustion struct {
	escrows *registry.Registry
	router  *scheduler.Scheduler
}

func (e escrowExhaustion) Exhaustion(escrowID string) scheduler.ExhaustionReason {
	candidate, live := e.escrows.Routable(escrowID)
	if !live {
		return scheduler.ExhaustionNone
	}
	return e.router.Exhaustion(candidate)
}

type publicationListener interface {
	EscrowPublished(escrowID, model string)
}

// escrowPublications hands each publication to every listener; each only marks and returns, as the registry calls it under its lock.
type escrowPublications []publicationListener

func (listeners escrowPublications) EscrowPublished(escrowID, model string) {
	for _, listener := range listeners {
		listener.EscrowPublished(escrowID, model)
	}
}
