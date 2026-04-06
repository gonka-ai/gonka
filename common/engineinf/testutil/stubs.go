// Package testutil provides shared test doubles for the inference package.
// Import only from _test.go files.
package testutil

import (
	"context"
	"fmt"
	"sync"

	inference "common/engineinf"
	"common/mlnode/gen"
)

// StubNodeLock is a test double for mlnode.NodeLock.
type StubNodeLock struct {
	endpoint string
	lockID   string

	mu              sync.Mutex
	released        bool
	releasedOutcome gen.ReleaseOutcome
}

func NewStubNodeLock(endpoint, lockID string) *StubNodeLock {
	return &StubNodeLock{endpoint: endpoint, lockID: lockID}
}

func (s *StubNodeLock) Acquire(_ context.Context, _ string, _ []string) (*gen.AcquireMLNodeResponse, error) {
	return &gen.AcquireMLNodeResponse{Endpoint: s.endpoint, LockId: s.lockID}, nil
}

func (s *StubNodeLock) Release(_ context.Context, _ string, outcome gen.ReleaseOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = true
	s.releasedOutcome = outcome
	return nil
}

func (s *StubNodeLock) Released() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.released
}

func (s *StubNodeLock) ReleasedOutcome() gen.ReleaseOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releasedOutcome
}

// StubChainQuerier is a test double for the chainQuerier dependency of inference.Engine.
type StubChainQuerier struct {
	mu         sync.RWMutex
	inferences map[string]*inference.Inference
	payloads   map[string]map[uint64]*inference.PayloadResponse
}

func NewStubChainQuerier() *StubChainQuerier {
	return &StubChainQuerier{
		inferences: make(map[string]*inference.Inference),
		payloads:   make(map[string]map[uint64]*inference.PayloadResponse),
	}
}

func (s *StubChainQuerier) AddInference(id string, inf *inference.Inference) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inferences[id] = inf
}

func (s *StubChainQuerier) AddPayload(inferenceID string, epochID uint64, p *inference.PayloadResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.payloads[inferenceID] == nil {
		s.payloads[inferenceID] = make(map[uint64]*inference.PayloadResponse)
	}
	s.payloads[inferenceID][epochID] = p
}

func (s *StubChainQuerier) GetInference(_ context.Context, id string) (*inference.Inference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inf, ok := s.inferences[id]
	if !ok {
		return nil, fmt.Errorf("%w: inference %s", inference.ErrNotFound, id)
	}
	return inf, nil
}

func (s *StubChainQuerier) GetInferencePayload(_ context.Context, inferenceID string, epochID uint64) (*inference.PayloadResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	epochs, ok := s.payloads[inferenceID]
	if !ok {
		return nil, fmt.Errorf("%w: payload for inference %s", inference.ErrNotFound, inferenceID)
	}
	if p, ok := epochs[epochID]; ok {
		return p, nil
	}
	if epochID > 0 {
		if p, ok := epochs[epochID-1]; ok {
			return p, nil
		}
	}
	if p, ok := epochs[epochID+1]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("%w: payload for inference %s epoch %d", inference.ErrNotFound, inferenceID, epochID)
}
