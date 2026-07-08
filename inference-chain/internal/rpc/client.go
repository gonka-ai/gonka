package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultTrustedBlocksPeriod = 1000
)

// rpcHTTPClient bounds the transport-level phases (connect + wait-for-response
// headers) of every RPC fetch; net/http's default client sets no timeout at all.
// The per-request context deadlines below additionally bound the body read, so a
// node that sends headers and then stalls mid-body cannot hang the caller either.
var rpcHTTPClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// Per-request total deadlines (cover the whole request including the body read).
// status/block responses are tiny, so a short cap is safe; genesis can be large,
// so it gets a generous cap that still bounds a stalled transfer. Vars so tests
// can shrink them.
var (
	statusRequestTimeout  = 30 * time.Second
	genesisRequestTimeout = 5 * time.Minute
)

// httpGetWithTimeout issues a GET whose context deadline bounds the entire
// request, including reading the body. The caller must close resp.Body and then
// call cancel (defer cancel() after the body is fully read/decoded).
func httpGetWithTimeout(url string, timeout time.Duration) (*http.Response, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	resp, err := rpcHTTPClient.Do(req)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

func getStatus(rpcNode string) (*StatusResponse, error) {
	url := fmt.Sprintf("%s/status", rpcNode)
	resp, cancel, err := httpGetWithTimeout(url, statusRequestTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received non-OK HTTP status: %s", resp.Status)
	}

	var status StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	return &status, nil
}

func GetTrustedBlock(trustedNode string, trustedBlockPeriod uint64) (uint64, string, error) {
	status, err := getStatus(trustedNode)
	if err != nil {
		return 0, "", fmt.Errorf("failed get status: %w", err)
	}

	var (
		trustHeight uint64
		trustHash   string
	)

	latestHeight, err := strconv.ParseUint(status.Result.SyncInfo.LatestBlockHeight, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("error parsing latest block height: %v", err)
	}

	if trustedBlockPeriod == 0 {
		trustedBlockPeriod = defaultTrustedBlocksPeriod
	}

	if latestHeight <= trustedBlockPeriod {
		trustHeight, err = strconv.ParseUint(status.Result.SyncInfo.EarliestBlockHeight, 10, 64)
		if err != nil {
			return 0, "", fmt.Errorf("error parsing latest block height: %v", err)
		}
		trustHash = status.Result.SyncInfo.EarliestBlockHash
	} else {
		trustHeight = latestHeight - trustedBlockPeriod
		trustHash, err = GetBlockHash(trustedNode, trustHeight)
		if err != nil {
			return 0, "", err
		}
	}
	return trustHeight, trustHash, nil
}

func GetBlockHash(rpcNode string, height uint64) (string, error) {
	if height == 0 {
		return "", errors.New("height must be greater than zero")
	}

	url := fmt.Sprintf("%s/block?height=%d", rpcNode, height)
	resp, cancel, err := httpGetWithTimeout(url, statusRequestTimeout)
	if err != nil {
		return "", err
	}
	defer cancel()
	defer resp.Body.Close()

	var block BlockResponse
	if err := json.NewDecoder(resp.Body).Decode(&block); err != nil {
		return "", err
	}

	if block.Result.Block.Header.Height == "" {
		return "", errors.New("failed to get block hash")
	}
	return block.Result.BlockId.Hash, nil
}

func GetNodeId(nodeRpcUrl string) (string, error) {
	status, err := getStatus(nodeRpcUrl)
	if err != nil {
		return "", fmt.Errorf("failed get node id: %w", err)
	}
	return status.Result.NodeInfo.ID, nil
}

func DownloadGenesis(nodeAddress string) (json.RawMessage, error) {
	url := fmt.Sprintf("%s/genesis", nodeAddress)

	resp, cancel, err := httpGetWithTimeout(url, genesisRequestTimeout)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received non-OK HTTP status: %s", resp.Status)
	}

	var genResp GenesisResponse
	if err := json.NewDecoder(resp.Body).Decode(&genResp); err != nil {
		return nil, fmt.Errorf("failed to decode genesis JSON: %w", err)
	}
	return genResp.Result.Genesis, nil
}
