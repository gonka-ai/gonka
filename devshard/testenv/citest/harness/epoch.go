package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"common/chain"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"devshard/testenv/config"
	"devshard/testenv/mockchain/adminface"
)

// MockChainSnapshot holds block height and epoch from mock-chain queries.
type MockChainSnapshot struct {
	BlockHeight       int64
	EpochIndex        uint64
	PocStart          int64
	NextPocStart      int64
	ParamsBlockHeight int64
}

// GetMockChainSnapshot queries mock-chain gRPC EpochInfo and optional admin revision.
func GetMockChainSnapshot(t *testing.T, cfg *config.File, eps Endpoints, client *http.Client) MockChainSnapshot {
	t.Helper()
	snap := queryMockChainEpochInfo(t, eps.MockChainGRPC)
	if rev, err := tryMockChainRevision(client, eps.MockChainAdmin); err == nil {
		snap.BlockHeight = rev.BlockHeight
		snap.ParamsBlockHeight = rev.ParamsBlockHeight
		snap.NextPocStart = rev.NextPocStartBlockHeight
		snap.EpochIndex = rev.EpochIndex
	}
	if snap.NextPocStart == 0 {
		length := cfg.Epoch.EpochLength
		if length == 0 {
			length = 400
		}
		snap.NextPocStart = snap.PocStart + length
		if snap.NextPocStart <= snap.BlockHeight {
			snap.NextPocStart = snap.BlockHeight + length
		}
	}
	return snap
}

func queryMockChainEpochInfo(t *testing.T, grpcAddr string) MockChainSnapshot {
	t.Helper()
	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	c := chain.NewFromConn(conn)
	resp, err := c.InferenceQueryClient().EpochInfo(context.Background(), &inferencetypes.QueryEpochInfoRequest{})
	require.NoError(t, err)
	return MockChainSnapshot{
		BlockHeight: resp.BlockHeight,
		EpochIndex:  resp.LatestEpoch.Index,
		PocStart:    resp.LatestEpoch.PocStartBlockHeight,
	}
}

func tryMockChainRevision(client *http.Client, adminURL string) (adminface.RevisionResponse, error) {
	resp, err := client.Get(adminURL + "/testenv/revision")
	if err != nil {
		return adminface.RevisionResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return adminface.RevisionResponse{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	var rev adminface.RevisionResponse
	if err := json.NewDecoder(resp.Body).Decode(&rev); err != nil {
		return adminface.RevisionResponse{}, err
	}
	return rev, nil
}

// PatchTestenvEpochAdvance posts advance=true to mock-dapi /testenv/epoch proxy.
func PatchTestenvEpochAdvance(t *testing.T, client *http.Client, mockDapiHTTP string) {
	t.Helper()
	data, err := json.Marshal(adminface.EpochRequest{Advance: true})
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, mockDapiHTTP+"/testenv/epoch", bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "POST /testenv/epoch advance")
}
