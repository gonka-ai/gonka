package mockdapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"common/nodemanager/gen"
	cosrv "devshard/chainoracle/server"
	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockdapi"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type testBed struct {
	adminURL string
	grpcAddr string
	httpURL  string
	cleanup  func()
}

func startBed(t *testing.T) testBed {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st := seed.Defaults()

	grpcSrv, grpcLis, err := grpcface.NewInProcessServer(grpcface.Deps{Store: st})
	require.NoError(t, err)

	admin := adminface.NewServer(st, nil, nil)
	adminHTTP := httptest.NewServer(admin.Handler())

	cfg := mockdapi.DefaultConfig()
	cfg.ChainGRPCAddr = grpcLis.Addr().String()
	cfg.ChainTestenvURL = adminHTTP.URL
	// Disable background poll; tests drive RefreshRuntimeConfig explicitly via /testenv/*.
	cfg.ChainPollInterval = time.Hour
	cfg.BlockInterval = 50 * time.Millisecond

	svc, err := mockdapi.New(ctx, cfg)
	require.NoError(t, err)

	grpcL, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpL, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() { _ = svc.RunOn(ctx, grpcL, httpL) }()

	require.Eventually(t, func() bool {
		conn, err := grpc.NewClient(grpcL.Addr().String(),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return false
		}
		_ = conn.Close()
		resp, err := http.Get("http://" + httpL.Addr().String() + "/healthz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 20*time.Millisecond)

	return testBed{
		adminURL: adminHTTP.URL,
		grpcAddr: grpcL.Addr().String(),
		httpURL:  "http://" + httpL.Addr().String(),
		cleanup: func() {
			cancel()
			adminHTTP.Close()
			grpcSrv.Stop()
			_ = grpcLis.Close()
		},
	}
}

func TestMockDAPI_GetRuntimeConfigLongPollViaTestenvParams(t *testing.T) {
	bed := startBed(t)
	t.Cleanup(bed.cleanup)

	ctx := context.Background()
	conn, err := grpc.NewClient(bed.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	client := gen.NewNodeManagerClient(conn)

	resp, err := client.GetRuntimeConfig(ctx, &gen.GetRuntimeConfigRequest{ClientParamsBlockHeight: 0})
	require.NoError(t, err)
	require.False(t, resp.Unchanged)
	startHeight := resp.Config.ParamsBlockHeight

	resp, err = client.GetRuntimeConfig(ctx, &gen.GetRuntimeConfigRequest{ClientParamsBlockHeight: startHeight})
	require.NoError(t, err)
	require.True(t, resp.Unchanged)

	done := make(chan *gen.GetRuntimeConfigResponse, 1)
	go func() {
		r, err := client.GetRuntimeConfig(ctx, &gen.GetRuntimeConfigRequest{
			ClientParamsBlockHeight: startHeight,
			MaxWaitSeconds:          5,
		})
		require.NoError(t, err)
		done <- r
	}()

	time.Sleep(50 * time.Millisecond)
	maxNonce := uint32(999)
	body, _ := json.Marshal(adminface.ParamsRequest{MaxNonce: &maxNonce})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bed.httpURL+"/testenv/params", strings.NewReader(string(body)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, postResp.StatusCode)
	_ = postResp.Body.Close()

	select {
	case r := <-done:
		require.False(t, r.Unchanged)
		require.Greater(t, r.Config.ParamsBlockHeight, startHeight)
		require.Equal(t, uint32(999), r.Config.MaxNonce)
	case <-time.After(3 * time.Second):
		t.Fatal("long-poll did not wake after /testenv/params")
	}
}

func TestMockDAPI_GatewayPhaseEpochLatest(t *testing.T) {
	bed := startBed(t)
	t.Cleanup(bed.cleanup)

	resp, err := http.Get(bed.httpURL + "/v1/epochs/latest")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "Inference", body["phase"])
}

func TestMockDAPI_VersionsJSON(t *testing.T) {
	bed := startBed(t)
	t.Cleanup(bed.cleanup)

	resp, err := http.Get(bed.httpURL + "/versions")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var cfg cosrv.VersionConfig
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&cfg))
	require.Len(t, cfg.Versions, 1)
	require.Equal(t, "v2", cfg.Versions[0].Name)
	require.NotEmpty(t, cfg.Versions[0].Binary)
}

func TestMockDAPI_BlockStreamMonotonic(t *testing.T) {
	bed := startBed(t)
	t.Cleanup(bed.cleanup)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, bed.httpURL+"/block/stream?from=1", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	reader := bufio.NewReader(resp.Body)
	var lastHeight int64
	for i := 0; i < 2; i++ {
		line, err := readSSEDataLine(reader)
		require.NoError(t, err)
		var hdr struct {
			Height int64 `json:"height"`
		}
		require.NoError(t, json.Unmarshal([]byte(line), &hdr))
		if i > 0 {
			require.Greater(t, hdr.Height, lastHeight)
		}
		lastHeight = hdr.Height
	}
}

func TestMockDAPI_AcquireMLNode(t *testing.T) {
	bed := startBed(t)
	t.Cleanup(bed.cleanup)

	conn, err := grpc.NewClient(bed.grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	resp, err := gen.NewNodeManagerClient(conn).AcquireMLNode(context.Background(), &gen.AcquireMLNodeRequest{Model: "test-model"})
	require.NoError(t, err)
	require.Equal(t, "http://mock-openai:8088", resp.Endpoint)
}

func readSSEDataLine(r *bufio.Reader) (string, error) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: "), nil
		}
	}
}
