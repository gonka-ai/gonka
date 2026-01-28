package cosmosclient

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/logging"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/productscience/inference/x/inference/types"
)

var ErrNoHealthyConnections = errors.New("no healthy connections available")

const (
	DefaultPoolSize            = 3
	DefaultHealthInterval      = 30 * time.Second
	DefaultPingTimeout         = 5 * time.Second
	DefaultMaxConcurrentChecks = 10
	DefaultHealthJitter        = 0.2
)

type ConnectionPool struct {
	clients  []*cosmosclient.Client
	cancels  []context.CancelFunc
	healthy  []atomic.Bool
	mu       sync.RWMutex
	next     uint64
	ctx      context.Context
	cancel   context.CancelFunc
	config   *apiconfig.ConfigManager
	prefix   string
	interval time.Duration
	timeout  time.Duration
	checks   int
	rand     *rand.Rand
	sem      chan struct{}
}

func NewConnectionPool(ctx context.Context, prefix string, config *apiconfig.ConfigManager, size int) (*ConnectionPool, error) {
	if size <= 0 {
		size = DefaultPoolSize
	}
	checks := capChecks(size)
	if size > DefaultMaxConcurrentChecks {
		logging.Warn("connection pool: large pool size", types.System, "size", size, "maxConcurrentChecks", checks)
	}
	poolCtx, cancel := context.WithCancel(ctx)
	p := &ConnectionPool{
		clients:  make([]*cosmosclient.Client, size),
		cancels:  make([]context.CancelFunc, size),
		healthy:  make([]atomic.Bool, size),
		ctx:      poolCtx,
		cancel:   cancel,
		config:   config,
		prefix:   prefix,
		interval: DefaultHealthInterval,
		timeout:  DefaultPingTimeout,
		checks:   checks,
		rand:     rand.New(rand.NewSource(time.Now().UnixNano())),
		sem:      make(chan struct{}, checks),
	}
	ok := 0
	for i := range p.clients {
		if c, cCancel, err := p.create(); err == nil {
			p.clients[i] = c
			p.cancels[i] = cCancel
			p.healthy[i].Store(true)
			ok++
		}
	}
	if ok == 0 {
		cancel()
		return nil, ErrNoHealthyConnections
	}
	go p.healthLoop()
	return p, nil
}

func capChecks(size int) int {
	if size > DefaultMaxConcurrentChecks {
		return DefaultMaxConcurrentChecks
	}
	return size
}

func (p *ConnectionPool) create() (*cosmosclient.Client, context.CancelFunc, error) {
	cfg := p.config.GetChainNodeConfig()
	dir, err := expandPath(cfg.KeyringDir)
	if err != nil {
		return nil, nil, err
	}
	clientCtx, cancel := context.WithCancel(p.ctx)
	c, err := createBaseCosmosClient(clientCtx, p.prefix, cfg.Url, dir)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return c, cancel, nil
}

func (p *ConnectionPool) Get() (*cosmosclient.Client, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := len(p.clients)
	start := atomic.AddUint64(&p.next, 1) - 1
	for i := 0; i < n; i++ {
		idx := int((start + uint64(i)) % uint64(n))
		if p.clients[idx] != nil && p.healthy[idx].Load() {
			return p.clients[idx], nil
		}
	}
	return nil, ErrNoHealthyConnections
}

func (p *ConnectionPool) healthLoop() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-time.After(p.jitterDuration(p.interval)):
			p.check()
		}
	}
}

func (p *ConnectionPool) jitterDuration(base time.Duration) time.Duration {
	factor := 1 + ((p.rand.Float64()*2 - 1) * DefaultHealthJitter)
	return time.Duration(float64(base) * factor)
}

func (p *ConnectionPool) check() {
	clients := p.snapshotClients()
	pingResults := p.pingClients(clients)
	newClients, newCancels := p.buildReplacements(pingResults)
	p.swapClients(pingResults, newClients, newCancels)
}

func (p *ConnectionPool) snapshotClients() []*cosmosclient.Client {
	p.mu.RLock()
	defer p.mu.RUnlock()
	clients := make([]*cosmosclient.Client, len(p.clients))
	copy(clients, p.clients)
	return clients
}

func (p *ConnectionPool) pingClients(clients []*cosmosclient.Client) []bool {
	pingResults := make([]bool, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		p.sem <- struct{}{}
		go func(idx int, client *cosmosclient.Client) {
			defer wg.Done()
			defer func() { <-p.sem }()
			pingResults[idx] = client != nil && p.ping(client)
		}(i, c)
	}
	wg.Wait()
	return pingResults
}

func (p *ConnectionPool) buildReplacements(pingResults []bool) ([]*cosmosclient.Client, []context.CancelFunc) {
	newClients := make([]*cosmosclient.Client, len(pingResults))
	newCancels := make([]context.CancelFunc, len(pingResults))
	for i := range pingResults {
		if pingResults[i] {
			continue
		}
		if newC, newCancel, err := p.create(); err == nil {
			newClients[i] = newC
			newCancels[i] = newCancel
		} else {
			logging.Warn("connection pool: failed to create connection", types.System, "index", i, "error", err)
		}
	}
	return newClients, newCancels
}

func (p *ConnectionPool) swapClients(pingResults []bool, newClients []*cosmosclient.Client, newCancels []context.CancelFunc) {
	var toCancel []context.CancelFunc
	var toStop []*cosmosclient.Client
	p.mu.Lock()
	for i := 0; i < len(p.clients); i++ {
		if newClients[i] != nil {
			oldClient := p.clients[i]
			oldCancel := p.cancels[i]
			p.clients[i] = newClients[i]
			p.healthy[i].Store(true)
			p.cancels[i] = newCancels[i]
			if oldCancel != nil {
				toCancel = append(toCancel, oldCancel)
			}
			if oldClient != nil && oldClient.RPC != nil {
				toStop = append(toStop, oldClient)
			}
			logging.Debug("connection pool: replaced connection", types.System, "index", i)
		} else if pingResults[i] {
			p.healthy[i].Store(true)
		} else {
			p.healthy[i].Store(false)
		}
	}
	p.mu.Unlock()
	for _, cancel := range toCancel {
		cancel()
	}
	for _, client := range toStop {
		client.RPC.Stop()
	}
}

func (p *ConnectionPool) ping(c *cosmosclient.Client) bool {
	if c == nil || c.RPC == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(p.ctx, p.timeout)
	defer cancel()
	_, err := c.RPC.Status(ctx)
	return err == nil
}

func (p *ConnectionPool) Close() {
	p.cancel()
	p.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(p.cancels))
	clients := make([]*cosmosclient.Client, 0, len(p.clients))
	for i, c := range p.clients {
		if p.cancels[i] != nil {
			cancels = append(cancels, p.cancels[i])
		}
		if c != nil && c.RPC != nil {
			clients = append(clients, c)
		}
		p.clients[i] = nil
		p.cancels[i] = nil
		p.healthy[i].Store(false)
	}
	p.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	for _, c := range clients {
		c.RPC.Stop()
	}
}

func (p *ConnectionPool) HealthyCount() int {
	n := 0
	for i := range p.healthy {
		if p.healthy[i].Load() {
			n++
		}
	}
	return n
}
