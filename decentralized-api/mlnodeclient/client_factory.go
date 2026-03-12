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
	key := pocUrl
	f.mu.Lock()
	defer f.mu.Unlock()
	if client, exists := f.clients[key]; exists {
		return client
	}
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
