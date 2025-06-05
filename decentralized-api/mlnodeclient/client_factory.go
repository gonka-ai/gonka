package mlnodeclient

type ClientFactory interface {
	CreateClient(pocUrl string, inferenceUrl string) MLNodeClient
}

type HttpClientFactory struct{}

func (f *HttpClientFactory) CreateClient(pocUrl string, inferenceUrl string) MLNodeClient {
	return NewNodeClient(pocUrl, inferenceUrl)
}

type MockClientFactory struct{}

func (f *MockClientFactory) CreateClient(pocUrl string, inferenceUrl string) MLNodeClient {
	return NewMockClient()
}
