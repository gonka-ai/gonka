package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"common/nodemanager/gen"
	"devshard/testenv/config"
	"devshard/testenv/mockchain/adminface"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// MockDAPIEndpoints adds gRPC address for NodeManager clients.
type MockDAPIEndpoints struct {
	HTTP string
	GRPC string
}

// MockDAPIFromConfig returns published mock-dapi URLs for citest.
func MockDAPIFromConfig(cfg *config.File) MockDAPIEndpoints {
	return MockDAPIEndpoints{
		HTTP: fmt.Sprintf("http://127.0.0.1:%d", cfg.MockDapi.HTTPPort),
		GRPC: fmt.Sprintf("127.0.0.1:%d", cfg.MockDapi.GRPCPort),
	}
}

// DialMockDAPI opens an insecure gRPC client to mock-dapi NodeManager.
func DialMockDAPI(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// PatchTestenvParams posts a fault-injection params patch via mock-dapi (/testenv/params).
func PatchTestenvParams(t *testing.T, client *http.Client, mockDapiHTTP string, req adminface.ParamsRequest) {
	t.Helper()
	data, err := json.Marshal(req)
	require.NoError(t, err)
	httpReq, err := http.NewRequest(http.MethodPost, mockDapiHTTP+"/testenv/params", bytes.NewReader(data))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST /testenv/params")
}

// GetRuntimeConfigOnce calls NodeManager.GetRuntimeConfig without server-side wait.
func GetRuntimeConfigOnce(ctx context.Context, client gen.NodeManagerClient, clientHeight int64) (*gen.GetRuntimeConfigResponse, error) {
	return client.GetRuntimeConfig(ctx, &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: clientHeight,
		MaxWaitSeconds:          0,
	})
}

// WaitRuntimeConfigLongPoll blocks until mock-dapi returns a newer params snapshot or ctx ends.
func WaitRuntimeConfigLongPoll(ctx context.Context, client gen.NodeManagerClient, clientHeight int64, maxWait time.Duration) (*gen.GetRuntimeConfigResponse, error) {
	sec := int32(maxWait / time.Second)
	if sec < 1 {
		sec = 1
	}
	return client.GetRuntimeConfig(ctx, &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: clientHeight,
		MaxWaitSeconds:          sec,
	})
}
