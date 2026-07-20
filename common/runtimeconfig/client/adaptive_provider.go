package client

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type adaptiveProvider struct {
	*baseProvider
	cfg AdaptiveConfig

	events       chan runnerEvent
	done         chan struct{}
	activeSource atomic.Value // string

	mu           sync.Mutex
	runnerCancel context.CancelFunc
	runnerWG     sync.WaitGroup
	bootAt       time.Time

	lastGRPCApply        atomic.Int64 // unix nano; 0 = no apply yet
	grpcErrorsSinceApply atomic.Int32
	failbackStreak       int
}

func (p *adaptiveProvider) Wait() {
	<-p.done
}

func (p *adaptiveProvider) ActiveSource() string {
	if v := p.activeSource.Load(); v != nil {
		return v.(string)
	}
	return SourceActiveGRPC
}

func (p *adaptiveProvider) supervisor(ctx context.Context) {
	defer close(p.done)

	p.bootAt = p.cfg.Clock.Now()

	err := probeGRPC(ctx, p.cfg.GRPC, p.cfg.ProbeTimeout)
	if status.Code(err) == codes.Unimplemented {
		p.startChain(ctx, "boot_probe_unimplemented")
	} else {
		p.startGRPC(ctx, "boot_probe")
	}

	var staleC, reprobeC <-chan time.Time
	resetTimers := func() {
		staleC = nil
		reprobeC = nil
		switch p.ActiveSource() {
		case SourceActiveGRPC:
			staleC = p.cfg.Clock.After(p.cfg.StaleCheckInterval)
		case SourceActiveChain:
			reprobeC = p.cfg.Clock.After(p.cfg.GRPCReprobe)
		}
	}
	resetTimers()

	for {
		select {
		case <-ctx.Done():
			p.cancelRunner()
			return

		case ev := <-p.events:
			switch ev.kind {
			case runnerEventApplied:
				p.lastGRPCApply.Store(p.cfg.Clock.Now().UnixNano())
				p.grpcErrorsSinceApply.Store(0)
			case runnerEventPollError:
				p.grpcErrorsSinceApply.Add(1)
			case runnerEventUnimplemented:
				if p.ActiveSource() != SourceActiveChain {
					p.startChain(ctx, "unimplemented")
					resetTimers()
				}
			}

		case <-staleC:
			if p.shouldFailover() {
				p.startChain(ctx, "stale_window")
				resetTimers()
			} else {
				staleC = p.cfg.Clock.After(p.cfg.StaleCheckInterval)
			}

		case <-reprobeC:
			if p.grpcProbeHealthy(ctx) {
				p.failbackStreak++
				if p.failbackStreak >= p.cfg.FailbackProbes {
					p.startGRPC(ctx, "failback")
					resetTimers()
					continue
				}
			} else {
				p.failbackStreak = 0
			}
			reprobeC = p.cfg.Clock.After(p.cfg.GRPCReprobe)
		}
	}
}

func (p *adaptiveProvider) shouldFailover() bool {
	if p.ActiveSource() != SourceActiveGRPC {
		return false
	}
	if p.grpcErrorsSinceApply.Load() == 0 {
		return false
	}
	now := p.cfg.Clock.Now()
	last := p.lastGRPCApply.Load()
	var since time.Duration
	if last == 0 {
		since = now.Sub(p.bootAt)
	} else {
		since = now.Sub(time.Unix(0, last))
	}
	return since >= p.cfg.GRPCStale
}

func (p *adaptiveProvider) grpcProbeHealthy(ctx context.Context) bool {
	err := probeGRPC(ctx, p.cfg.GRPC, p.cfg.ProbeTimeout)
	if err == nil {
		return true
	}
	if status.Code(err) == codes.Unimplemented {
		return false
	}
	return false
}

func (p *adaptiveProvider) cancelRunner() {
	p.mu.Lock()
	cancel := p.runnerCancel
	p.runnerCancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (p *adaptiveProvider) stopRunner() {
	p.cancelRunner()
	p.runnerWG.Wait()
}

func (p *adaptiveProvider) startGRPC(parent context.Context, reason string) {
	p.stopRunner()
	from := p.ActiveSource()
	p.activeSource.Store(SourceActiveGRPC)
	p.lastGRPCApply.Store(0)
	p.grpcErrorsSinceApply.Store(0)
	p.failbackStreak = 0

	runnerCtx, cancel := context.WithCancel(parent)
	p.mu.Lock()
	p.runnerCancel = cancel
	p.mu.Unlock()

	p.logSwitch(from, SourceActiveGRPC, reason)
	p.runnerWG.Add(1)
	go func() {
		defer p.runnerWG.Done()
		newGRPCRunner(p.baseProvider, p.cfg.GRPC).run(runnerCtx, p.events)
	}()
}

func (p *adaptiveProvider) startChain(parent context.Context, reason string) {
	p.stopRunner()
	from := p.ActiveSource()
	p.activeSource.Store(SourceActiveChain)
	p.failbackStreak = 0

	runnerCtx, cancel := context.WithCancel(parent)
	p.mu.Lock()
	p.runnerCancel = cancel
	p.mu.Unlock()

	p.logSwitch(from, SourceActiveChain, reason)

	runner := newChainRunner(p.baseProvider, p.cfg.Chain)
	initCtx, initCancel := context.WithTimeout(parent, p.cfg.Chain.InitialTimeout)
	runner.refresh(initCtx)
	initCancel()
	p.runnerWG.Add(1)
	go func() {
		defer p.runnerWG.Done()
		runner.run(runnerCtx)
	}()
}

func (p *adaptiveProvider) logSwitch(from, to, reason string) {
	if from == to {
		return
	}
	if to == SourceActiveChain {
		p.cfg.Log.Warn("runtime params: source switch",
			"from", from, "to", to, "reason", reason)
		return
	}
	p.cfg.Log.Info("runtime params: source switch",
		"from", from, "to", to, "reason", reason)
}
