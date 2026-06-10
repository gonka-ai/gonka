package mlnodeclient

import (
	"crypto/tls"
	"net/http"
	"sync"
	"time"
)

type ClientFactory interface {
	CreateClient(pocUrl string, inferenceUrl string) MLNodeClient
	NewHTTPClient(timeout time.Duration) *http.Client
}

type HttpClientFactory struct {
	TLSConfig *tls.Config
}

func (f *HttpClientFactory) CreateClient(pocUrl string, inferenceUrl string) MLNodeClient {
	return NewNodeClientWithTLS(pocUrl, inferenceUrl, f.TLSConfig)
}

func (f *HttpClientFactory) NewHTTPClient(timeout time.Duration) *http.Client {
	client := &http.Client{Timeout: timeout}
	if f.TLSConfig != nil {
		client.Transport = &http.Transport{TLSClientConfig: f.TLSConfig}
	}
	return client
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

func (f *MockClientFactory) NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
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
