package mlnodeclient

import "sync"

type ClientFactory interface {
	CreateClient(pocUrl string, inferenceUrl string) MLNodeClient
}

type HttpClientFactory struct{}

func (f *HttpClientFactory) CreateClient(pocUrl string, inferenceUrl string) MLNodeClient {
	return NewNodeClient(pocUrl, inferenceUrl)
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

func (f *MockClientFactory) CreateClient(pocUrl string, inferenceUrl string) MLNodeClient {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Use pocUrl as the key to identify nodes (it should be unique per node)
	key := pocUrl
	if client, exists := f.clients[key]; exists {
		return client
	}

	// Create new mock client for this node
	client := NewMockClient()
	f.clients[key] = client
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

	// Return a copy of the map to avoid concurrent access to the map itself
	clientsCopy := make(map[string]*MockClient)
	for k, v := range f.clients {
		clientsCopy[k] = v
	}
	return clientsCopy
}

func (f *MockClientFactory) Reset() {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, client := range f.clients {
		client.Reset()
	}
}
