package cosmosclient

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	igniteclient "github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"google.golang.org/grpc"
)

func TestErrNoHealthyConnections(t *testing.T) {
	err := ErrNoHealthyConnections
	if err.Error() != "no healthy connections available" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func initTestPool(p *ConnectionPool) *ConnectionPool {
	if p.rand == nil {
		p.rand = rand.New(rand.NewSource(1))
	}
	if p.grpcConns == nil && len(p.clients) > 0 {
		p.grpcConns = make([]*grpc.ClientConn, len(p.clients))
	}
	if p.cancels == nil && len(p.clients) > 0 {
		p.cancels = make([]context.CancelFunc, len(p.clients))
	}
	if p.sem == nil {
		checks := capChecks(len(p.clients))
		if checks == 0 {
			checks = DefaultMaxConcurrentChecks
		}
		p.sem = make(chan struct{}, checks)
	}
	return p
}

func makeHealthySlice(vals ...bool) []atomic.Bool {
	result := make([]atomic.Bool, len(vals))
	for i, v := range vals {
		result[i].Store(v)
	}
	return result
}

func TestConnectionPoolHealthyCount(t *testing.T) {
	p := initTestPool(&ConnectionPool{healthy: makeHealthySlice(true, false, true)})
	if p.HealthyCount() != 2 {
		t.Errorf("expected 2 healthy, got %d", p.HealthyCount())
	}
}

func TestConnectionPoolHealthyCountAllHealthy(t *testing.T) {
	p := initTestPool(&ConnectionPool{healthy: makeHealthySlice(true, true, true, true)})
	if p.HealthyCount() != 4 {
		t.Errorf("expected 4 healthy, got %d", p.HealthyCount())
	}
}

func TestConnectionPoolHealthyCountNoneHealthy(t *testing.T) {
	p := initTestPool(&ConnectionPool{healthy: makeHealthySlice(false, false, false)})
	if p.HealthyCount() != 0 {
		t.Errorf("expected 0 healthy, got %d", p.HealthyCount())
	}
}

func TestConnectionPoolHealthyCountEmpty(t *testing.T) {
	p := initTestPool(&ConnectionPool{healthy: []atomic.Bool{}})
	if p.HealthyCount() != 0 {
		t.Errorf("expected 0 healthy, got %d", p.HealthyCount())
	}
}

func TestConnectionPoolGetNoHealthy(t *testing.T) {
	p := initTestPool(&ConnectionPool{
		clients: make([]*igniteclient.Client, 3),
		healthy: makeHealthySlice(false, false, false),
	})
	_, err := p.Get()
	if err != ErrNoHealthyConnections {
		t.Errorf("expected ErrNoHealthyConnections, got %v", err)
	}
}

func TestConnectionPoolGetNilClients(t *testing.T) {
	p := initTestPool(&ConnectionPool{
		clients: []*igniteclient.Client{nil, nil, nil},
		healthy: makeHealthySlice(true, true, true),
	})
	_, err := p.Get()
	if err != ErrNoHealthyConnections {
		t.Errorf("expected ErrNoHealthyConnections, got %v", err)
	}
}

func TestConnectionPoolRoundRobin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c1 := &igniteclient.Client{}
	c2 := &igniteclient.Client{}
	c3 := &igniteclient.Client{}

	p := initTestPool(&ConnectionPool{
		clients: []*igniteclient.Client{c1, c2, c3},
		healthy: makeHealthySlice(true, true, true),
		ctx:     ctx,
		cancel:  cancel,
	})

	got1, err := p.Get()
	if err != nil {
		t.Fatalf("unexpected error from Get(): %v", err)
	}
	got2, err := p.Get()
	if err != nil {
		t.Fatalf("unexpected error from Get(): %v", err)
	}
	got3, err := p.Get()
	if err != nil {
		t.Fatalf("unexpected error from Get(): %v", err)
	}
	got4, err := p.Get()
	if err != nil {
		t.Fatalf("unexpected error from Get(): %v", err)
	}

	if got1 != c1 || got2 != c2 || got3 != c3 || got4 != c1 {
		t.Errorf("expected round-robin sequence c1, c2, c3, c1; got %p, %p, %p, %p", got1, got2, got3, got4)
	}
}

func TestConnectionPoolClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := initTestPool(&ConnectionPool{
		clients: make([]*igniteclient.Client, 2),
		healthy: makeHealthySlice(true, true),
		ctx:     ctx,
		cancel:  cancel,
	})
	p.Close()
	select {
	case <-p.ctx.Done():
	default:
		t.Error("context should be cancelled after Close")
	}
}

func TestConnectionPoolConcurrentGet(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clients := make([]*igniteclient.Client, 5)
	for i := range clients {
		clients[i] = &igniteclient.Client{}
	}

	p := initTestPool(&ConnectionPool{
		clients: clients,
		healthy: makeHealthySlice(true, true, true, true, true),
		ctx:     ctx,
		cancel:  cancel,
	})

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := p.Get()
			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConnectionPoolConcurrentHealthyCount(t *testing.T) {
	p := initTestPool(&ConnectionPool{
		healthy: makeHealthySlice(true, false, true, true, false),
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count := p.HealthyCount()
			if count != 3 {
				t.Errorf("expected 3 healthy, got %d", count)
			}
		}()
	}
	wg.Wait()
}

func TestConnectionPoolSkipsUnhealthy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c2 := &igniteclient.Client{}

	p := initTestPool(&ConnectionPool{
		clients: []*igniteclient.Client{nil, c2, nil},
		healthy: makeHealthySlice(false, true, false),
		ctx:     ctx,
		cancel:  cancel,
	})

	got, err := p.Get()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if got != c2 {
		t.Error("expected to get the only healthy client")
	}
}

func TestConnectionPoolIntervalAndTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := initTestPool(&ConnectionPool{
		clients:  make([]*igniteclient.Client, 2),
		healthy:  makeHealthySlice(true, true),
		ctx:      ctx,
		cancel:   cancel,
		interval: 30 * time.Second,
		timeout:  10 * time.Second,
	})

	if p.interval != 30*time.Second {
		t.Errorf("expected interval 30s, got %v", p.interval)
	}
	if p.timeout != 10*time.Second {
		t.Errorf("expected timeout 10s, got %v", p.timeout)
	}
}

func TestConnectionPoolHealthLoopStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := initTestPool(&ConnectionPool{
		clients:  make([]*igniteclient.Client, 1),
		healthy:  makeHealthySlice(true),
		ctx:      ctx,
		cancel:   cancel,
		interval: 1 * time.Hour,
	})

	done := make(chan struct{})
	go func() {
		p.healthLoop()
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Error("healthLoop did not stop after cancel")
	}
}

func TestConnectionPoolGetWrapsAround(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c3 := &igniteclient.Client{}

	p := initTestPool(&ConnectionPool{
		clients: []*igniteclient.Client{nil, nil, c3},
		healthy: makeHealthySlice(false, false, true),
		ctx:     ctx,
		cancel:  cancel,
	})

	got, err := p.Get()
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if got != c3 {
		t.Error("expected to wrap around and find c3")
	}
}

func TestConnectionPoolMutexProtection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clients := make([]*igniteclient.Client, 3)
	for i := range clients {
		clients[i] = &igniteclient.Client{}
	}

	p := initTestPool(&ConnectionPool{
		clients: clients,
		healthy: makeHealthySlice(true, true, true),
		ctx:     ctx,
		cancel:  cancel,
	})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				p.Get()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				p.HealthyCount()
			}
		}()
	}
	wg.Wait()
}

func TestConnectionPoolDefaultConstants(t *testing.T) {
	if DefaultPoolSize != 3 {
		t.Errorf("expected DefaultPoolSize 3, got %d", DefaultPoolSize)
	}
	if DefaultHealthInterval != 30*time.Second {
		t.Errorf("expected DefaultHealthInterval 30s, got %v", DefaultHealthInterval)
	}
	if DefaultPingTimeout != 5*time.Second {
		t.Errorf("expected DefaultPingTimeout 5s, got %v", DefaultPingTimeout)
	}
}

func TestConnectionPoolChecksCap(t *testing.T) {
	if capChecks(3) != 3 {
		t.Errorf("expected checks 3, got %d", capChecks(3))
	}
	if capChecks(DefaultMaxConcurrentChecks) != DefaultMaxConcurrentChecks {
		t.Errorf("expected checks %d, got %d", DefaultMaxConcurrentChecks, capChecks(DefaultMaxConcurrentChecks))
	}
	if capChecks(DefaultMaxConcurrentChecks+1) != DefaultMaxConcurrentChecks {
		t.Errorf("expected checks %d, got %d", DefaultMaxConcurrentChecks, capChecks(DefaultMaxConcurrentChecks+1))
	}
}
