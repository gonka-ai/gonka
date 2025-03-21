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
	"sync/atomic"
	"time"
)

const (
	finishInferenceAction   = "/inference.inference.MsgFinishInference"
	validationAction        = "/inference.inference.MsgValidation"
	submitGovProposalAction = "/cosmos.gov.v1.MsgSubmitProposal"

	newBlockEventType = "tendermint/event/NewBlock"
	txEventType       = "tendermint/event/Tx"
)

type EventListener struct {
	nodeBroker          *broker.Broker
	transactionRecorder cosmosclient.InferenceCosmosClient
	configManager       *apiconfig.ConfigManager
	params              *types.Params
	nodeCaughtUp        atomic.Bool

	nodePocOrchestrator *poc.NodePoCOrchestrator
	ws                  *websocket.Conn
}

func NewEventListener(
	configManager *apiconfig.ConfigManager,
	params *types.Params,
	nodeBroker *broker.Broker,
	transactionRecorder cosmosclient.InferenceCosmosClient) *EventListener {
	return &EventListener{
		nodeBroker:          nodeBroker,
		transactionRecorder: transactionRecorder,
		configManager:       configManager,
		params:              params,
	}
}

func (el *EventListener) openWsConn() {
	websocketUrl := getWebsocketUrl(el.configManager.GetConfig())
	logging.Info("Connecting to websocket at", types.EventProcessing, "url", websocketUrl)

	ws, _, err := websocket.DefaultDialer.Dial(websocketUrl, nil)
	if err != nil {
		logging.Error("Failed to connect to websocket", types.EventProcessing, "error", err)
		log.Fatal("dial:", err)
	}
	el.ws = ws
}

func (el *EventListener) Start(ctx context.Context) {
	el.openWsConn()

	go el.startSyncStatusChecker()
	pubKey, err := el.transactionRecorder.Account.Record.GetPubKey()
	if err != nil {
		logging.Error("Failed to get public key", types.EventProcessing, "error", err)
		return
	}
	pubKeyString := utils.PubKeyToHexString(pubKey)

	logging.Debug("Initializing PoC orchestrator",
		types.PoC, "name", el.transactionRecorder.Account.Name,
		"address", el.transactionRecorder.Address,
		"pubkey", pubKeyString)

	// TODO init pocOrchestrator somewhere else and apss as ready object?
	pocOrchestrator := poc.NewPoCOrchestrator(pubKeyString, int(el.params.PocParams.DefaultDifficulty))

	// PRTODO: decide if host is just host or host+port????? or url. Think what better name and stuff
	nodePocOrchestrator := poc.NewNodePoCOrchestrator(
		pubKeyString,
		el.nodeBroker,
		el.configManager.GetConfig().Api.PoCCallbackUrl,
		el.configManager.GetConfig().ChainNode.Url,
		&el.transactionRecorder,
		el.params,
	)
	el.nodePocOrchestrator = nodePocOrchestrator

	logging.Info("PoC orchestrator initialized", types.PoC, "nodePocOrchestrator", nodePocOrchestrator)
	go pocOrchestrator.Run()

	eventChan := make(chan *chainevents.JSONRPCResponse, 100)
	defer close(eventChan)
	el.processEvents(ctx, eventChan)

	blockEventChan := make(chan *chainevents.JSONRPCResponse, 100)
	defer close(blockEventChan)
	el.processBlockEvents(ctx, blockEventChan)

	el.listen(blockEventChan, eventChan)
}

func (el *EventListener) processEvents(ctx context.Context, eventChan chan *chainevents.JSONRPCResponse) {
	numWorkers := 10
	for i := 0; i < numWorkers; i++ {
		go func() {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-eventChan:
				if !ok {
					logging.Warn("Event channel is closed", types.System)
					return
				}
				if event == nil {
					logging.Error("processEvents Go worker received nil chain event", types.System)
				} else {
					el.processEvent(event)
				}
			}
		}()
	}
}

func (el *EventListener) processBlockEvents(ctx context.Context, blockEventChan chan *chainevents.JSONRPCResponse) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-blockEventChan:
			if !ok {
				logging.Warn("blockEvent channel is closed", types.System)
				return
			}

			if event == nil {
				logging.Error("processBlockEvents Go worker received nil chain event", types.System)
			} else {
				el.processEvent(event)
			}
		}
	}()
}

func (el *EventListener) listen(blockEventChan, eventChan chan *chainevents.JSONRPCResponse) {
	for {
		_, message, err := el.ws.ReadMessage()
		if err != nil {
			logging.Warn("Failed to read a websocket message", types.EventProcessing, "errorType", fmt.Sprintf("%T", err), "error", err)

			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logging.Warn("Websocket connection closed", types.EventProcessing, "errorType", fmt.Sprintf("%T", err), "error", err)
				if upgrade.CheckForUpgrade(el.configManager) {
					logging.Error("Upgrade required! Exiting...", types.Upgrades)
					panic("Upgrade required")
				}

				el.ws.Close()
				logging.Warn("Reopen websocket", types.EventProcessing)

				time.Sleep(10 * time.Second) // TODO add increasing delay here and num of tries
				el.openWsConn()
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
		eventChan <- &event
	}

	el.ws.Close()
}

func (el *EventListener) startSyncStatusChecker() {
	chainNodeUrl := el.configManager.GetConfig().ChainNode.Url

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		status, err := getStatus(chainNodeUrl)
		if err != nil {
			logging.Error("Error getting node status", types.EventProcessing, "error", err)
			continue
		}
		// The node is "synced" if it's NOT catching up.
		el.updateNodeSyncStatus(!status.SyncInfo.CatchingUp)
		logging.Debug("Updated sync status", types.EventProcessing, "caughtUp", !status.SyncInfo.CatchingUp, "height", status.SyncInfo.LatestBlockHeight)
	}
}

func (el *EventListener) isNodeSynced() bool {
	return el.nodeCaughtUp.Load()
}

func (el *EventListener) updateNodeSyncStatus(status bool) {
	el.nodeCaughtUp.Store(status)
}

// processEvent is the worker function that processes a JSONRPCResponse event.
func (el *EventListener) processEvent(event *chainevents.JSONRPCResponse) {
	switch event.Result.Data.Type {
	case newBlockEventType:
		logging.Debug("New block event received", types.EventProcessing, "type", event.Result.Data.Type)
		if el.isNodeSynced() {
			poc.ProcessNewBlockEvent(el.nodePocOrchestrator, event, el.transactionRecorder, el.configManager)
		}
		upgrade.ProcessNewBlockEvent(event, el.transactionRecorder, el.configManager)
	case txEventType:
		el.handleMessage(event)
	default:
		logging.Warn("Unexpected event type received", types.EventProcessing, "type", event.Result.Data.Type)
	}
}

func (el *EventListener) handleMessage(event *chainevents.JSONRPCResponse) {
	currentConfig := el.configManager.GetConfig()
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
		if el.isNodeSynced() {
			server.SampleInferenceToValidate(event.Result.Events["inference_finished.inference_id"], el.transactionRecorder, el.nodeBroker, currentConfig)
		}
	case validationAction:
		if el.isNodeSynced() {
			server.VerifyInvalidation(event.Result.Events, el.transactionRecorder, el.nodeBroker)
		}
	case submitGovProposalAction:
		proposalIdOrNil := event.Result.Events["proposal_id"]
		logging.Debug("New proposal submitted", types.EventProcessing, "proposalId", proposalIdOrNil)
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
