package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	devshardpkg "devshard"
	"devshard/observability"
	"devshard/transport"
)

const (
	rpcStatsEnvInterval    = "DEVSHARD_GATEWAY_RPC_STATS_INTERVAL"
	rpcStatsEnvTimeout     = "DEVSHARD_GATEWAY_RPC_STATS_TIMEOUT"
	rpcStatsEnvConcurrency = "DEVSHARD_GATEWAY_RPC_STATS_CONCURRENCY"
	rpcStatsEnvDisabled    = "DEVSHARD_GATEWAY_RPC_STATS_DISABLED"

	defaultRPCStatsInterval    = 15 * time.Second
	defaultRPCStatsTimeout     = 2 * time.Second
	defaultRPCStatsConcurrency = 8

	rpcStatsMaxBodyBytes  = 8 << 20
	rpcStatsProcessEscrow = "_"
	rpcStatsAttachFloor   = "floor"
	rpcStatsAttachOther   = "other"
)

var errRPCStatsTooLarge = errors.New("rpc stats body too large")

type rpcStatsConfig struct {
	Interval    time.Duration
	Timeout     time.Duration
	Concurrency int
	Disabled    bool
}

func loadRPCStatsConfig() rpcStatsConfig {
	cfg := rpcStatsConfig{
		Interval:    defaultRPCStatsInterval,
		Timeout:     readDurationEnv(rpcStatsEnvTimeout, defaultRPCStatsTimeout),
		Concurrency: int(readInt64Env(rpcStatsEnvConcurrency, defaultRPCStatsConcurrency)),
		Disabled:    readBoolEnv(rpcStatsEnvDisabled, false),
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = defaultRPCStatsConcurrency
	}
	raw := strings.TrimSpace(os.Getenv(rpcStatsEnvInterval))
	if raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			log.Printf("invalid %s=%q, using %s", rpcStatsEnvInterval, raw, defaultRPCStatsInterval)
		} else if d <= 0 {
			cfg.Disabled = true
		} else {
			cfg.Interval = d
		}
	}
	return cfg
}

type rpcStatsTarget struct {
	Participant string
	Dial        string
}

type rpcStatsHostState struct {
	Participant string
	Dial        string
	Up          bool
	HasSnap     bool
	Snap        transport.RPCStatsSnapshot
}

type rpcStatsWarnFunc func(minute int64, hosts int, banned uint64, sample, zone, endpoint string)

type rpcStatsPoller struct {
	gateway *Gateway
	cfg     rpcStatsConfig
	client  *http.Client
	now     func() time.Time
	warn    rpcStatsWarnFunc
	targets func() map[string]string

	mu           sync.Mutex
	byHost       map[string]*rpcStatsHostState
	warnedMinute atomic.Int64
	ticking      atomic.Bool

	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

func newRPCStatsPoller(gateway *Gateway, cfg rpcStatsConfig) *rpcStatsPoller {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = defaultRPCStatsConcurrency
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultRPCStatsTimeout
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultRPCStatsInterval
	}
	idle := cfg.Interval * 3
	if idle <= 0 {
		idle = defaultRPCStatsInterval * 3
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       idle,
		ResponseHeaderTimeout: cfg.Timeout,
		DisableCompression:    true,
	}
	return &rpcStatsPoller{
		gateway: gateway,
		cfg:     cfg,
		client:  &http.Client{Timeout: cfg.Timeout, Transport: tr},
		now:     time.Now,
		warn:    defaultRPCStatsWarn,
		byHost:  make(map[string]*rpcStatsHostState),
		done:    make(chan struct{}),
	}
}

func (p *rpcStatsPoller) start() {
	if p == nil || p.started {
		return
	}
	p.started = true
	if p.cfg.Disabled {
		close(p.done)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go func() {
		defer close(p.done)
		p.poll(ctx)
		ticker := time.NewTicker(p.cfg.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.poll(ctx)
			}
		}
	}()
}

func (p *rpcStatsPoller) stop() {
	if p == nil {
		return
	}
	if !p.started {
		close(p.done)
		p.started = true
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	<-p.done
}

func (p *rpcStatsPoller) currentTargets() map[string]string {
	if p == nil {
		return nil
	}
	if p.targets != nil {
		return p.targets()
	}
	if p.gateway != nil && p.gateway.phaseGate != nil {
		return p.gateway.phaseGate.InferenceURLs()
	}
	return nil
}

func uniqueInferenceDials(urls map[string]string) []rpcStatsTarget {
	keys := make([]string, 0, len(urls))
	for k := range urls {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seen := make(map[string]struct{}, len(keys))
	out := make([]rpcStatsTarget, 0, len(keys))
	for _, participant := range keys {
		dial := strings.TrimSpace(urls[participant])
		if participant == "" || dial == "" {
			continue
		}
		if _, ok := seen[dial]; ok {
			continue
		}
		seen[dial] = struct{}{}
		out = append(out, rpcStatsTarget{Participant: participant, Dial: dial})
	}
	return out
}

func rpcStatsURL(base string) string {
	return strings.TrimSuffix(strings.TrimSpace(base), "/") + devshardpkg.VersionlessStatsRPCPath()
}

func (p *rpcStatsPoller) poll(ctx context.Context) {
	if p == nil {
		return
	}
	if !p.ticking.CompareAndSwap(false, true) {
		return
	}
	defer p.ticking.Store(false)

	targets := uniqueInferenceDials(p.currentTargets())
	if len(targets) == 0 {
		return
	}

	results := make([]rpcStatsHostState, len(targets))
	sem := make(chan struct{}, p.cfg.Concurrency)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t rpcStatsTarget) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = p.failedState(t)
				return
			}
			defer func() { <-sem }()

			snap, err := p.fetchOne(ctx, t.Dial)
			if err != nil {
				results[i] = p.failedState(t)
				return
			}
			results[i] = rpcStatsHostState{
				Participant: t.Participant,
				Dial:        t.Dial,
				Up:          true,
				HasSnap:     true,
				Snap:        snap,
			}
		}(i, t)
	}
	wg.Wait()
	p.commit(results)
	p.maybeWarn(results)
}

func (p *rpcStatsPoller) failedState(t rpcStatsTarget) rpcStatsHostState {
	st := rpcStatsHostState{Participant: t.Participant, Dial: t.Dial, Up: false}
	p.mu.Lock()
	prev := p.byHost[t.Participant]
	p.mu.Unlock()
	if prev != nil && prev.HasSnap {
		st.HasSnap = true
		st.Snap = prev.Snap
	}
	return st
}

func (p *rpcStatsPoller) commit(results []rpcStatsHostState) {
	next := make(map[string]*rpcStatsHostState, len(results))
	for i := range results {
		st := results[i]
		cp := st
		next[st.Participant] = &cp
	}
	p.mu.Lock()
	p.byHost = next
	p.mu.Unlock()
}

func (p *rpcStatsPoller) hosts() []rpcStatsHostState {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]rpcStatsHostState, 0, len(p.byHost))
	for _, st := range p.byHost {
		if st != nil {
			out = append(out, *st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Participant < out[j].Participant })
	return out
}

func (p *rpcStatsPoller) fetchOne(ctx context.Context, dial string) (transport.RPCStatsSnapshot, error) {
	reqCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rpcStatsURL(dial), nil)
	if err != nil {
		return transport.RPCStatsSnapshot{}, err
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := p.client.Do(req)
	if err != nil {
		return transport.RPCStatsSnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, rpcStatsMaxBodyBytes))
		return transport.RPCStatsSnapshot{}, errors.New("rpc stats status " + resp.Status)
	}
	raw, err := readLimitedBytes(resp.Body, rpcStatsMaxBodyBytes)
	if err != nil {
		return transport.RPCStatsSnapshot{}, err
	}
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		raw, err = gunzipLimited(raw, rpcStatsMaxBodyBytes)
		if err != nil {
			return transport.RPCStatsSnapshot{}, err
		}
	}
	var snap transport.RPCStatsSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return transport.RPCStatsSnapshot{}, err
	}
	return snap, nil
}

func readLimitedBytes(r io.Reader, max int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, errRPCStatsTooLarge
	}
	return raw, nil
}

func gunzipLimited(raw []byte, max int64) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	return readLimitedBytes(gr, max)
}

func (p *rpcStatsPoller) maybeWarn(results []rpcStatsHostState) {
	if p == nil {
		return
	}
	var (
		bannedHosts int
		totalBanned uint64
		sample      string
		minute      int64
	)
	zoneCounts := map[string]uint64{}
	epCounts := map[string]uint64{}
	for _, st := range results {
		if !st.Up || !st.HasSnap {
			continue
		}
		if st.Snap.Host.Banned == 0 && st.Snap.Host.Attach.Banned == 0 {
			continue
		}
		bannedHosts++
		totalBanned += st.Snap.Host.Banned
		if st.Snap.Host.Banned == 0 {
			totalBanned += st.Snap.Host.Attach.Banned
		}
		if sample == "" {
			sample = strings.TrimSpace(st.Snap.HostAddress)
			if sample == "" {
				sample = st.Participant
			}
		}
		if st.Snap.MinuteUnix > minute {
			minute = st.Snap.MinuteUnix
		}
		for _, z := range st.Snap.Host.Zones {
			if z.Banned > 0 {
				zoneCounts[z.Zone] += z.Banned
			}
		}
		for _, e := range st.Snap.Host.Endpoints {
			if e.Banned > 0 {
				epCounts[e.Endpoint] += e.Banned
			}
		}
	}
	if bannedHosts == 0 {
		return
	}
	if minute == 0 && p.now != nil {
		minute = (p.now().Unix()/60 - 1) * 60
	}
	for {
		prev := p.warnedMinute.Load()
		if prev == minute {
			return
		}
		if p.warnedMinute.CompareAndSwap(prev, minute) {
			break
		}
	}
	if p.warn != nil {
		p.warn(minute, bannedHosts, totalBanned, sample, topKey(zoneCounts), topKey(epCounts))
	}
}

func topKey(counts map[string]uint64) string {
	var (
		best string
		n    uint64
	)
	for k, v := range counts {
		if v > n || (v == n && (best == "" || k < best)) {
			best = k
			n = v
		}
	}
	return best
}

func defaultRPCStatsWarn(minute int64, hosts int, banned uint64, sample, zone, endpoint string) {
	observability.Log(context.Background(), observability.LevelWarn, "rpc rate limit gateway closed minute",
		observability.StageReceived, observability.WhereGatewayRPCStats, "", observability.ReasonRateLimited, nil,
		"minute_unix", minute,
		"hosts", hosts,
		"banned", banned,
		"host_address", sample,
		"zone", zone,
		"endpoint", endpoint,
	)
}

func (g *Gateway) handleDebugRPCTraffic(w http.ResponseWriter, r *http.Request) {
	if !allowGetOrHead(w, r) {
		return
	}
	hosts := []map[string]any{}
	if g != nil && g.rpcStats != nil {
		for _, st := range g.rpcStats.hosts() {
			hosts = append(hosts, map[string]any{
				"participant": st.Participant,
				"dial":        st.Dial,
				"up":          st.Up,
				"snapshot":    st.Snap,
			})
		}
	}
	writeJSON(w, map[string]any{"hosts": hosts})
}
