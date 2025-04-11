package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

const (
	defaultTrustedBlocksPeriod = 1000
)

func getStatus(rpcNode string) (*StatusResponse, error) {
	url := fmt.Sprintf("%s/status", rpcNode)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
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
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
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

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
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
