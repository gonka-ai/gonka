package poc

import (
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/logging"
	"errors"
	"log"
	"strconv"

	"github.com/productscience/inference/x/inference/types"
)

func ProcessNewBlockEvent(nodePoCOrchestrator *NodePoCOrchestrator, event *chainevents.JSONRPCResponse, transactionRecorder cosmosclient.InferenceCosmosClient, configManager *apiconfig.ConfigManager) {
	if event.Result.Data.Type != "tendermint/event/NewBlock" {
		log.Fatalf("Expected tendermint/event/NewBlock event, got %s", event.Result.Data.Type)
		return
	}

	params, err := transactionRecorder.NewInferenceQueryClient().Params(transactionRecorder.Context, &types.QueryParamsRequest{})
	if err == nil {
		nodePoCOrchestrator.SetParams(&params.Params)
	}

	//for key := range event.Result.Events {
	//	for i, attr := range event.Result.Events[key] {
	//		logging.Debug("\t NewBlockEventValue", "key", key, "attr", attr, "index", i)
	//	}
	//}

	data := event.Result.Data.Value
	blockHeight, err := getBlockHeight(data)
	if err != nil {
		logging.Error("Failed to get blockHeight from event data", types.Stages, "error", err)
		return
	}

	err = configManager.SetHeight(blockHeight)
	if err != nil {
		logging.Warn("Failed to write config", types.Config, "error", err)
	}

	blockHash, err := getBlockHash(data)
	if err != nil {
		logging.Error("Failed to get blockHash from event data", types.Stages, "error", err)
		return
	}

	epochParams := nodePoCOrchestrator.GetParams().EpochParams
	logging.Debug("New block event received", types.Stages, "blockHeight", blockHeight, "blockHash", blockHash)

	if epochParams.IsStartOfPoCStage(blockHeight) {
		logging.Info("IsStartOfPocStage: sending StartPoCEvent to the PoC orchestrator", types.Stages)

		nodePoCOrchestrator.StartPoC(blockHeight, blockHash)
		generateSeed(blockHeight, &transactionRecorder, configManager)
		return
	}

	if epochParams.IsEndOfPoCStage(blockHeight) {
		logging.Info("IsEndOfPoCStage. Calling MoveToValidationStage", types.Stages)

		nodePoCOrchestrator.MoveToValidationStage(blockHeight)
	}

	if epochParams.IsStartOfPoCValidationStage(blockHeight) {
		logging.Info("IsStartOfPoCValidationStage", types.Stages)

		go func() {
			nodePoCOrchestrator.ValidateReceivedBatches(blockHeight)
		}()
	}

	if epochParams.IsEndOfPoCValidationStage(blockHeight) {
		logging.Info("IsEndOfPoCValidationStage", types.Stages)

		nodePoCOrchestrator.StopPoC()

		return
	}

	if epochParams.IsSetNewValidatorsStage(blockHeight) {
		logging.Info("IsSetNewValidatorsStage", types.Stages)
		go func() {
			changeCurrentSeed(configManager)
		}()
	}

	if epochParams.IsClaimMoneyStage(blockHeight) {
		logging.Info("IsClaimMoneyStage", types.Stages)
		go func() {
			requestMoney(&transactionRecorder, configManager)
		}()
	}
}

func getBlockHeight(data map[string]interface{}) (int64, error) {
	block, ok := data["block"].(map[string]interface{})
	if !ok {
		return 0, errors.New("failed to access 'block' key")
	}

	header, ok := block["header"].(map[string]interface{})
	if !ok {
		return 0, errors.New("failed to access 'header' key")
	}

	heightString, ok := header["height"].(string)
	if !ok {
		return 0, errors.New("failed to access 'height' key or it's not a string")
	}

	height, err := strconv.ParseInt(heightString, 10, 64)
	if err != nil {
		return 0, errors.New("Failed to convert retrieved height value to int64")
	}

	return height, nil
}

func getBlockHash(data map[string]interface{}) (string, error) {
	blockID, ok := data["block_id"].(map[string]interface{})
	if !ok {
		return "", errors.New("failed to access 'block_id' key")
	}

	hash, ok := blockID["hash"].(string)
	if !ok {
		return "", errors.New("failed to access 'hash' key or it's not a string")
	}

	return hash, nil
}
