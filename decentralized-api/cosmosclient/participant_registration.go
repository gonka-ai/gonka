package cosmosclient

import (
	"context"
	"decentralized-api/apiconfig"
	"encoding/base64"
	"fmt"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/rpc/client/http"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"time"
)

func ParticipantExists(recorder CosmosMessageClient) (bool, error) {
	queryClient := recorder.NewInferenceQueryClient()
	request := &types.QueryInferenceParticipantRequest{Address: recorder.GetAddress()}

	// TODO: check participant state, compute diff and update?
	// 	Or implement some ways to periodically (or by request) update the participant state
	_, err := queryClient.InferenceParticipant(*recorder.GetContext(), request)
	if err != nil {
		return false, err
	}

	return true, nil
}

// An alternative could be to always submit a new participant
//
//	and let the chain decide if it's a new or existing participant?
//
// Or if it's a genesis participant just submit it again if error is "block < 0"?
func waitForFirstBlock(client *http.HTTP, timeout time.Duration) error {
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

func RegisterGenesisParticipant(recorder CosmosMessageClient, config *apiconfig.Config) error {
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

	uniqueModelsList := getUniqueModels(config)

	slog.Info("Registering genesis participant", "validatorKey", validatorKeyString, "Url", config.Api.PublicUrl, "Models", uniqueModelsList)

	msg := &inference.MsgSubmitNewParticipant{
		Url:          config.Api.PublicUrl,
		Models:       uniqueModelsList,
		ValidatorKey: validatorKeyString,
	}

	return recorder.SubmitNewParticipant(msg)
}

func RegisterJoiningParticipant(recorder CosmosMessageClient, config *apiconfig.Config) error {
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

	uniqueModelsList := getUniqueModels(config)
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

	// TODO: do an http request to existing seed node

	return nil
}

func getUniqueModels(config *apiconfig.Config) []string {
	uniqueModelsSet := map[string]bool{}
	for _, node := range config.Nodes {
		for _, model := range node.Models {
			uniqueModelsSet[model] = true
		}
	}
	var uniqueModelsList []string
	for model := range uniqueModelsSet {
		uniqueModelsList = append(uniqueModelsList, model)
	}
	return uniqueModelsList
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
