package mlnodeclient

import (
	"decentralized-api/mlnode"
	"sync"
)

type ClientFactory interface {
	// CreateClientForNode builds a client for the MLNode described by ep at the
	// given node version. This is the single seam for MLNode client creation.
	CreateClientForNode(ep mlnode.Endpoint, version string) MLNodeClient
}

type HttpClientFactory struct{}

func (f *HttpClientFactory) CreateClientForNode(ep mlnode.Endpoint, version string) MLNodeClient {
	c := NewNodeClient(ep.PoCURL(version), ep.InferenceURL(version))
	c.healthUrl = ep.HealthURL(version)
	c.authToken = ep.AuthToken()
	return c
}

type MockClientFactory struct {
	mu      sync.RWMutex
	clients map[string]*MockClient
}

func NewMockClientFactory() *MockClientFactory {
	return &MockClientFactory{
		clients: make(map[string]*MockClient),
	}
}

func (f *MockClientFactory) CreateClientForNode(ep mlnode.Endpoint, version string) MLNodeClient {
	return f.MockClientFor(ep.PoCURL(version))
}

// MockClientFor returns the mock client registered for pocUrl, creating one on
// first use. Test-only helper for seeding and inspecting mock clients keyed by
// the node's PoC URL.
func (f *MockClientFactory) MockClientFor(pocUrl string) *MockClient {
	f.mu.Lock()
	defer f.mu.Unlock()
	if client, exists := f.clients[pocUrl]; exists {
		return client
	}
	client := NewMockClient()
	f.clients[pocUrl] = client
	return client
}

func (f *MockClientFactory) GetClientForNode(pocUrl string) *MockClient {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.clients[pocUrl]
}

func (f *MockClientFactory) GetAllClients() map[string]*MockClient {
	f.mu.RLock()
	defer f.mu.RUnlock()
	result := make(map[string]*MockClient, len(f.clients))
	for k, v := range f.clients {
		result[k] = v
	}
	return result
}

func (f *MockClientFactory) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, client := range f.clients {
		client.Reset()
	}
}
