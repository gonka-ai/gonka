package pocstream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"decentralized-api/logging"
	"decentralized-api/pocstream/gen"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type PhaseGate interface {
	StreamActive() bool
}

type StreamTarget struct {
	NodeID  string
	Address string
}

type TargetSource func() ([]StreamTarget, error)

type DialFunc func(address string) (gen.PoCCallbackStreamClient, io.Closer, error)

type Config struct {
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	SyncInterval   time.Duration
}

func DefaultConfig() Config {
	return Config{
		InitialBackoff: time.Second,
		MaxBackoff:     30 * time.Second,
		SyncInterval:   5 * time.Second,
	}
}

type managedStream struct {
	nodeID  string
	address string
	cancel  context.CancelFunc

	mu          sync.Mutex
	resumeAfter string
}

func (s *managedStream) setResumeAfter(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumeAfter = id
}

func (s *managedStream) getResumeAfter() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resumeAfter
}

type Manager struct {
	handler http.Handler
	gate    PhaseGate
	targets TargetSource
	dial    DialFunc
	cfg     Config

	mu      sync.Mutex
	rootCtx context.Context
	streams map[string]*managedStream
}

type Option func(*Manager)

func WithDialer(dial DialFunc) Option {
	return func(m *Manager) { m.dial = dial }
}

func WithConfig(cfg Config) Option {
	return func(m *Manager) { m.cfg = cfg }
}

func NewManager(handler http.Handler, gate PhaseGate, targets TargetSource, opts ...Option) *Manager {
	m := &Manager{
		handler: handler,
		gate:    gate,
		targets: targets,
		dial:    grpcDial,
		cfg:     DefaultConfig(),
		rootCtx: context.Background(),
		streams: make(map[string]*managedStream),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func grpcDial(address string) (gen.PoCCallbackStreamClient, io.Closer, error) {
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return gen.NewPoCCallbackStreamClient(conn), conn, nil
}

func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.rootCtx = ctx
	m.mu.Unlock()

	go func() {
		ticker := time.NewTicker(m.cfg.SyncInterval)
		defer ticker.Stop()
		m.sync()
		for {
			select {
			case <-ctx.Done():
				m.StopAll()
				return
			case <-ticker.C:
				m.sync()
			}
		}
	}()
}

func (m *Manager) sync() {
	if !m.gate.StreamActive() {
		m.StopAll()
		return
	}

	targets, err := m.targets()
	if err != nil {
		logging.Warn("PoC stream: failed to resolve stream targets, keeping current streams", types.PoC, "error", err)
		return
	}

	desired := make(map[string]bool, len(targets))
	for _, target := range targets {
		desired[target.NodeID] = true
		m.ensureStream(target.NodeID, target.Address)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for nodeID, stream := range m.streams {
		if !desired[nodeID] {
			stream.cancel()
			delete(m.streams, nodeID)
		}
	}
}

func (m *Manager) ensureStream(nodeID string, grpcAddress string) {
	m.mu.Lock()
	if existing, ok := m.streams[nodeID]; ok {
		if existing.address == grpcAddress {
			m.mu.Unlock()
			return
		}
		existing.cancel()
		delete(m.streams, nodeID)
	}

	ctx, cancel := context.WithCancel(m.rootCtx)
	stream := &managedStream{
		nodeID:  nodeID,
		address: grpcAddress,
		cancel:  cancel,
	}
	m.streams[nodeID] = stream
	m.mu.Unlock()

	logging.Info("PoC stream: opening callback stream", types.PoC,
		"node_id", nodeID, "address", grpcAddress)
	go m.runStream(ctx, stream)
}

func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for nodeID, stream := range m.streams {
		stream.cancel()
		delete(m.streams, nodeID)
	}
}

func (m *Manager) removeStream(stream *managedStream) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.streams[stream.nodeID]; ok && current == stream {
		stream.cancel()
		delete(m.streams, stream.nodeID)
	}
}

func (m *Manager) runStream(ctx context.Context, stream *managedStream) {
	defer m.removeStream(stream)

	backoff := m.cfg.InitialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		if !m.gate.StreamActive() {
			logging.Info("PoC stream: phase over, closing stream", types.PoC,
				"node_id", stream.nodeID)
			return
		}

		received, err := m.runStreamOnce(ctx, stream)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logging.Warn("PoC stream: stream interrupted, reconnecting", types.PoC,
				"node_id", stream.nodeID, "resume_after", stream.getResumeAfter(),
				"backoff", backoff.String(), "error", err)
		}

		if received {
			backoff = m.cfg.InitialBackoff
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > m.cfg.MaxBackoff {
			backoff = m.cfg.MaxBackoff
		}
	}
}

func (m *Manager) runStreamOnce(ctx context.Context, stream *managedStream) (bool, error) {
	client, closer, err := m.dial(stream.address)
	if err != nil {
		return false, err
	}
	defer closer.Close()

	rpc, err := client.StreamCallbacks(ctx, &gen.StreamCallbacksRequest{
		ResumeAfterId: stream.getResumeAfter(),
	})
	if err != nil {
		return false, err
	}

	received := false
	for {
		callback, err := rpc.Recv()
		if err != nil {
			return received, err
		}
		received = true
		if err := m.handleCallback(ctx, client, stream, callback); err != nil {
			return received, err
		}
	}
}

func (m *Manager) handleCallback(ctx context.Context, client gen.PoCCallbackStreamClient, stream *managedStream, callback *gen.Callback) error {
	status, err := m.dispatch(callback)
	switch {
	case err != nil:
		logging.Warn("PoC stream: dropping malformed callback", types.PoC,
			"node_id", stream.nodeID, "id", callback.Id, "path", callback.Path, "error", err)
	case status < http.StatusMultipleChoices:
	case status >= http.StatusBadRequest && status < http.StatusInternalServerError:
		logging.Warn("PoC stream: callback rejected by handler", types.PoC,
			"node_id", stream.nodeID, "id", callback.Id, "path", callback.Path, "status", status)
	default:
		return fmt.Errorf("transient failure for callback %s (%s): handler returned status %d", callback.Id, callback.Path, status)
	}

	if _, err := client.AckCallbacks(ctx, &gen.AckCallbacksRequest{Ids: []string{callback.Id}}); err != nil {
		return fmt.Errorf("ack failed for callback %s: %w", callback.Id, err)
	}
	stream.setResumeAfter(callback.Id)
	return nil
}

func (m *Manager) dispatch(callback *gen.Callback) (int, error) {
	req, err := http.NewRequest(http.MethodPost, callback.Path, bytes.NewReader(callback.Body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	m.handler.ServeHTTP(rec, req)
	return rec.Code, nil
}
