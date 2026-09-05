package mlnodeping

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"common/probe"

	"decentralized-api/broker"
	"decentralized-api/observability"
)

const (
	defaultInterval    = 15 * time.Second
	defaultTimeout     = 2 * time.Second
	defaultConcurrency = 8
)

// Inventory supplies the same broker snapshot federation uses.
type Inventory interface {
	GetNodes() ([]broker.NodeResponse, error)
}

// Config tunes the dapi → mlnode ping job.
type Config struct {
	Interval    time.Duration
	Timeout     time.Duration
	Concurrency int
	Disabled    bool
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = defaultInterval
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Concurrency <= 0 {
		c.Concurrency = defaultConcurrency
	}
	return c
}

type source struct {
	inv Inventory
}

// Targets builds probe destinations from broker inventory. Ping and federation
// share PoCUrl() + url.JoinPath so dial paths cannot drift.
func (s *source) Targets() []probe.Target {
	if s == nil || s.inv == nil {
		return nil
	}
	nodes, err := s.inv.GetNodes()
	if err != nil {
		log.Printf("mlnode_ping: GetNodes: %v", err)
		return nil
	}
	out := make([]probe.Target, 0, len(nodes))
	for _, n := range nodes {
		id := strings.TrimSpace(n.Node.Id)
		base := n.Node.PoCUrl()
		if id == "" || base == "" {
			continue
		}
		pingURL, err := observability.JoinMLNodePoCPath(base, observability.MLNodeClockPath)
		if err != nil {
			_ = observability.LogMLNodeTargetError(id, err)
			continue
		}
		fallbackURL, err := observability.JoinMLNodePoCPath(base, observability.MLNodeReadyzPath)
		if err != nil {
			_ = observability.LogMLNodeTargetError(id, err)
			continue
		}
		out = append(out, probe.Target{
			Key:         id,
			ClockURL:     pingURL,
			FallbackURL: fallbackURL,
		})
	}
	return out
}

type sink struct{}

func (sink) Observe(r probe.Result) { observability.ObserveMLNodePingResult(r) }
func (sink) Forget(key string)      { observability.DeleteMLNodePingMetrics(key) }

type observer struct{}

func (observer) TickStarted()      { observability.IncMLNodePingTicks() }
func (observer) TickSkipped()      { observability.IncMLNodePingTicksSkipped() }
func (observer) TargetCount(n int) { observability.SetMLNodePingTargets(n) }

// Job runs periodic probes against broker ML nodes.
type Job struct {
	cfg    Config
	source *source
	cancel context.CancelFunc
	done   chan struct{}
}

// New builds a job. Call Start to run the scheduler.
func New(inv Inventory, cfg Config) *Job {
	cfg = cfg.withDefaults()
	return &Job{
		cfg:    cfg,
		source: &source{inv: inv},
		done:   make(chan struct{}),
	}
}

// Start launches the probe scheduler. No-op when Disabled.
func (j *Job) Start(ctx context.Context) {
	if j == nil {
		return
	}
	if j.cfg.Disabled {
		close(j.done)
		return
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       j.cfg.Interval * 3,
		ResponseHeaderTimeout: j.cfg.Timeout,
	}
	prober, err := probe.New(probe.Config{
		Interval:      j.cfg.Interval,
		Timeout:       j.cfg.Timeout,
		Concurrency:   j.cfg.Concurrency,
		Jitter:        j.cfg.Interval / 10,
		CapabilityTTL: 10 * time.Minute,
		Transport:     tr,
	})
	if err != nil {
		log.Printf("mlnode_ping: disabled: %v", err)
		close(j.done)
		return
	}
	sched := probe.NewScheduler(prober, j.source, sink{}, observer{})
	runCtx, cancel := context.WithCancel(ctx)
	j.cancel = cancel
	go func() {
		defer close(j.done)
		sched.Run(runCtx)
	}()
}

// Stop cancels the scheduler and waits for exit.
func (j *Job) Stop() {
	if j == nil {
		return
	}
	if j.cancel != nil {
		j.cancel()
	}
	<-j.done
}

// TargetsForTest exposes the current target snapshot (unit tests).
func (j *Job) TargetsForTest() []probe.Target {
	if j == nil || j.source == nil {
		return nil
	}
	return j.source.Targets()
}
