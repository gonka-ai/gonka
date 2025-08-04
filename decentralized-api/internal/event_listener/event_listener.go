package event_listener

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/bls"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/poc"
	"decentralized-api/internal/validation"
	"decentralized-api/logging"
	"decentralized-api/training"
	"decentralized-api/upgrade"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/productscience/inference/x/inference/types"
)

const (
	finishInferenceAction           = "/inference.inference.MsgFinishInference"
	startInferenceAction            = "/inference.inference.MsgStartInference"
	validationAction                = "/inference.inference.MsgValidation"
	trainingTaskAssignedAction      = "/inference.inference.MsgAssignTrainingTask"
	submitGovProposalAction         = "/cosmos.gov.v1.MsgSubmitProposal"
	requestThresholdSignatureAction = "/inference.bls.MsgRequestThresholdSignature"

	// BLS Typed Event Types (from EmitTypedEvent)
	blsKeyGenerationInitiatedEvent    = "inference.bls.EventKeyGenerationInitiated"
	blsVerifyingPhaseStartedEvent     = "inference.bls.EventVerifyingPhaseStarted"
	blsGroupPublicKeyGeneratedEvent   = "inference.bls.EventGroupPublicKeyGenerated"
	blsThresholdSigningRequestedEvent = "inference.bls.EventThresholdSigningRequested"

	newBlockEventType = "tendermint/event/NewBlock"
	txEventType       = "tendermint/event/Tx"
)

// TODO: write tests properly
type EventListener struct {
	nodeBroker          *broker.Broker
	configManager       *apiconfig.ConfigManager
	validator           *validation.InferenceValidator
	transactionRecorder cosmosclient.InferenceCosmosClient
	trainingExecutor    *training.Executor
	blsManager          *bls.BlsManager
	nodeCaughtUp        atomic.Bool
	phaseTracker        *chainphase.ChainPhaseTracker
	dispatcher          *OnNewBlockDispatcher
	cancelFunc          context.CancelFunc

	ws *websocket.Conn
}

func NewEventListener(
	configManager *apiconfig.ConfigManager,
	nodePocOrchestrator poc.NodePoCOrchestrator,
	nodeBroker *broker.Broker,
	validator *validation.InferenceValidator,
	transactionRecorder cosmosclient.InferenceCosmosClient,
	trainingExecutor *training.Executor,
	phaseTracker *chainphase.ChainPhaseTracker,
	cancelFunc context.CancelFunc,
	blsManager *bls.BlsManager,
) *EventListener {
	// Create the new block dispatcher
	dispatcher := NewOnNewBlockDispatcherFromCosmosClient(
		nodeBroker,
		configManager,
		nodePocOrchestrator,
		&transactionRecorder,
		phaseTracker,
		DefaultReconciliationConfig,
	)

	return &EventListener{
		nodeBroker:          nodeBroker,
		transactionRecorder: transactionRecorder,
		configManager:       configManager,
		validator:           validator,
		trainingExecutor:    trainingExecutor,
		phaseTracker:        phaseTracker,
		dispatcher:          dispatcher,
		cancelFunc:          cancelFunc,
		blsManager:          blsManager,
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

	// WARNING: It looks like Tendermint can't support more than 5 subscriptions per websocket
	// If we want to add more subscription we should subscribe to all TX and filter on our side
	subscribeToEvents(el.ws, 1, "tm.event='NewBlock'")
	// All transactions originating from the inference module
	subscribeToEvents(el.ws, 2, "tm.event='Tx' AND message.module='inference'")
	// All transactions originating from the BLS module
	subscribeToEvents(el.ws, 3, "tm.event='Tx' AND message.module='bls'")
	// Validation-specific flag (remains separate because it can originate from other modules)
	subscribeToEvents(el.ws, 4, "tm.event='Tx' AND inference_validation.needs_revalidation='true'")

	logging.Info("All subscription calls in openWsConnAndSubscribe have been made with new combined queries.", types.EventProcessing)
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
						logging.Error("Upgrade required! Shutting down the entire system...", types.Upgrades)
						el.cancelFunc()
						return
					}

				}

				logging.Warn("Close websocket connection", types.EventProcessing)
				el.ws.Close()

				logging.Warn("Reopen websocket", types.EventProcessing)
				time.Sleep(10 * time.Second)

				el.openWsConnAndSubscribe()
				continue
			}

			// logging.Debug("Raw websocket message received", types.EventProcessing, "raw_message_bytes", string(message))

			var event chainevents.JSONRPCResponse
			if err = json.Unmarshal(message, &event); err != nil {
				logging.Error("Error unmarshalling message to JSONRPCResponse", types.EventProcessing, "error", err, "raw_message_bytes", string(message))
				continue
			}

			// Detailed logging for event type evaluation
			isNewBlockTypeComparison := event.Result.Data.Type == newBlockEventType
			logging.Info("Event unmarshalled. Evaluating type...", types.EventProcessing,
				"event_id", event.ID,
				"subscription_query", event.Result.Query,
				"result_data_type", event.Result.Data.Type,
				"comparing_against_type", newBlockEventType,
				"is_new_block_event_type_result", isNewBlockTypeComparison)

			if isNewBlockTypeComparison {
				logging.Info("Event classified as NewBlock", types.EventProcessing, "ID", event.ID, "subscription_query", event.Result.Query, "result_data_type", event.Result.Data.Type)
				blockQueue.In <- &event
				continue
			}

			logging.Info("Adding event to the main event queue (classified as non-NewBlock)", types.EventProcessing, "type", event.Result.Data.Type, "id", event.ID, "subscription_query", event.Result.Query)
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
		isSynced := !status.SyncInfo.CatchingUp
		el.updateNodeSyncStatus(isSynced)
		// Note: Sync status is now handled by the dispatcher during block processing
		logging.Debug("Updated sync status", types.EventProcessing, "caughtUp", isSynced, "height", status.SyncInfo.LatestBlockHeight)
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
			// Check for BLS events in NewBlock events (emitted from EndBlocker)
			el.handleBLSEvents(event, workerName)
		}

		// Parse the event into NewBlockInfo
		blockInfo, err := parseNewBlockInfo(event)
		if err != nil {
			logging.Error("Failed to parse new block info", types.EventProcessing, "error", err, "worker", workerName)
			return
		}

		// Process using the new dispatcher
		ctx := context.Background() // We could pass this from caller if needed
		err = el.dispatcher.ProcessNewBlock(ctx, *blockInfo)
		if err != nil {
			logging.Error("Failed to process new block", types.EventProcessing, "error", err, "worker", workerName)
		}

		// Still handle upgrade processing separately
		upgrade.ProcessNewBlockEvent(event, el.transactionRecorder, el.configManager)

	case txEventType:
		el.handleMessage(event, workerName)
	default:
		logging.Warn("Unexpected event type received", types.EventProcessing, "type", event.Result.Data.Type)
	}
}

func (el *EventListener) handleBLSEvents(event *chainevents.JSONRPCResponse, workerName string) {
	// Check for BLS events in NewBlock events (emitted from EndBlocker)
	// Note: Threshold signing events are handled separately in handleBLSTransactionEvents

	if epochIdValues := event.Result.Events[blsKeyGenerationInitiatedEvent+".epoch_id"]; len(epochIdValues) > 0 {
		logging.Info("Key generation initiated event received", types.EventProcessing, "worker", workerName)
		err := el.blsManager.ProcessKeyGenerationInitiated(event)
		if err != nil {
			logging.Error("Failed to process key generation initiated event", types.EventProcessing, "error", err, "worker", workerName)
		}
	}

	if epochIdValues := event.Result.Events[blsVerifyingPhaseStartedEvent+".epoch_id"]; len(epochIdValues) > 0 {
		logging.Info("Verifying phase started event received", types.EventProcessing, "worker", workerName)
		err := el.blsManager.ProcessVerifyingPhaseStarted(event)
		if err != nil {
			logging.Error("Failed to process verifying phase started event", types.EventProcessing, "error", err, "worker", workerName)
		}
	}

	if epochIdValues := event.Result.Events[blsGroupPublicKeyGeneratedEvent+".epoch_id"]; len(epochIdValues) > 0 {
		logging.Info("Group public key generated event received", types.EventProcessing, "worker", workerName)
		err := el.blsManager.ProcessGroupPublicKeyGenerated(event)
		if err != nil {
			logging.Error("Failed to process group public key generated event", types.EventProcessing, "error", err, "worker", workerName)
		}
	}
}

func (el *EventListener) handleBLSTransactionEvents(event *chainevents.JSONRPCResponse, workerName string) {
	// Only handle threshold signing events in transaction events (emitted from message processing)
	if requestIdValues := event.Result.Events[blsThresholdSigningRequestedEvent+".request_id"]; len(requestIdValues) > 0 {
		logging.Info("Threshold signing requested event received (from transaction)", types.EventProcessing, "worker", workerName)
		if el.isNodeSynced() {
			err := el.blsManager.ProcessThresholdSigningRequested(event)
			if err != nil {
				logging.Error("Failed to process threshold signing requested event", types.EventProcessing, "error", err, "worker", workerName)
			}
		}
	}
}

func (el *EventListener) handleMessage(event *chainevents.JSONRPCResponse, name string) {
	if waitForEventHeight(event, el.configManager, name) {
		logging.Warn("Event height not reached yet, skipping", types.EventProcessing, "event", event)
		return
	}

	// Check for transaction-specific BLS events (only threshold signing events are emitted from message processing)
	el.handleBLSTransactionEvents(event, name)

	actions, ok := event.Result.Events["message.action"]
	if !ok || len(actions) == 0 {
		// Handle the missing key or empty slice.
		// For example, log an error, return from the function, etc.
		logging.Info("No message.action event found", types.EventProcessing, "event", event)
		return // or handle it accordingly
	}

	action := actions[0]
	// Get the keys of the map event.Result.Events:
	//for key := range event.Result.Events {
	//	for i, attr := range event.Result.Events[key] {
	//		logging.Debug("\tEventValue", "key", key, "attr", attr, "index", i)
	//	}
	//}

	switch action {
	case startInferenceAction, finishInferenceAction:
		logging.Info("New Tx event received", types.EventProcessing, "type", event.Result.Data.Type, "action", action, "worker", name)
		if el.isNodeSynced() {
			el.validator.SampleInferenceToValidate(
				event.Result.Events["inference_finished.inference_id"],
				el.transactionRecorder,
			)
		}
	case validationAction:
		logging.Info("New Tx event received", types.EventProcessing, "type", event.Result.Data.Type, "action", action, "worker", name)
		if el.isNodeSynced() {
			el.validator.VerifyInvalidation(event.Result.Events, el.transactionRecorder)
		}
	case submitGovProposalAction:
		logging.Info("New Tx event received", types.EventProcessing, "type", event.Result.Data.Type, "action", action, "worker", name)
		proposalIdOrNil := event.Result.Events["proposal_id"]
		logging.Debug("New proposal submitted", types.EventProcessing, "proposalId", proposalIdOrNil)
	case trainingTaskAssignedAction:
		logging.Info("New Tx event received", types.EventProcessing, "type", event.Result.Data.Type, "action", action, "worker", name)
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
	case requestThresholdSignatureAction:
	default:
		logging.Debug("Unhandled action received", types.EventProcessing, "action", action)
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
