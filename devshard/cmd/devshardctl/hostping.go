package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"common/probe"

	"devshard/transport"
)

const (
	hostPingEnvInterval    = "DEVSHARD_GATEWAY_HOST_PING_INTERVAL"
	hostPingEnvTimeout     = "DEVSHARD_GATEWAY_HOST_PING_TIMEOUT"
	hostPingEnvConcurrency = "DEVSHARD_GATEWAY_HOST_PING_CONCURRENCY"
	hostPingEnvDisabled    = "DEVSHARD_GATEWAY_HOST_PING_DISABLED"

	defaultHostPingInterval    = 15 * time.Second
	defaultHostPingTimeout     = 2 * time.Second
	defaultHostPingConcurrency = 8
	hostPingReconcileInterval  = 5 * time.Minute
)

type hostPingConfig struct {
	Interval    time.Duration
	Timeout     time.Duration
	Concurrency int
	Disabled    bool
}

func loadHostPingConfig() hostPingConfig {
	cfg := hostPingConfig{
		Interval:    readDurationEnv(hostPingEnvInterval, defaultHostPingInterval),
		Timeout:     readDurationEnv(hostPingEnvTimeout, defaultHostPingTimeout),
		Concurrency: int(readInt64Env(hostPingEnvConcurrency, defaultHostPingConcurrency)),
		Disabled:    readBoolEnv(hostPingEnvDisabled, false),
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = defaultHostPingConcurrency
	}
	return cfg
}

func readDurationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("invalid %s=%q, using %s", name, raw, fallback)
		return fallback
	}
	return d
}

type escrowDialMeta struct {
	routePrefix    string
	participantKey string
}

type hostPingEntry struct {
	refcount        int
	lastUsed        time.Time
	routePrefix     string
	participantKeys map[string]struct{}
	escrows         map[string]struct{}
}

// hostPingTargets is dial-keyed; own mutex (not g.mu).
// An escrow may hold refs on multiple dials (multi-host group).
type hostPingTargets struct {
	mu             sync.Mutex
	byDial         map[string]*hostPingEntry
	escrowDials    map[string]map[string]escrowDialMeta // escrowID → dial → meta
	escrowReleased map[string]struct{}
}

func newHostPingTargets() *hostPingTargets {
	return &hostPingTargets{
		byDial:         make(map[string]*hostPingEntry),
		escrowDials:    make(map[string]map[string]escrowDialMeta),
		escrowReleased: make(map[string]struct{}),
	}
}

func normalizeDial(dial string) string {
	return strings.TrimRight(strings.TrimSpace(dial), "/")
}

func joinProbePath(dial, routePrefix, leaf string) string {
	dial = normalizeDial(dial)
	prefix := strings.TrimSpace(routePrefix)
	if prefix == "" {
		prefix = transport.DefaultRoutePrefix()
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	prefix = strings.TrimRight(prefix, "/")
	if !strings.HasPrefix(leaf, "/") {
		leaf = "/" + leaf
	}
	return dial + prefix + leaf
}

// ObserveEscrowHost records first successful inference for an escrow→host dial.
// Idempotent per (escrow, dial) for refcount; participant keys accumulate.
// Returns true when the dial's routePrefix changed so the caller can
// Invalidate the capability cache (rediscover /clock without waiting on TTL).
func (t *hostPingTargets) ObserveEscrowHost(escrowID, dial, routePrefix, participantKey string) (prefixChanged bool) {
	if t == nil {
		return false
	}
	escrowID = strings.TrimSpace(escrowID)
	dial = normalizeDial(dial)
	participantKey = strings.TrimSpace(participantKey)
	routePrefix = strings.TrimSpace(routePrefix)
	if escrowID == "" || dial == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, released := t.escrowReleased[escrowID]; released {
		return false
	}
	held := t.escrowDials[escrowID]
	if held == nil {
		held = make(map[string]escrowDialMeta)
		t.escrowDials[escrowID] = held
	}
	if meta, ok := held[dial]; ok {
		if participantKey != "" && participantKey != meta.participantKey {
			meta.participantKey = participantKey
			held[dial] = meta
			if e := t.byDial[dial]; e != nil {
				e.participantKeys[participantKey] = struct{}{}
				e.lastUsed = time.Now()
			}
		}
		if e := t.byDial[dial]; e != nil && routePrefix != "" && e.routePrefix != routePrefix {
			e.routePrefix = routePrefix
			meta.routePrefix = routePrefix
			held[dial] = meta
			e.lastUsed = time.Now()
			return true
		}
		return false
	}
	e := t.byDial[dial]
	prevPrefix := ""
	if e == nil {
		e = &hostPingEntry{
			participantKeys: make(map[string]struct{}),
			escrows:         make(map[string]struct{}),
			routePrefix:     routePrefix,
		}
		t.byDial[dial] = e
	} else {
		prevPrefix = e.routePrefix
	}
	if routePrefix != "" {
		e.routePrefix = routePrefix
	}
	e.refcount++
	e.lastUsed = time.Now()
	e.escrows[escrowID] = struct{}{}
	if participantKey != "" {
		e.participantKeys[participantKey] = struct{}{}
	}
	held[dial] = escrowDialMeta{
		routePrefix:    e.routePrefix,
		participantKey: participantKey,
	}
	return prevPrefix != "" && routePrefix != "" && prevPrefix != routePrefix
}

// ReleaseEscrow drops every dial ref held by the escrow. Idempotent.
// Returns dials that left the set (refcount hit 0) with their participant keys
// at removal time, for DeleteLabelValues.
func (t *hostPingTargets) ReleaseEscrow(escrowID string) (removed map[string][]string) {
	if t == nil {
		return nil
	}
	escrowID = strings.TrimSpace(escrowID)
	if escrowID == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.escrowReleased[escrowID]; ok {
		return nil
	}
	t.escrowReleased[escrowID] = struct{}{}
	held := t.escrowDials[escrowID]
	delete(t.escrowDials, escrowID)
	if len(held) == 0 {
		return nil
	}
	removed = make(map[string][]string)
	for dial := range held {
		e := t.byDial[dial]
		if e == nil {
			continue
		}
		delete(e.escrows, escrowID)
		e.refcount--
		if e.refcount <= 0 {
			participants := make([]string, 0, len(e.participantKeys))
			for pk := range e.participantKeys {
				participants = append(participants, pk)
			}
			delete(t.byDial, dial)
			removed[dial] = participants
			continue
		}
		// Rebuild participant keys from remaining escrow refs on this dial.
		e.participantKeys = make(map[string]struct{})
		for id := range e.escrows {
			for d, meta := range t.escrowDials[id] {
				if d == dial && meta.participantKey != "" {
					e.participantKeys[meta.participantKey] = struct{}{}
				}
			}
		}
	}
	return removed
}

func (t *hostPingTargets) Targets() []probe.Target {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]probe.Target, 0, len(t.byDial))
	for dial, e := range t.byDial {
		if e.refcount <= 0 {
			continue
		}
		out = append(out, probe.Target{
			Key:         dial,
			ClockURL:     joinProbePath(dial, e.routePrefix, "/clock"),
			FallbackURL: joinProbePath(dial, e.routePrefix, "/healthz"),
		})
	}
	return out
}

func (t *hostPingTargets) participantKeys(dial string) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.byDial[normalizeDial(dial)]
	if e == nil {
		return nil
	}
	out := make([]string, 0, len(e.participantKeys))
	for pk := range e.participantKeys {
		out = append(out, pk)
	}
	return out
}

func (t *hostPingTargets) snapshotStats() (targets int, refSum int, escrowDialRefs int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	targets = len(t.byDial)
	for _, e := range t.byDial {
		refSum += e.refcount
	}
	for _, held := range t.escrowDials {
		escrowDialRefs += len(held)
	}
	return targets, refSum, escrowDialRefs
}

func (t *hostPingTargets) refcount(dial string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.byDial[normalizeDial(dial)]; e != nil {
		return e.refcount
	}
	return 0
}

func (t *hostPingTargets) hasEscrow(escrowID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.escrowDials[strings.TrimSpace(escrowID)]
	return ok
}

type hostPingSink struct {
	metrics *DevshardMetrics
	targets *hostPingTargets
}

func (s *hostPingSink) Observe(r probe.Result) {
	if s == nil || s.metrics == nil {
		return
	}
	s.metrics.ObserveHostPingResult(r)
	if s.targets != nil {
		for _, pk := range s.targets.participantKeys(r.Key) {
			s.metrics.SetHostPingParticipantInfo(r.Key, pk, true)
		}
	}
}

func (s *hostPingSink) Forget(key string) {
	if s == nil || s.metrics == nil {
		return
	}
	var participants []string
	if s.targets != nil {
		participants = s.targets.participantKeys(key)
	}
	s.metrics.DeleteHostPingMetrics(key, participants)
}

type hostPingObserver struct {
	metrics *DevshardMetrics
}

func (o *hostPingObserver) TickStarted() {
	if o != nil && o.metrics != nil {
		o.metrics.IncHostPingTicks()
	}
}

func (o *hostPingObserver) TickSkipped() {
	if o != nil && o.metrics != nil {
		o.metrics.IncHostPingTicksSkipped()
	}
}

func (o *hostPingObserver) TargetCount(n int) {
	if o != nil && o.metrics != nil {
		o.metrics.SetHostPingTargets(n)
	}
}

type hostPingJob struct {
	cfg     hostPingConfig
	targets *hostPingTargets
	metrics *DevshardMetrics
	prober  *probe.Prober
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

func newHostPingJob(metrics *DevshardMetrics, cfg hostPingConfig) *hostPingJob {
	return &hostPingJob{
		cfg:     cfg,
		targets: newHostPingTargets(),
		metrics: metrics,
		done:    make(chan struct{}),
	}
}

func (j *hostPingJob) start() {
	if j == nil || j.started {
		return
	}
	j.started = true
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
		log.Printf("host_ping: disabled: %v", err)
		close(j.done)
		return
	}
	j.prober = prober
	sink := &hostPingSink{metrics: j.metrics, targets: j.targets}
	obs := &hostPingObserver{metrics: j.metrics}
	sched := probe.NewScheduler(prober, j.targets, sink, obs)

	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	go func() {
		defer close(j.done)
		go j.reconcileLoop(ctx)
		sched.Run(ctx)
	}()
}

func (j *hostPingJob) stop() {
	if j == nil {
		return
	}
	if !j.started {
		close(j.done)
		j.started = true
		return
	}
	if j.cancel != nil {
		j.cancel()
	}
	<-j.done
}

func (j *hostPingJob) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(hostPingReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			targets, refSum, escrowDialRefs := j.targets.snapshotStats()
			if refSum != escrowDialRefs {
				log.Printf("host_ping: reconcile discrepancy targets=%d refSum=%d escrowDialRefs=%d", targets, refSum, escrowDialRefs)
			}
		}
	}
}

func (j *hostPingJob) ObserveEscrowHost(escrowID, dial, routePrefix, participantKey string) {
	if j == nil || j.cfg.Disabled {
		return
	}
	if j.targets.ObserveEscrowHost(escrowID, dial, routePrefix, participantKey) {
		j.InvalidateDial(dial)
	}
	if j.metrics != nil && participantKey != "" {
		j.metrics.SetHostPingParticipantInfo(normalizeDial(dial), participantKey, true)
	}
}

// InvalidateDial clears the capability cache for a dial so the next probe
// rediscovers /clock after a RoutePrefix / approved-version change.
func (j *hostPingJob) InvalidateDial(dial string) {
	if j == nil || j.prober == nil {
		return
	}
	j.prober.Invalidate(normalizeDial(dial))
}

func (j *hostPingJob) ReleaseEscrow(escrowID string) {
	if j == nil {
		return
	}
	removed := j.targets.ReleaseEscrow(escrowID)
	if j.metrics == nil {
		return
	}
	for dial, participants := range removed {
		j.metrics.DeleteHostPingMetrics(dial, participants)
	}
}

type hostDialer interface {
	BaseURL() string
	RoutePrefix() string
}

// hostClientDialInfo extracts dial base URL and route prefix from a HostClient.
func hostClientDialInfo(c any) (dial, routePrefix string, ok bool) {
	d, ok := c.(hostDialer)
	if !ok || d == nil {
		return "", "", false
	}
	dial = normalizeDial(d.BaseURL())
	if dial == "" {
		return "", "", false
	}
	return dial, d.RoutePrefix(), true
}
