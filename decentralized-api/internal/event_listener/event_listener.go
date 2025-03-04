package event_listener

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainevents"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/poc"
	"decentralized-api/internal/server"
	"decentralized-api/upgrade"
	"encoding/json"
	"fmt"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/gorilla/websocket"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
	"log"
	"log/slog"
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
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		status, err := getStatus(chainNodeUrl)
		if err != nil {
			slog.Error("Error getting node status", "error", err)
			continue
		}
		// The node is "synced" if it's NOT catching up.
		updateNodeSyncStatus(!status.SyncInfo.CatchingUp)
		slog.Debug("Updated sync status", "caughtUp", !status.SyncInfo.CatchingUp, "height", status.SyncInfo.LatestBlockHeight)
	}
}

const (
	finishInferenceAction   = "/inference.inference.MsgFinishInference"
	validationAction        = "/inference.inference.MsgValidation"
	submitGovProposalAction = "/cosmos.gov.v1.MsgSubmitProposal"

	newBlockEventType = "tendermint/event/NewBlock"
	txEventType       = "tendermint/event/Tx"
)

func StartEventListener(
	nodeBroker *broker.Broker,
	transactionRecorder cosmosclient.InferenceCosmosClient,
	configManager *apiconfig.ConfigManager,
	params *types.Params,
) {
	websocketUrl := getWebsocketUrl(configManager.GetConfig())
	slog.Info("Connecting to websocket at", "url", websocketUrl)
	ws, _, err := websocket.DefaultDialer.Dial(websocketUrl, nil)
	if err != nil {
		slog.Error("Failed to connect to websocket", "error", err)
		log.Fatal("dial:", err)
	}
	defer ws.Close()

	// Subscribe to custom events
	subscribeToEvents(ws, "tm.event='Tx' AND message.action='"+finishInferenceAction+"'")
	subscribeToEvents(ws, "tm.event='NewBlock'")
	subscribeToEvents(ws, "tm.event='Tx' AND inference_validation.needs_revalidation='true'")
	subscribeToEvents(ws, "tm.event='Tx' AND message.action='"+submitGovProposalAction+"'")

	go startSyncStatusChecker(configManager.GetConfig().ChainNode.Url)

	pubKey, err := transactionRecorder.Account.Record.GetPubKey()
	if err != nil {
		slog.Error("Failed to get public key", "error", err)
		return
	}
	pubKeyString := utils.PubKeyToHexString(pubKey)

	slog.Debug("Initializing PoC orchestrator",
		"name", transactionRecorder.Account.Name,
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
	slog.Info("PoC orchestrator initialized", "nodePocOrchestrator", nodePocOrchestrator)
	go pocOrchestrator.Run()

	eventChan := make(chan *chainevents.JSONRPCResponse, 100)
	numWorkers := 10
	for i := 0; i < numWorkers; i++ {
		go func() {
			for event := range eventChan {
				if event == nil {
					slog.Error("Go worker received nil chain event")
					continue
				}
				processEvent(event, nodeBroker, transactionRecorder, configManager, nodePocOrchestrator)
			}
		}()
	}

	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			slog.Warn("Failed to read a websocket message", "errorType", fmt.Sprintf("%T", err), "error", err)

			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Warn("Websocket connection closed", "errorType", fmt.Sprintf("%T", err), "error", err)
				if upgrade.CheckForUpgrade(configManager) {
					slog.Error("Upgrade required! Exiting...")
					panic("Upgrade required")
				}
				continue
			}
			continue
		}

		var event chainevents.JSONRPCResponse
		if err = json.Unmarshal(message, &event); err != nil {
			slog.Error("Error unmarshalling message to JSONRPCResponse", "error", err, "message", message)
			continue // no sense to check event, if it wasn't unmarshalled correctly
		}

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
) {
	switch event.Result.Data.Type {
	case newBlockEventType:
		slog.Debug("New block event received", "type", event.Result.Data.Type)
		if isNodeSynced() {
			poc.ProcessNewBlockEvent(nodePocOrchestrator, event, transactionRecorder, configManager)
		}
		upgrade.ProcessNewBlockEvent(event, transactionRecorder, configManager)
	case txEventType:
		handleMessage(nodeBroker, transactionRecorder, event, configManager.GetConfig())
	default:
		slog.Warn("Unexpected event type received", "type", event.Result.Data.Type)
	}
}

func handleMessage(
	nodeBroker *broker.Broker,
	transactionRecorder cosmosclient.InferenceCosmosClient,
	event *chainevents.JSONRPCResponse,
	currentConfig *apiconfig.Config,
) {
	if waitForEventHeight(event, currentConfig) {
		return
	}

	actions, ok := event.Result.Events["message.action"]
	if !ok || len(actions) == 0 {
		// Handle the missing key or empty slice.
		// For example, log an error, return from the function, etc.
		slog.Info("No message.action event found", "event", event)
		return // or handle it accordingly
	}

	action := actions[0]
	slog.Debug("New Tx event received", "type", event.Result.Data.Type, "action", action)
	// Get the keys of the map event.Result.Events:
	//for key := range event.Result.Events {
	//	for i, attr := range event.Result.Events[key] {
	//		slog.Debug("\tEventValue", "key", key, "attr", attr, "index", i)
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
		slog.Debug("New proposal submitted", "proposalId", proposalIdOrNil)
	default:
		slog.Debug("Unhandled action received", "action", action)
	}
}

// currentConfig must be a pointer, or it won't update
func waitForEventHeight(event *chainevents.JSONRPCResponse, currentConfig *apiconfig.Config) bool {
	heightString := event.Result.Events["tx.height"][0]
	expectedHeight, err := strconv.ParseInt(heightString, 10, 64)
	if err != nil {
		slog.Error("Failed to parse height", "error", err)
		return true
	}
	for currentConfig.CurrentHeight < expectedHeight {
		slog.Info("Height race condition! Waiting for height to catch up", "currentHeight", currentConfig.CurrentHeight, "expectedHeight", expectedHeight)
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func subscribeToEvents(ws *websocket.Conn, query string) {
	subscribeMsg := fmt.Sprintf(`{"jsonrpc": "2.0", "method": "subscribe", "id": "1", "params": ["%s"]}`, query)
	if err := ws.WriteMessage(websocket.TextMessage, []byte(subscribeMsg)); err != nil {
		slog.Error("Failed to subscribe to a websocket", "error", err)
		log.Fatalf("Failed to subscribe to a websocket. %v", err)
	}
}

func getWebsocketUrl(config *apiconfig.Config) string {
	// Parse the input URL
	u, err := url.Parse(config.ChainNode.Url)
	if err != nil {
		slog.Error("Error parsing URL", "error", err)
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
			slog.Info("Inference not ready, retrying...", "attempt", i+1, "error", err)
			time.Sleep(2 * time.Second) // Try a longer wait for specific inference delays
			continue
		}
		// If not an RPC error, log and return early
		slog.Error("Failed to get chain params", "error", err)
		return nil, err
	}
	slog.Error("Exhausted all retries to get chain params", "error", err)
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
