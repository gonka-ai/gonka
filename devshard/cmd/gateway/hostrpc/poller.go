// Package hostrpc polls the hosts the live escrows use for their rate-limiter counters. See README.md.
package hostrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"common/httpguard"

	"devshard"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/registry"
	"devshard/logging"
	"devshard/transport"
)

const (
	statsPath    = devshard.VersionlessRoutePrefix + "/stats/rpc"
	maxBodyBytes = 8 << 20
)

var errTooLarge = errors.New("rpc stats body too large")

type liveDials interface {
	HostDials() []registry.HostDial
}

// Sink takes each finished wave whole, keyed by participant; a nil snapshot is a host that did not answer.
type Sink interface {
	Replace(snapshots map[string]*transport.RPCStatsSnapshot)
}

// Poller reads every live host's closed-minute RPC counters on a wall-clock cadence. See README.md.
type Poller struct {
	live        liveDials
	sink        Sink
	client      *http.Client
	interval    time.Duration
	timeout     time.Duration
	concurrency int
}

// New returns nil when polling is off or its schedule does not hold. See README.md.
func New(settings config.HostRPCStats, live liveDials, sink Sink) *Poller {
	if !settings.Enabled || live == nil || sink == nil {
		return nil
	}
	interval := time.Duration(settings.IntervalMS) * time.Millisecond
	timeout := time.Duration(settings.TimeoutMS) * time.Millisecond
	if interval <= 0 || timeout <= 0 || timeout > interval || settings.Concurrency <= 0 {
		logging.Warn("host rpc stats are off: the poll schedule does not hold",
			logkey.Subsystem, "hostrpc", logkey.Error, "interval, timeout and concurrency must be positive and the timeout at most the interval")
		return nil
	}
	return &Poller{
		live:        live,
		sink:        sink,
		client:      &http.Client{Transport: newGuardedTransport()},
		interval:    interval,
		timeout:     timeout,
		concurrency: int(settings.Concurrency),
	}
}

func (p *Poller) Start(ctx context.Context) {
	if p == nil {
		return
	}
	go p.run(ctx)
}

func (p *Poller) run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		p.wave(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Poller) wave(ctx context.Context) {
	dials := p.dialsByParticipant()
	snapshots := make([]*transport.RPCStatsSnapshot, len(dials))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(p.concurrency, len(dials)) {
		workers.Go(func() {
			for i := range jobs {
				snapshots[i] = p.poll(ctx, dials[i])
			}
		})
	}
feed:
	for i := range dials {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	workers.Wait()
	if ctx.Err() != nil {
		return
	}
	byParticipant := make(map[string]*transport.RPCStatsSnapshot, len(dials))
	for i, dial := range dials {
		byParticipant[dial.ParticipantKey] = snapshots[i]
	}
	p.sink.Replace(byParticipant)
}

func (p *Poller) dialsByParticipant() []registry.HostDial {
	live := p.live.HostDials()
	seen := make(map[string]struct{}, len(live))
	dials := make([]registry.HostDial, 0, len(live))
	for _, dial := range live {
		if _, known := seen[dial.ParticipantKey]; known {
			continue
		}
		seen[dial.ParticipantKey] = struct{}{}
		dials = append(dials, dial)
	}
	return dials
}

func (p *Poller) poll(ctx context.Context, dial registry.HostDial) *transport.RPCStatsSnapshot {
	snapshot, err := p.fetch(ctx, dial.BaseURL)
	if err != nil {
		logging.Debug("host rpc stats poll failed", logkey.Subsystem, "hostrpc", logkey.Host, dial.ParticipantKey, logkey.Error, err)
		return nil
	}
	return &snapshot
}

func (p *Poller) fetch(ctx context.Context, baseURL string) (transport.RPCStatsSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+statsPath, nil)
	if err != nil {
		return transport.RPCStatsSnapshot{}, err
	}
	response, err := p.client.Do(request)
	if err != nil {
		return transport.RPCStatsSnapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return transport.RPCStatsSnapshot{}, fmt.Errorf("rpc stats status %s", response.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	if err != nil {
		return transport.RPCStatsSnapshot{}, err
	}
	if len(raw) > maxBodyBytes {
		return transport.RPCStatsSnapshot{}, errTooLarge
	}
	var snapshot transport.RPCStatsSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return transport.RPCStatsSnapshot{}, err
	}
	return snapshot, nil
}

func newGuardedTransport() *http.Transport {
	guarded := http.DefaultTransport.(*http.Transport).Clone()
	guarded.Proxy = nil
	guarded.DialContext = httpguard.NewDialer().DialContext
	return guarded
}
