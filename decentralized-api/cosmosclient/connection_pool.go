package cosmosclient

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/logging"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/productscience/inference/x/inference/types"
)

var ErrNoHealthyConnections = errors.New("no healthy connections available")

const (
	DefaultPoolSize       = 3
	DefaultHealthInterval = 30 * time.Second
	DefaultPingTimeout    = 5 * time.Second
)

type ConnectionPool struct {
	clients  []*cosmosclient.Client
	healthy  []atomic.Bool
	mu       sync.RWMutex
	next     uint64
	ctx      context.Context
	cancel   context.CancelFunc
	config   *apiconfig.ConfigManager
	prefix   string
	interval time.Duration
	timeout  time.Duration
}

func NewConnectionPool(ctx context.Context, prefix string, config *apiconfig.ConfigManager, size int) (*ConnectionPool, error) {
	if size <= 0 {
		size = DefaultPoolSize
	}
	poolCtx, cancel := context.WithCancel(ctx)
	p := &ConnectionPool{
		clients:  make([]*cosmosclient.Client, size),
		healthy:  make([]atomic.Bool, size),
		ctx:      poolCtx,
		cancel:   cancel,
		config:   config,
		prefix:   prefix,
		interval: DefaultHealthInterval,
		timeout:  DefaultPingTimeout,
	}
	ok := 0
	for i := range p.clients {
		if c, err := p.create(); err == nil {
			p.clients[i] = c
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

func (p *ConnectionPool) create() (*cosmosclient.Client, error) {
	cfg := p.config.GetChainNodeConfig()
	dir, err := expandPath(cfg.KeyringDir)
	if err != nil {
		return nil, err
	}
	return createBaseCosmosClient(p.ctx, p.prefix, cfg.Url, dir)
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
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.check()
		}
	}
}

func (p *ConnectionPool) check() {
	p.mu.RLock()
	n := len(p.clients)
	clients := make([]*cosmosclient.Client, n)
	copy(clients, p.clients)
	p.mu.RUnlock()

	pingResults := make([]bool, n)
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func(idx int, client *cosmosclient.Client) {
			defer wg.Done()
			pingResults[idx] = client != nil && p.ping(client)
		}(i, c)
	}
	wg.Wait()

	newClients := make([]*cosmosclient.Client, n)
	for i := 0; i < n; i++ {
		if !pingResults[i] {
			p.healthy[i].Store(false)
			if newC, err := p.create(); err == nil {
				newClients[i] = newC
			} else {
				logging.Warn("connection pool: failed to create connection", types.System, "index", i, "error", err)
			}
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	for i := 0; i < n; i++ {
		if newClients[i] != nil {
			oldClient := p.clients[i]
			p.clients[i] = newClients[i]
			p.healthy[i].Store(true)
			if oldClient != nil && oldClient.RPC != nil {
				oldClient.RPC.Stop()
			}
			logging.Debug("connection pool: replaced connection", types.System, "index", i)
		} else if pingResults[i] {
			p.healthy[i].Store(true)
		}
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
	defer p.mu.Unlock()
	for i, c := range p.clients {
		if c != nil && c.RPC != nil {
			c.RPC.Stop()
		}
		p.clients[i] = nil
		p.healthy[i].Store(false)
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
