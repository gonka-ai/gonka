package mlnodeclient

type ClientFactory interface {
	CreateClient(pocUrl string, inferenceUrl string) MLNodeClient
}

type HttpClientFactory struct{}

func (f *HttpClientFactory) CreateClient(pocUrl string, inferenceUrl string) MLNodeClient {
	return NewNodeClient(pocUrl, inferenceUrl)
}

type MockClientFactory struct {
	clients map[string]*MockClient
}

func NewMockClientFactory() *MockClientFactory {
	return &MockClientFactory{
		clients: make(map[string]*MockClient),
	}
}

func (f *MockClientFactory) CreateClient(pocUrl string, inferenceUrl string) MLNodeClient {
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
	return f.clients[pocUrl]
}

func (f *MockClientFactory) GetAllClients() map[string]*MockClient {
	return f.clients
}

func (f *MockClientFactory) Reset() {
	for _, client := range f.clients {
		*client = *NewMockClient() // Reset the client state
	}
}
