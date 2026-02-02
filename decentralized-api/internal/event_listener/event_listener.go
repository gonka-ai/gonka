package event_listener

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/bls"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/startup"
	"decentralized-api/internal/validation"
	"decentralized-api/logging"
	"decentralized-api/poc"
	"decentralized-api/training"
	"decentralized-api/upgrade"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/productscience/inference/x/inference/types"
)

const (
	// BLS Typed Event Types (from EmitTypedEvent)
	blsKeyGenerationInitiatedEvent    = "inference.bls.EventKeyGenerationInitiated"
	blsVerifyingPhaseStartedEvent     = "inference.bls.EventVerifyingPhaseStarted"
	blsGroupPublicKeyGeneratedEvent   = "inference.bls.EventGroupPublicKeyGenerated"
	blsThresholdSigningRequestedEvent = "inference.bls.EventThresholdSigningRequested"

	newBlockEventType      = "tendermint/event/NewBlock"
	txEventType            = "tendermint/event/Tx"
	systemBarrierEventType = "decentralized-api/event/Barrier"
)

// TODO: write tests properly
type EventListener struct {
	nodeBroker            *broker.Broker
	configManager         *apiconfig.ConfigManager
	validator             *validation.InferenceValidator
	transactionRecorder   cosmosclient.InferenceCosmosClient
	trainingExecutor      *training.Executor
	blsManager            *bls.BlsManager
	nodeCaughtUp          atomic.Bool
	phaseTracker          *chainphase.ChainPhaseTracker
	dispatcher            *OnNewBlockDispatcher
	cancelFunc            context.CancelFunc
	rewardRecoveryChecker *startup.RewardRecoveryChecker

	eventHandlers []EventHandler

	ws            *websocket.Conn
	blockObserver *BlockObserver
}

func NewEventListener(
	configManager *apiconfig.ConfigManager,
	pocOrchestrator poc.Orchestrator,
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
		pocOrchestrator,
		&transactionRecorder,
		phaseTracker,
		DefaultReconciliationConfig,
		validator,
	)

	eventHandlers := []EventHandler{
		&BlsTransactionEventHandler{},
		&InferenceFinishedEventHandler{},
		&InferenceValidationEventHandler{},
		&SubmitProposalEventHandler{},
		&TrainingTaskAssignedEventHandler{},
	}

	bo := NewBlockObserver(configManager)

	return &EventListener{
		nodeBroker:            nodeBroker,
		transactionRecorder:   transactionRecorder,
		configManager:         configManager,
		validator:             validator,
		trainingExecutor:      trainingExecutor,
		phaseTracker:          phaseTracker,
		dispatcher:            dispatcher,
		cancelFunc:            cancelFunc,
		blsManager:            blsManager,
		eventHandlers:         eventHandlers,
		blockObserver:         bo,
		rewardRecoveryChecker: startup.NewRewardRecoveryChecker(phaseTracker, &transactionRecorder, validator, configManager),
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

	// Subscribe only to NewBlock events; all Tx events will be polled via BlockObserver
	subscribeToEvents(el.ws, 1, "tm.event='NewBlock'")

	logging.Info("Subscribed to NewBlock only; Tx will be polled by BlockObserver.", types.EventProcessing)
}

func (el *EventListener) Start(ctx context.Context) {
	el.openWsConnAndSubscribe()
	defer el.ws.Close()

	go el.startSyncStatusChecker()

	// Start processing of Tx events sourced by BlockObserver
	el.processEvents(ctx, el.blockObserver.Queue)

	blockEventQueue := NewUnboundedQueue[*chainevents.JSONRPCResponse]()
	defer blockEventQueue.Close()
	el.processBlockEvents(ctx, blockEventQueue)

	// Start BlockObserver
	go el.blockObserver.Process(ctx)

	el.listen(ctx, blockEventQueue, el.blockObserver.Queue)
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

			// We no longer subscribe to Tx over WS; ignore other event types
			logging.Debug("Ignoring non-NewBlock WS event", types.EventProcessing, "type", event.Result.Data.Type)
		}
	}
}

func (el *EventListener) startSyncStatusChecker() {
	chainNodeUrl := el.configManager.GetChainNodeConfig().Url
	hasTriedVersionSync := false

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
		wasAlreadySynced := el.isNodeSynced()
		el.updateNodeSyncStatus(isSynced)

		if isSynced && !wasAlreadySynced && !hasTriedVersionSync {
			hasTriedVersionSync = true
			go func() {
				queryClient := el.transactionRecorder.NewInferenceQueryClient()
				if err := el.configManager.SyncVersionFromChain(queryClient); err != nil {
					logging.Debug("MLNode version sync failed after blockchain ready", types.Config, "error", err)
				} else {
					logging.Info("MLNode version synced successfully after blockchain ready", types.Config)
				}
			}()
		}

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

		// Update BlockObserver with latest height and sync status
		el.blockObserver.updateStatus(blockInfo.Height, el.isNodeSynced())

		// Process using the new dispatcher
		ctx := context.Background() // We could pass this from caller if needed
		err = el.dispatcher.ProcessNewBlock(ctx, *blockInfo)
		if err != nil {
			logging.Error("Failed to process new block", types.EventProcessing, "error", err, "worker", workerName)
		}

		// Still handle upgrade processing separately
		upgrade.ProcessNewBlockEvent(event, el.transactionRecorder, el.configManager)
		if el.isNodeSynced() {
			el.rewardRecoveryChecker.RecoverIfNeeded(blockInfo.Height)
		}

	case txEventType:
		if el.hasHandler(event) {
			el.handleMessage(event, workerName)
		}
	case systemBarrierEventType:
		heights := event.Result.Events["barrier.height"]
		if len(heights) > 0 {
			height, err := strconv.ParseInt(heights[0], 10, 64)
			if err == nil {
				el.blockObserver.signalAllEventsRead(height)
			} else {
				logging.Warn("Invalid barrier height", types.EventProcessing, "value", heights[0], "error", err)
			}
		}
	default:
		logging.Warn("Unexpected event type received", types.EventProcessing, "type", event.Result.Data.Type)
	}
}

func (el *EventListener) hasHandler(event *chainevents.JSONRPCResponse) bool {
	for _, handler := range el.eventHandlers {
		if handler.CanHandle(event) {
			return true
		}
	}
	return false
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

func (el *EventListener) handleMessage(event *chainevents.JSONRPCResponse, name string) {
	if waitForEventHeight(event, el.configManager, name) {
		logging.Warn("Event height not reached yet, skipping", types.EventProcessing, "event", event)
		return
	}

	for _, handler := range el.eventHandlers {
		if handler.CanHandle(event) {
			logging.Info("Handling event", types.EventProcessing, "event", event, "handler", handler.GetName(), "worker", name)
			err := handler.Handle(event, el)
			if err != nil {
				logging.Error("Failed to handle event", types.EventProcessing, "error", err, "event", event)
			}
		}
	}
}

type EventHandler interface {
	GetName() string
	CanHandle(event *chainevents.JSONRPCResponse) bool
	Handle(event *chainevents.JSONRPCResponse, el *EventListener) error
}
type BlsTransactionEventHandler struct{}

func (e *BlsTransactionEventHandler) GetName() string {
	return "bls_transaction"
}

func (e *BlsTransactionEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events[blsThresholdSigningRequestedEvent+".request_id"]) > 0
}

func (e *BlsTransactionEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	if el.isNodeSynced() {
		return el.blsManager.ProcessThresholdSigningRequested(event)
	}
	return nil
}

type InferenceFinishedEventHandler struct {
}

func (e *InferenceFinishedEventHandler) GetName() string {
	return "inference_finished"
}

func (e *InferenceFinishedEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events["inference_finished.inference_id"]) > 0
}

func (e *InferenceFinishedEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	if el.isNodeSynced() {
		el.validator.SampleInferenceToValidate(event.Result.Events["inference_finished.inference_id"], el.transactionRecorder)
	}
	return nil
}

type InferenceValidationEventHandler struct {
}

func (e *InferenceValidationEventHandler) GetName() string {
	return "inference_validation"
}

func (e *InferenceValidationEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	needsRevalidation := event.Result.Events["inference_validation.needs_revalidation"]
	return len(needsRevalidation) > 0 && needsRevalidation[0] == "true"
}

func (e *InferenceValidationEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	if el.isNodeSynced() {
		el.validator.VerifyInvalidation(event.Result.Events, el.transactionRecorder)
	}
	return nil
}

type SubmitProposalEventHandler struct{}

func (e *SubmitProposalEventHandler) GetName() string {
	return "submit_proposal"
}

func (e *SubmitProposalEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events["submit_proposal.proposal_id"]) > 0
}

func (e *SubmitProposalEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	proposalIds := event.Result.Events["submit_proposal.proposal_id"]
	if len(proposalIds) == 0 {
		return errors.New("proposal_id not found in event")
	}
	logging.Debug("Handling `submit_proposal` event", types.EventProcessing, "proposalId", proposalIds[0])
	return nil
}

type TrainingTaskAssignedEventHandler struct{}

func (e *TrainingTaskAssignedEventHandler) GetName() string {
	return "training_task_assigned"
}

func (e *TrainingTaskAssignedEventHandler) CanHandle(event *chainevents.JSONRPCResponse) bool {
	return len(event.Result.Events["training_task_assigned.task_id"]) > 0
}

func (e *TrainingTaskAssignedEventHandler) Handle(event *chainevents.JSONRPCResponse, el *EventListener) error {
	if el.isNodeSynced() {
		for _, taskId := range event.Result.Events["training_task_assigned.task_id"] {
			taskIdUint, err := strconv.ParseUint(taskId, 10, 64)
			if err != nil {
				logging.Error("Failed to parse task ID", types.Training, "error", err)
				continue // Continue to the next task ID
			}
			el.trainingExecutor.ProcessTaskAssignedEvent(taskIdUint)
		}
	}
	return nil
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
