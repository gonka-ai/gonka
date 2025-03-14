package event_listener

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainevents"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/poc"
	"decentralized-api/internal/server"
	"decentralized-api/logging"
	"decentralized-api/training"
	"decentralized-api/upgrade"
	"encoding/json"
	"fmt"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/gorilla/websocket"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TODO idea for refactoring: create EventListener struct, which will contain this global vars and all params passed to StartEventListener as fields fields
var (
	syncStatusMu sync.RWMutex
	nodeCaughtUp bool
)

func isNodeSynced() bool {
	syncStatusMu.RLock()
	defer syncStatusMu.RUnlock()
	return nodeCaughtUp
}

func updateNodeSyncStatus(status bool) {
	syncStatusMu.Lock()
	defer syncStatusMu.Unlock()
	nodeCaughtUp = status
}

func startSyncStatusChecker(chainNodeUrl string) {
	tendermintClint := cosmosclient.TendermintClient{
		ChainNodeUrl: chainNodeUrl,
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		status, err := tendermintClint.Status()
		if err != nil {
			logging.Error("Error getting node status", types.EventProcessing, "error", err)
			continue
		}
		// The node is "synced" if it's NOT catching up.
		updateNodeSyncStatus(!status.SyncInfo.CatchingUp)
		logging.Debug("Updated sync status", types.EventProcessing, "caughtUp", !status.SyncInfo.CatchingUp, "height", status.SyncInfo.LatestBlockHeight)
	}
}

const (
	finishInferenceAction      = "/inference.inference.MsgFinishInference"
	validationAction           = "/inference.inference.MsgValidation"
	trainingTaskAssignedAction = "/inference.inference.MsgAssignTrainingTask"
	submitGovProposalAction    = "/cosmos.gov.v1.MsgSubmitProposal"

	newBlockEventType = "tendermint/event/NewBlock"
	txEventType       = "tendermint/event/Tx"
)

func StartEventListener(
	nodeBroker *broker.Broker,
	transactionRecorder cosmosclient.InferenceCosmosClient,
	configManager *apiconfig.ConfigManager,
	params *types.Params,
	trainingExecutor *training.Executor,
) {
	websocketUrl := getWebsocketUrl(configManager.GetConfig())
	logging.Info("Connecting to websocket at", types.EventProcessing, "url", websocketUrl)
	ws, _, err := websocket.DefaultDialer.Dial(websocketUrl, nil)
	if err != nil {
		logging.Error("Failed to connect to websocket", types.EventProcessing, "error", err)
		log.Fatal("dial:", err)
	}
	defer ws.Close()

	// Subscribe to custom events
	subscribeToEvents(ws, "tm.event='Tx' AND message.action='"+finishInferenceAction+"'")
	subscribeToEvents(ws, "tm.event='NewBlock'")
	subscribeToEvents(ws, "tm.event='Tx' AND inference_validation.needs_revalidation='true'")
	subscribeToEvents(ws, "tm.event='Tx' AND message.action='"+submitGovProposalAction+"'")
	subscribeToEvents(ws, "tm.event='Tx' AND message.action='"+trainingTaskAssignedAction+"'")

	go startSyncStatusChecker(configManager.GetConfig().ChainNode.Url)

	pubKey, err := transactionRecorder.Account.Record.GetPubKey()
	if err != nil {
		logging.Error("Failed to get public key", types.EventProcessing, "error", err)
		return
	}
	pubKeyString := utils.PubKeyToHexString(pubKey)

	logging.Debug("Initializing PoC orchestrator",
		types.PoC, "name", transactionRecorder.Account.Name,
		"address", transactionRecorder.Address,
		"pubkey", pubKeyString)

	pocOrchestrator := poc.NewPoCOrchestrator(pubKeyString, int(params.PocParams.DefaultDifficulty))
	// PRTODO: decide if host is just host or host+port????? or url. Think what better name and stuff
	nodePocOrchestrator := poc.NewNodePoCOrchestrator(
		pubKeyString,
		nodeBroker,
		configManager.GetConfig().Api.PoCCallbackUrl,
		configManager.GetConfig().ChainNode.Url,
		&transactionRecorder,
		params,
	)
	logging.Info("PoC orchestrator initialized", types.PoC, "nodePocOrchestrator", nodePocOrchestrator)
	go pocOrchestrator.Run()

	eventChan := make(chan *chainevents.JSONRPCResponse, 100)
	numWorkers := 10
	for i := 0; i < numWorkers; i++ {
		go func() {
			for event := range eventChan {
				if event == nil {
					logging.Error("Go worker received nil chain event", types.System)
					continue
				}
				processEvent(event, nodeBroker, transactionRecorder, configManager, nodePocOrchestrator, trainingExecutor)
			}
		}()
	}
	// TODO: We should probably extract out the channels and handlers into a class
	blockEventChan := make(chan *chainevents.JSONRPCResponse, 100)
	go func() {
		for event := range blockEventChan {
			if event == nil {
				logging.Error("Go worker received nil chain event", types.System)
				continue
			}

			processEvent(event, nodeBroker, transactionRecorder, configManager, nodePocOrchestrator, trainingExecutor)
		}
	}()

	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			logging.Warn("Failed to read a websocket message", types.EventProcessing, "errorType", fmt.Sprintf("%T", err), "error", err)

			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logging.Warn("Websocket connection closed", types.EventProcessing, "errorType", fmt.Sprintf("%T", err), "error", err)
				if upgrade.CheckForUpgrade(configManager) {
					logging.Error("Upgrade required! Exiting...", types.Upgrades)
					panic("Upgrade required")
				}
				continue
			}
			continue
		}

		var event chainevents.JSONRPCResponse
		if err = json.Unmarshal(message, &event); err != nil {
			logging.Error("Error unmarshalling message to JSONRPCResponse", types.EventProcessing, "error", err, "message", message)
			continue // no sense to check event, if it wasn't unmarshalled correctly
		}

		if event.Result.Data.Type == newBlockEventType {
			blockEventChan <- &event
			continue
		}
		logging.Info("Adding event to queue", types.EventProcessing, "type", event.Result.Data.Type)
		// Push the event into the channel for processing.
		eventChan <- &event
	}
}

// processEvent is the worker function that processes a JSONRPCResponse event.
func processEvent(
	event *chainevents.JSONRPCResponse,
	nodeBroker *broker.Broker,
	transactionRecorder cosmosclient.InferenceCosmosClient,
	configManager *apiconfig.ConfigManager,
	nodePocOrchestrator *poc.NodePoCOrchestrator,
	trainingExecutor *training.Executor,
) {
	switch event.Result.Data.Type {
	case newBlockEventType:
		logging.Debug("New block event received", types.EventProcessing, "type", event.Result.Data.Type)
		if isNodeSynced() {
			poc.ProcessNewBlockEvent(nodePocOrchestrator, event, transactionRecorder, configManager)
		}
		upgrade.ProcessNewBlockEvent(event, transactionRecorder, configManager)
	case txEventType:
		handleMessage(nodeBroker, transactionRecorder, event, configManager.GetConfig(), trainingExecutor)
	default:
		logging.Warn("Unexpected event type received", types.EventProcessing, "type", event.Result.Data.Type)
	}
}

func handleMessage(
	nodeBroker *broker.Broker,
	transactionRecorder cosmosclient.InferenceCosmosClient,
	event *chainevents.JSONRPCResponse,
	currentConfig *apiconfig.Config,
	executor *training.Executor,
) {
	if waitForEventHeight(event, currentConfig) {
		return
	}

	actions, ok := event.Result.Events["message.action"]
	if !ok || len(actions) == 0 {
		// Handle the missing key or empty slice.
		// For example, log an error, return from the function, etc.
		logging.Info("No message.action event found", types.EventProcessing, "event", event)
		return // or handle it accordingly
	}

	action := actions[0]
	logging.Debug("New Tx event received", types.EventProcessing, "type", event.Result.Data.Type, "action", action)
	// Get the keys of the map event.Result.Events:
	//for key := range event.Result.Events {
	//	for i, attr := range event.Result.Events[key] {
	//		logging.Debug("\tEventValue", "key", key, "attr", attr, "index", i)
	//	}
	//}
	switch action {
	case finishInferenceAction:
		if isNodeSynced() {
			server.SampleInferenceToValidate(event.Result.Events["inference_finished.inference_id"], transactionRecorder, nodeBroker, currentConfig)
		}
	case validationAction:
		if isNodeSynced() {
			server.VerifyInvalidation(event.Result.Events, transactionRecorder, nodeBroker)
		}
	case submitGovProposalAction:
		proposalIdOrNil := event.Result.Events["proposal_id"]
		logging.Debug("New proposal submitted", types.EventProcessing, "proposalId", proposalIdOrNil)
	case trainingTaskAssignedAction:
		if isNodeSynced() {
			logging.Debug("MsgAssignTrainingTask event", types.EventProcessing, "event", event)
			taskIds := event.Result.Events["training_task_assigned.task_id"]
			if taskIds == nil {
				logging.Error("No task ID found in training task assigned event", types.Training, "event", event)
				return
			}
			for _, taskId := range taskIds {
				taskIdUint, err := strconv.ParseUint(taskId, 10, 64)
				if err != nil {
					logging.Error("Failed to parse task ID", types.Training, "error", err)
					return
				}
				executor.ProcessTaskAssignedEvent(taskIdUint)
			}
		}
	default:
		logging.Debug("Unhandled action received", types.EventProcessing, "action", action)
	}
}

// currentConfig must be a pointer, or it won't update
func waitForEventHeight(event *chainevents.JSONRPCResponse, currentConfig *apiconfig.Config) bool {
	heightString := event.Result.Events["tx.height"][0]
	expectedHeight, err := strconv.ParseInt(heightString, 10, 64)
	if err != nil {
		logging.Error("Failed to parse height", types.EventProcessing, "error", err)
		return true
	}
	for currentConfig.CurrentHeight < expectedHeight {
		logging.Info("Height race condition! Waiting for height to catch up", types.EventProcessing, "currentHeight", currentConfig.CurrentHeight, "expectedHeight", expectedHeight)
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func subscribeToEvents(ws *websocket.Conn, query string) {
	subscribeMsg := fmt.Sprintf(`{"jsonrpc": "2.0", "method": "subscribe", "id": "1", "params": ["%s"]}`, query)
	if err := ws.WriteMessage(websocket.TextMessage, []byte(subscribeMsg)); err != nil {
		logging.Error("Failed to subscribe to a websocket", types.EventProcessing, "error", err)
		log.Fatalf("Failed to subscribe to a websocket. %v", err)
	}
}

func getWebsocketUrl(config *apiconfig.Config) string {
	// Parse the input URL
	u, err := url.Parse(config.ChainNode.Url)
	if err != nil {
		logging.Error("Error parsing URL", types.EventProcessing, "error", err)
		return ""
	}

	// Modify the scheme to "ws" and append the "/websocket" path
	u.Scheme = "ws"
	u.Path = "/websocket"

	// Construct the new URL
	return u.String()
}

func GetParams(ctx context.Context, transactionRecorder cosmosclient.InferenceCosmosClient) (*types.QueryParamsResponse, error) {
	var params *types.QueryParamsResponse
	var err error
	for i := 0; i < 10; i++ {
		params, err = transactionRecorder.NewInferenceQueryClient().Params(ctx, &types.QueryParamsRequest{})
		if err == nil {
			return params, nil
		}

		if strings.HasPrefix(err.Error(), "rpc error: code = Unknown desc = inference is not ready") {
			logging.Info("Inference not ready, retrying...", types.System, "attempt", i+1, "error", err)
			time.Sleep(2 * time.Second) // Try a longer wait for specific inference delays
			continue
		}
		// If not an RPC error, log and return early
		logging.Error("Failed to get chain params", types.System, "error", err)
		return nil, err
	}
	logging.Error("Exhausted all retries to get chain params", types.System, "error", err)
	return nil, err
}

func getStatus(chainNodeUrl string) (*coretypes.ResultStatus, error) {
	client, err := cosmosclient.NewRpcClient(chainNodeUrl)
	if err != nil {
		return nil, err
	}

	status, err := client.Status(context.Background())
	if err != nil {
		return nil, err
	}

	return status, nil
}
