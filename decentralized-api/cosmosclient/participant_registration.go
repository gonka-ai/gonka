package cosmosclient

import (
	"bytes"
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/cometbft/cometbft/crypto"
	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func ParticipantExists(recorder CosmosMessageClient) (bool, error) {
	queryClient := recorder.NewInferenceQueryClient()
	request := &types.QueryGetParticipantRequest{Index: recorder.GetAddress()}

	// TODO: check participant state, compute diff and update?
	// 	Or implement some ways to periodically (or by request) update the participant state
	response, err := queryClient.Participant(*recorder.GetContext(), request)
	if err != nil {
		if strings.Contains(err.Error(), "code = NotFound") {
			slog.Info("Participant does not exist", "address", recorder.GetAddress(), "err", err)
			return false, nil
		} else {
			return false, err
		}
	}

	_ = response

	return true, nil
}

// An alternative could be to always submit a new participant
//
//	and let the chain decide if it's a new or existing participant?
//
// Or if it's a genesis participant just submit it again if error is "block < 0"?
func waitForFirstBlock(client *rpcclient.HTTP, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for first block")
		default:
			status, err := client.Status(ctx)
			if err != nil {
				slog.Debug("Waiting for chain to start...", "error", err)
				time.Sleep(1 * time.Second)
				continue
			}
			if status.SyncInfo.LatestBlockHeight > 0 {
				return nil
			}
			time.Sleep(1 * time.Second)
		}
	}
}

func RegisterParticipantIfNeeded(recorder CosmosMessageClient, config *apiconfig.Config, nodeBroker *broker.Broker) error {
	if config.ChainNode.IsGenesis {
		return registerGenesisParticipant(recorder, config, nodeBroker)
	} else {
		return registerJoiningParticipant(recorder, config, nodeBroker)
	}
}

func registerGenesisParticipant(recorder CosmosMessageClient, config *apiconfig.Config, nodeBroker *broker.Broker) error {
	// Create RPC client
	client, err := NewRpcClient(config.ChainNode.Url)
	if err != nil {
		return fmt.Errorf("failed to create tendermint RPC client: %w", err)
	}

	// Wait for chain to start
	if err := waitForFirstBlock(client, 1*time.Minute); err != nil {
		return fmt.Errorf("chain failed to start: %w", err)
	}

	if exists, err := ParticipantExists(recorder); exists {
		slog.Info("Genesis participant already exists")
		return nil
	} else if err != nil {
		return fmt.Errorf("Failed to check if genesis participant exists: %w", err)
	}

	validatorKey, err := getValidatorKey(config)
	if err != nil {
		return err
	}
	validatorKeyString := base64.StdEncoding.EncodeToString(validatorKey.Bytes())

	uniqueModelsList, err := getUniqueModels(nodeBroker)
	if err != nil {
		return fmt.Errorf("Failed to get unique models: %w", err)
	}

	slog.Info("Registering genesis participant", "validatorKey", validatorKeyString, "Url", config.Api.PublicUrl, "Models", uniqueModelsList)

	msg := &inference.MsgSubmitNewParticipant{
		Url:          config.Api.PublicUrl,
		Models:       uniqueModelsList,
		ValidatorKey: validatorKeyString,
	}

	return recorder.SubmitNewParticipant(msg)
}

// FIXME: duplicating code, temp solution to avoid cycle import:
//
//	api > cosmosclient > api
type submitUnfundedNewParticipantDto struct {
	Address      string   `json:"address"`
	Url          string   `json:"url"`
	Models       []string `json:"models"`
	ValidatorKey string   `json:"validator_key"`
	PubKey       string   `json:"pub_key"`
}

func registerJoiningParticipant(recorder CosmosMessageClient, config *apiconfig.Config, nodeBroker *broker.Broker) error {
	// Probably move into an upper-level function
	if exists, err := ParticipantExists(recorder); exists {
		slog.Info("Participant already exists, skipping registration")
		return nil
	} else if err != nil {
		return fmt.Errorf("Failed to check if participant exists: %w", err)
	}

	validatorKey, err := getValidatorKey(config)
	if err != nil {
		return err
	}
	validatorKeyString := base64.StdEncoding.EncodeToString(validatorKey.Bytes())

	uniqueModelsList, err := getUniqueModels(nodeBroker)
	if err != nil {
		return fmt.Errorf("Failed to get unique models: %w", err)
	}

	address := recorder.GetAddress()
	pubKey, err := recorder.GetAccount().Record.GetPubKey()
	if err != nil {
		return fmt.Errorf("Failed to get public key: %w", err)
	}
	pubKeyString := base64.StdEncoding.EncodeToString(pubKey.Bytes())

	slog.Info(
		"Registering joining participant",
		"validatorKey", validatorKeyString,
		"Url", config.Api.PublicUrl,
		"Models", uniqueModelsList,
		"Address", address,
		"PubKey", pubKeyString,
	)

	requestBody := submitUnfundedNewParticipantDto{
		Address:      address,
		Url:          config.Api.PublicUrl,
		Models:       uniqueModelsList,
		ValidatorKey: validatorKeyString,
		PubKey:       pubKeyString,
	}

	requestUrl, err := url.JoinPath(config.ChainNode.SeedApiUrl, "/v1/participants")
	if err != nil {
		return fmt.Errorf("Failed to join URL path: %w", err)
	}

	// Serialize request body to JSON
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	// Create the POST request
	req, err := http.NewRequest(http.MethodPost, requestUrl, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// Handle the response
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("received non-OK response: %s", resp.Status)
	}

	return nil
}

func getUniqueModels(nodeBroker *broker.Broker) ([]string, error) {
	nodes, err := nodeBroker.GetNodes()
	if err != nil {
		return nil, fmt.Errorf("failed to get nodes from broker: %w", err)
	}

	uniqueModelsSet := make(map[string]bool)
	for _, node := range nodes {
		for _, model := range node.Node.Models {
			uniqueModelsSet[model] = true
		}
	}
	var uniqueModelsList []string
	for model := range uniqueModelsSet {
		uniqueModelsList = append(uniqueModelsList, model)
	}
	return uniqueModelsList, nil
}

func getValidatorKey(config *apiconfig.Config) (crypto.PubKey, error) {
	// Get validator key through RPC
	client, err := NewRpcClient(config.ChainNode.Url)
	if err != nil {
		return nil, fmt.Errorf("failed to create tendermint RPC client: %w", err)
	}

	// Get validator info
	result, err := client.Status(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get status from tendermint RPC client: %w", err)
	}

	validatorKey := result.ValidatorInfo.PubKey
	return validatorKey, nil
}
