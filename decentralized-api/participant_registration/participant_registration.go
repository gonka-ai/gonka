package participant_registration

import (
	"bytes"
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/server/public"
	"decentralized-api/logging"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/cometbft/cometbft/crypto"
	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func participantExistsWithWait(recorder cosmosclient.CosmosMessageClient, chainNodeUrl string) (bool, error) {
	client, err := cosmosclient.NewRpcClient(chainNodeUrl)
	if err != nil {
		return false, fmt.Errorf("failed to create tendermint RPC client: %w", err)
	}
	if err := waitForFirstBlock(client, 1*time.Minute); err != nil {
		return false, fmt.Errorf("chain failed to start: %w", err)
	}

	return participantExists(recorder)
}

func participantExists(recorder cosmosclient.CosmosMessageClient) (bool, error) {
	queryClient := recorder.NewInferenceQueryClient()
	request := &types.QueryGetParticipantRequest{Index: recorder.GetAddress()}

	// TODO: check participant state, compute diff and update?
	// 	Or implement some ways to periodically (or by request) update the participant state
	response, err := queryClient.Participant(*recorder.GetContext(), request)
	if err != nil {
		if strings.Contains(err.Error(), "code = NotFound") {
			logging.Info("Participant does not exist", types.Participants, "address", recorder.GetAddress(), "err", err)
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
				logging.Debug("Waiting for chain to start...", types.System, "error", err)
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

func RegisterParticipantIfNeeded(recorder cosmosclient.CosmosMessageClient, config *apiconfig.ConfigManager, nodeBroker *broker.Broker) error {
	if config.GetChainNodeConfig().IsGenesis {
		return registerGenesisParticipant(recorder, config, nodeBroker)
	} else {
		return registerJoiningParticipant(recorder, config, nodeBroker)
	}
}

func registerGenesisParticipant(recorder cosmosclient.CosmosMessageClient, configManager *apiconfig.ConfigManager, nodeBroker *broker.Broker) error {
	if exists, err := participantExistsWithWait(recorder, configManager.GetChainNodeConfig().Url); exists {
		logging.Info("Genesis participant already exists", types.Participants)
		return nil
	} else if err != nil {
		return fmt.Errorf("Failed to check if genesis participant exists: %w", err)
	}

	validatorKey, err := getValidatorKey(configManager.GetChainNodeConfig().Url)
	if err != nil {
		return err
	}
	validatorKeyString := base64.StdEncoding.EncodeToString(validatorKey.Bytes())
	workerPublicKey, err := configManager.CreateWorkerKey()
	if err != nil {
		return fmt.Errorf("failed to create worker key: %w", err)
	}
	uniqueModelsList, err := getUniqueModels(nodeBroker)
	if err != nil {
		return fmt.Errorf("failed to get unique models: %w", err)
	}

	publicUrl := configManager.GetApiConfig().PublicUrl
	logging.Info("Registering genesis participant", types.Participants, "validatorKey", validatorKeyString, "Url", publicUrl, "Models", uniqueModelsList)

	msg := &inference.MsgSubmitNewParticipant{
		Url:          publicUrl,
		Models:       uniqueModelsList,
		ValidatorKey: validatorKeyString,
		WorkerKey:    workerPublicKey,
	}

	return recorder.SubmitNewParticipant(msg)
}

func registerJoiningParticipant(recorder cosmosclient.CosmosMessageClient, configManager *apiconfig.ConfigManager, nodeBroker *broker.Broker) error {
	if exists, err := participantExistsWithWait(recorder, configManager.GetChainNodeConfig().Url); exists {
		logging.Info("Participant already exists, skipping registration", types.Participants)
		return nil
	} else if err != nil {
		return fmt.Errorf("Failed to check if participant exists: %w", err)
	}

	validatorKey, err := getValidatorKey(configManager.GetChainNodeConfig().Url)
	if err != nil {
		return err
	}
	validatorKeyString := base64.StdEncoding.EncodeToString(validatorKey.Bytes())

	workerKey, err := configManager.CreateWorkerKey()
	if err != nil {
		return fmt.Errorf("Failed to create worker key: %w", err)
	}

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

	logging.Info(
		"Registering joining participant",
		types.Participants, "validatorKey", validatorKeyString,
		"Url", configManager.GetApiConfig().PublicUrl,
		"Models", uniqueModelsList,
		"Address", address,
		"PubKey", pubKeyString,
	)

	requestBody := public.SubmitUnfundedNewParticipantDto{
		Address:      address,
		Url:          configManager.GetApiConfig().PublicUrl,
		Models:       uniqueModelsList,
		ValidatorKey: validatorKeyString,
		PubKey:       pubKeyString,
		WorkerKey:    workerKey,
	}

	requestUrl, err := url.JoinPath(configManager.GetChainNodeConfig().SeedApiUrl, "/v1/participants")
	if err != nil {
		return fmt.Errorf("failed to join URL path: %w", err)
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, requestUrl, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	logging.Info("Sending request to seed node", types.Participants, "url", requestUrl)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

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
		for model, _ := range node.Node.Models {
			uniqueModelsSet[model] = true
		}
	}
	var uniqueModelsList []string
	for model := range uniqueModelsSet {
		uniqueModelsList = append(uniqueModelsList, model)
	}
	return uniqueModelsList, nil
}

func getValidatorKey(chainNodeUrl string) (crypto.PubKey, error) {
	// Get validator key through RPC
	client, err := cosmosclient.NewRpcClient(chainNodeUrl)
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
