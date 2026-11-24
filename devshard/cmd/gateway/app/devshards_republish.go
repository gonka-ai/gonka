package app

import (
	"context"
	"slices"
	"time"

	"devshard/bridge"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/logging"
)

// republishOnDevshardWrites keeps routing in step with the rotation lifecycle, which knows no registry.
func (g *gateway) republishOnDevshardWrites(ctx context.Context, unopenedAtBoot int) <-chan struct{} {
	var reopen func() bool
	if g.hosts != nil {
		reopen = func() bool { return retireListedAbsentHosts(g.hosts, g.router, g.escrows) }
	}
	return republishEscrows(ctx, g.devshardWork, unopenedAtBoot, g.publishEscrows, reopen)
}

type absentHostMarks interface {
	AbsentHosts() map[string][]string
	ForgetAbsentHosts(escrowID string)
}

type liveEscrows interface {
	Routable(escrowID string) (scheduler.Escrow, bool)
	Candidates(model string) []scheduler.Escrow
	Snapshot() []registry.EscrowState
	IsBusy(escrowID string) bool
	Retire(escrowID string) error
}

// retireListedAbsentHosts retires each live escrow holding a dead slot whose participant the chain lists again, and reports whether it retired one, so the republish reopens it. See routing.md, "A slot the chain did not list at open".
func retireListedAbsentHosts(hosts HostDirectory, marks absentHostMarks, escrows liveEscrows) bool {
	listed := map[string]bool{}
	isListed := func(participant string) bool {
		known, probed := listed[participant]
		if !probed {
			_, err := hosts.GetHostInfo(participant)
			known = err == nil
			listed[participant] = known
		}
		return known
	}
	retired := false
	marked := marks.AbsentHosts()
	for escrowID, participants := range marked {
		candidate, live := escrows.Routable(escrowID)
		if !live {
			marks.ForgetAbsentHosts(escrowID)
			continue
		}
		if !slices.ContainsFunc(participants, isListed) || !modelServedThroughDrain(escrows, marked, candidate) {
			continue
		}
		logging.Info("reopening escrow: a participant absent at its open is listed again", logkey.Escrow, escrowID)
		if err := escrows.Retire(escrowID); err != nil {
			logging.Error("retiring escrow to reopen it", logkey.Escrow, escrowID, logkey.Error, err)
		}
		marks.ForgetAbsentHosts(escrowID)
		retired = true
	}
	return retired
}

const (
	// unopenedEscrowRetryDelay is the lifecycle tick, so an escrow that would not open is retried as often as rotation looks at it.
	unopenedEscrowRetryDelay = escrow.TickInterval
	// absentHostProbeInterval bounds the chain lookups for dead slots to one per marked participant per lifecycle tick.
	absentHostProbeInterval = escrow.TickInterval
)

// modelServedThroughDrain reports whether retiring the escrow leaves its model served: it closes at once, another of the model's escrows with no absent mark routes, or another routes while none is out of routing.
func modelServedThroughDrain(escrows liveEscrows, marked map[string][]string, candidate scheduler.Escrow) bool {
	if !escrows.IsBusy(candidate.ID) {
		return true
	}
	others := slices.DeleteFunc(escrows.Candidates(candidate.Model), func(other scheduler.Escrow) bool { return other.ID == candidate.ID })
	if slices.ContainsFunc(others, func(other scheduler.Escrow) bool { return len(marked[other.ID]) == 0 }) {
		return true
	}
	for _, state := range escrows.Snapshot() {
		if state.Model == candidate.Model && !state.Accepting {
			return false
		}
	}
	return len(others) > 0
}

// republishEscrows republishes on every devshard write, on a timer while an active escrow is left unopened, and once reopen retires an escrow to reopen it. See operations.md, "Boot".
func republishEscrows(ctx context.Context, writes <-chan struct{}, unopenedAtBoot int, publish func(context.Context) (int, error), reopen func() bool) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		retry := time.NewTimer(unopenedEscrowRetryDelay)
		defer retry.Stop()
		if unopenedAtBoot == 0 {
			retry.Stop()
		}
		var probe <-chan time.Time
		if reopen != nil {
			probes := time.NewTicker(absentHostProbeInterval)
			defer probes.Stop()
			probe = probes.C
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-writes:
			case <-retry.C:
			case <-probe:
				if !reopen() {
					continue
				}
			}
			unopened, err := publish(ctx)
			if err != nil && ctx.Err() == nil {
				logging.Error("republishing escrows", logkey.Error, err)
			}
			if unopened > 0 {
				retry.Reset(unopenedEscrowRetryDelay)
			} else {
				retry.Stop()
			}
		}
	}()
	return done
}

func notify(work chan struct{}) {
	select {
	case work <- struct{}{}:
	default:
	}
}

// HostDirectory is the chain's participant lookup, the one a session open asks.
type HostDirectory interface {
	GetHostInfo(address string) (*bridge.HostInfo, error)
}
