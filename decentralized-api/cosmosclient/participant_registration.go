package cosmosclient

import (
	"context"
	"decentralized-api/apiconfig"
	"encoding/base64"
	"fmt"
	"github.com/productscience/inference/api/inference/inference"
	"log/slog"
)

func RegisterGenesisParticipant(recorder CosmosMessageClient, config *apiconfig.Config) error {
	// Get validator key through RPC
	client, err := NewRpcClient(config.ChainNode.Url)
	if err != nil {
		return fmt.Errorf("failed to create tendermint RPC client: %w", err)
	}

	// Get validator info
	result, err := client.Status(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get status from tendermint RPC client: %w", err)
	}

	validatorKey := result.ValidatorInfo.PubKey
	validatorKeyString := base64.StdEncoding.EncodeToString(validatorKey.Bytes())

	// Get unique model list
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

	slog.Info("Registering genesis participant", "validatorKey", validatorKeyString, "Url", config.Api.PublicUrl, "Models", uniqueModelsList)

	msg := &inference.MsgSubmitNewParticipant{
		Url:          config.Api.PublicUrl,
		Models:       uniqueModelsList,
		ValidatorKey: validatorKeyString,
	}

	return recorder.SubmitNewParticipant(msg)
}
