package event_listener

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/poc"
	"decentralized-api/internal/validation"
	"decentralized-api/logging"
	"decentralized-api/training"
	"decentralized-api/upgrade"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/productscience/inference/x/inference/types"
	"log"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	finishInferenceAction      = "/inference.inference.MsgFinishInference"
	startInferenceAction       = "/inference.inference.MsgStartInference"
	validationAction           = "/inference.inference.MsgValidation"
	trainingTaskAssignedAction = "/inference.inference.MsgAssignTrainingTask"
	submitGovProposalAction    = "/cosmos.gov.v1.MsgSubmitProposal"

	newBlockEventType = "tendermint/event/NewBlock"
	txEventType       = "tendermint/event/Tx"
)

// TODO: write tests properly
type EventListener struct {
	nodeBroker          *broker.Broker
	configManager       *apiconfig.ConfigManager
	nodePocOrchestrator *poc.NodePoCOrchestrator
	validator           *validation.InferenceValidator
	transactionRecorder cosmosclient.InferenceCosmosClient
	trainingExecutor    *training.Executor
	nodeCaughtUp        atomic.Bool

	ws *websocket.Conn
}

func NewEventListener(
	configManager *apiconfig.ConfigManager,
	nodePocOrchestrator *poc.NodePoCOrchestrator,
	nodeBroker *broker.Broker,
	validator *validation.InferenceValidator,
	transactionRecorder cosmosclient.InferenceCosmosClient,
	trainingExecutor *training.Executor,
) *EventListener {
	return &EventListener{
		nodeBroker:          nodeBroker,
		transactionRecorder: transactionRecorder,
		configManager:       configManager,
		nodePocOrchestrator: nodePocOrchestrator,
		validator:           validator,
		trainingExecutor:    trainingExecutor,
	}
}

func (el *EventListener) openWsConnAndSubscribe() {
	websocketUrl := getWebsocketUrl(el.configManager.GetChainNodeConfig().Url)
	logging.Info("Connecting to websocket at", types.EventProcessing, "url", websocketUrl)

	ws, _, err := websocket.DefaultDialer.Dial(websocketUrl, nil)
	if err != nil {
		logging.Error("Failed to connect to websocket", types.EventProcessing, "error", err)
		log.Fatal("dial:", err)
	}
	el.ws = ws

	subscribeToEvents(el.ws, "tm.event='Tx' AND message.action='"+finishInferenceAction+"'")
	subscribeToEvents(el.ws, "tm.event='Tx' AND message.action='"+startInferenceAction+"'")
	subscribeToEvents(el.ws, "tm.event='NewBlock'")
	subscribeToEvents(el.ws, "tm.event='Tx' AND inference_validation.needs_revalidation='true'")
	subscribeToEvents(el.ws, "tm.event='Tx' AND message.action='"+submitGovProposalAction+"'")
	subscribeToEvents(el.ws, "tm.event='Tx' AND message.action='"+trainingTaskAssignedAction+"'")
}

func (el *EventListener) Start(ctx context.Context) {
	el.openWsConnAndSubscribe()
	defer el.ws.Close()

	go el.startSyncStatusChecker()

	mainEventQueue := NewUnboundedQueue[*chainevents.JSONRPCResponse]()
	defer mainEventQueue.Close()
	el.processEvents(ctx, mainEventQueue)

	blockEventQueue := NewUnboundedQueue[*chainevents.JSONRPCResponse]()
	defer blockEventQueue.Close()
	el.processBlockEvents(ctx, blockEventQueue)

	el.listen(ctx, blockEventQueue, mainEventQueue)
}

func worker(
	ctx context.Context,
	eventQueue *UnboundedQueue[*chainevents.JSONRPCResponse],
	processEvent func(event *chainevents.JSONRPCResponse, workerName string),
	workerName string) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-eventQueue.Out:
				if !ok {
					logging.Warn(workerName+": event channel is closed", types.System)
					return
				}
				if event == nil {
					logging.Error(workerName+": received nil chain event", types.System)
				} else {
					processEvent(event, workerName)
				}
			}
		}
	}()
}

func (el *EventListener) processEvents(ctx context.Context, mainQueue *UnboundedQueue[*chainevents.JSONRPCResponse]) {
	const numWorkers = 10
	for i := 0; i < numWorkers; i++ {
		worker(ctx, mainQueue, el.processEvent, "process_events_"+strconv.Itoa(i))
	}
}

func (el *EventListener) processBlockEvents(ctx context.Context, blockQueue *UnboundedQueue[*chainevents.JSONRPCResponse]) {
	const numWorkers = 2
	for i := 0; i < numWorkers; i++ {
		worker(ctx, blockQueue, el.processEvent, "process_block_events")
	}
}

func (el *EventListener) listen(ctx context.Context, blockQueue, mainQueue *UnboundedQueue[*chainevents.JSONRPCResponse]) {
	for {
		select {
		case <-ctx.Done():
			logging.Info("Close ws connection", types.EventProcessing)
			return
		default:
			_, message, err := el.ws.ReadMessage()
			if err != nil {
				logging.Warn("Failed to read a websocket message", types.EventProcessing, "errorType", fmt.Sprintf("%T", err), "error", err)

				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					logging.Warn("Websocket connection closed", types.EventProcessing, "errorType", fmt.Sprintf("%T", err), "error", err)

					if upgrade.CheckForUpgrade(el.configManager) {
						logging.Error("Upgrade required! Exiting...", types.Upgrades)
						panic("Upgrade required")
					}

				}

				logging.Warn("Close websocket connection", types.EventProcessing)
				el.ws.Close()

				logging.Warn("Reopen websocket", types.EventProcessing)
				time.Sleep(10 * time.Second)

				el.openWsConnAndSubscribe()
				continue
			}

			var event chainevents.JSONRPCResponse
			if err = json.Unmarshal(message, &event); err != nil {
				logging.Error("Error unmarshalling message to JSONRPCResponse", types.EventProcessing, "error", err, "message", message)
				continue
			}

			if event.Result.Data.Type == newBlockEventType {
				logging.Debug("New block event received", types.EventProcessing, "ID", event.ID)
				blockQueue.In <- &event
				continue
			}

			logging.Info("Adding event to queue", types.EventProcessing, "type", event.Result.Data.Type, "id", event.ID)
			select {
			case mainQueue.In <- &event:
				logging.Debug("Event successfully queued", types.EventProcessing, "type", event.Result.Data.Type, "id", event.ID)
			default:
				logging.Warn("Event channel full, dropping event", types.EventProcessing, "type", event.Result.Data.Type, "id", event.ID)
			}
		}
	}
}

func (el *EventListener) startSyncStatusChecker() {
	chainNodeUrl := el.configManager.GetChainNodeConfig().Url

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
func (el *EventListener) processEvent(event *chainevents.JSONRPCResponse, workerName string) {
	switch event.Result.Data.Type {
	case newBlockEventType:
		logging.Debug("New block event received", types.EventProcessing, "type", event.Result.Data.Type, "worker", workerName)
		if el.isNodeSynced() {
			poc.ProcessNewBlockEvent(el.nodePocOrchestrator, event, el.transactionRecorder, el.configManager)
		}
		upgrade.ProcessNewBlockEvent(event, el.transactionRecorder, el.configManager)
	case txEventType:
		logging.Debug("New Tx event received", types.EventProcessing, "type", event.Result.Data.Type, "worker", workerName)
		el.handleMessage(event, workerName)
	default:
		logging.Warn("Unexpected event type received", types.EventProcessing, "type", event.Result.Data.Type)
	}
}

func (el *EventListener) handleMessage(event *chainevents.JSONRPCResponse, name string) {
	if waitForEventHeight(event, el.configManager, name) {
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
	logging.Info("New Tx event received", types.EventProcessing, "type", event.Result.Data.Type, "action", action, "worker", name)
	// Get the keys of the map event.Result.Events:
	//for key := range event.Result.Events {
	//	for i, attr := range event.Result.Events[key] {
	//		logging.Debug("\tEventValue", "key", key, "attr", attr, "index", i)
	//	}
	//}
	switch action {
	case startInferenceAction, finishInferenceAction:
		if el.isNodeSynced() {
			el.validator.SampleInferenceToValidate(
				event.Result.Events["inference_finished.inference_id"],
				el.transactionRecorder,
			)
		}
	case validationAction:
		if el.isNodeSynced() {
			el.validator.VerifyInvalidation(event.Result.Events, el.transactionRecorder)
		}
	case submitGovProposalAction:
		proposalIdOrNil := event.Result.Events["proposal_id"]
		logging.Debug("New proposal submitted", types.EventProcessing, "proposalId", proposalIdOrNil)
	case trainingTaskAssignedAction:
		if el.isNodeSynced() {
			logging.Info("MsgAssignTrainingTask event", types.EventProcessing, "event", event)
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
				el.trainingExecutor.ProcessTaskAssignedEvent(taskIdUint)
			}
		}
	default:
		logging.Warn("Unhandled action received", types.EventProcessing, "action", action)
	}
}

func waitForEventHeight(event *chainevents.JSONRPCResponse, currentConfig *apiconfig.ConfigManager, name string) bool {
	heightString := event.Result.Events["tx.height"][0]
	expectedHeight, err := strconv.ParseInt(heightString, 10, 64)
	if err != nil {
		logging.Error("Failed to parse height", types.EventProcessing, "error", err)
		return true
	}
	for currentConfig.GetHeight() < expectedHeight {
		logging.Info("Height race condition! Waiting for height to catch up", types.EventProcessing, "currentHeight", currentConfig.GetHeight(), "expectedHeight", expectedHeight, "worker", name)
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
