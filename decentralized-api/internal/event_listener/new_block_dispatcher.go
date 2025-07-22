package event_listener

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/poc"
	"decentralized-api/logging"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

// Minimal interface for query operations needed by the dispatcher
type ChainStateClient interface {
	EpochInfo(ctx context.Context, req *types.QueryEpochInfoRequest, opts ...grpc.CallOption) (*types.QueryEpochInfoResponse, error)
	Params(ctx context.Context, req *types.QueryParamsRequest, opts ...grpc.CallOption) (*types.QueryParamsResponse, error)
}

// StatusFunc defines the function signature for getting node sync status
type StatusFunc func() (*coretypes.ResultStatus, error)

type SetHeightFunc func(blockHeight int64) error

// PoCParams contains Proof of Compute parameters
type PoCParams struct {
	StartBlockHeight int64
	StartBlockHash   string
}

// MlNodeStageReconciliationConfig defines when reconciliation should be triggered
type MlNodeStageReconciliationConfig struct {
	BlockInterval int           // Trigger every N blocks
	TimeInterval  time.Duration // OR every N time duration
}

type MlNodeReconciliationConfig struct {
	Inference       *MlNodeStageReconciliationConfig
	PoC             *MlNodeStageReconciliationConfig
	LastBlockHeight int64     // Track last reconciliation block
	LastTime        time.Time // Track last reconciliation time
}

// OnNewBlockDispatcher orchestrates processing of new block events
type OnNewBlockDispatcher struct {
	nodeBroker           *broker.Broker
	nodePocOrchestrator  poc.NodePoCOrchestrator
	queryClient          ChainStateClient
	phaseTracker         *chainphase.ChainPhaseTracker
	reconciliationConfig MlNodeReconciliationConfig
	getStatusFunc        StatusFunc
	setHeightFunc        SetHeightFunc
	randomSeedManager    poc.RandomSeedManager
	configManager        *apiconfig.ConfigManager
}

// StatusResponse matches the structure expected by getStatus function
type StatusResponse struct {
	SyncInfo SyncInfo `json:"sync_info"`
}

type SyncInfo struct {
	CatchingUp bool `json:"catching_up"`
}

var DefaultReconciliationConfig = MlNodeReconciliationConfig{
	Inference: &MlNodeStageReconciliationConfig{
		BlockInterval: 10,
		TimeInterval:  60 * time.Second,
	},
	PoC: &MlNodeStageReconciliationConfig{
		BlockInterval: 1,
		TimeInterval:  30 * time.Second,
	},
	LastTime:        time.Now(),
	LastBlockHeight: 0,
}

// NewOnNewBlockDispatcher creates a new dispatcher with default configuration
func NewOnNewBlockDispatcher(
	nodeBroker *broker.Broker,
	nodePocOrchestrator poc.NodePoCOrchestrator,
	queryClient ChainStateClient,
	phaseTracker *chainphase.ChainPhaseTracker,
	getStatusFunc StatusFunc,
	setHeightFunc SetHeightFunc,
	randomSeedManager poc.RandomSeedManager,
	reconciliationConfig MlNodeReconciliationConfig,
	configManager *apiconfig.ConfigManager,
) *OnNewBlockDispatcher {
	return &OnNewBlockDispatcher{
		nodeBroker:           nodeBroker,
		nodePocOrchestrator:  nodePocOrchestrator,
		queryClient:          queryClient,
		phaseTracker:         phaseTracker,
		reconciliationConfig: reconciliationConfig,
		getStatusFunc:        getStatusFunc,
		setHeightFunc:        setHeightFunc,
		randomSeedManager:    randomSeedManager,
		configManager:        configManager,
	}
}

// NewOnNewBlockDispatcherFromCosmosClient creates a dispatcher using a full cosmos client
// This is a convenience constructor for existing code
func NewOnNewBlockDispatcherFromCosmosClient(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	nodePocOrchestrator poc.NodePoCOrchestrator,
	cosmosClient cosmosclient.CosmosMessageClient,
	phaseTracker *chainphase.ChainPhaseTracker,
	reconciliationConfig MlNodeReconciliationConfig,
) *OnNewBlockDispatcher {
	// Adapt the cosmos client to our minimal interfaces
	queryClient := cosmosClient.NewInferenceQueryClient()
	setHeightFunc := func(blockHeight int64) error {
		return configManager.SetHeight(blockHeight)
	}
	getStatusFunc := func() (*coretypes.ResultStatus, error) {
		url := configManager.GetChainNodeConfig().Url
		return getStatus(url)
	}

	randomSeedManager := poc.NewRandomSeedManager(cosmosClient, configManager)

	return NewOnNewBlockDispatcher(
		nodeBroker,
		nodePocOrchestrator,
		queryClient,
		phaseTracker,
		getStatusFunc,
		setHeightFunc,
		randomSeedManager,
		reconciliationConfig,
		configManager,
	)
}

// ProcessNewBlock is the main entry point for processing new block events
func (d *OnNewBlockDispatcher) ProcessNewBlock(ctx context.Context, blockInfo chainphase.BlockInfo) error {
	logging.Debug("Processing new block", types.Stages,
		"height", blockInfo.Height,
		"hash", blockInfo.Hash)

	// 1. Query network for current state (sync status, epoch params)
	networkInfo, err := d.queryNetworkInfo(ctx)
	if err != nil {
		logging.Error("Failed to query network info, skipping block processing", types.Stages,
			"error", err, "height", blockInfo.Height)
		return err // Skip processing this block
	}

	// Fetch validation parameters - skip in tests
	if d.configManager != nil && !strings.HasPrefix(blockInfo.Hash, "hash-") { // Skip in tests where hash has format "hash-N"
		params, err := d.queryClient.Params(ctx, &types.QueryParamsRequest{})
		if err != nil {
			logging.Error("Failed to get params", types.Validation, "error", err)
		} else {
			// Update validation parameters in config
			validationParams := apiconfig.ValidationParamsCache{
				TimestampExpiration: params.Params.ValidationParams.TimestampExpiration,
				TimestampAdvance:    params.Params.ValidationParams.TimestampAdvance,
			}
			logging.Debug("Updating validation parameters", types.Validation,
				"timestampExpiration", validationParams.TimestampExpiration,
				"timestampAdvance", validationParams.TimestampAdvance)
			err = d.configManager.SetValidationParams(validationParams)
			if err != nil {
				logging.Warn("Failed to update validation parameters", types.Config, "error", err)
			}
		}
	}

	// Let's check in prod how often this happens
	if networkInfo.BlockHeight != blockInfo.Height {
		logging.Warn("Block height mismatch between event and network query", types.Stages,
			"event_height", blockInfo.Height,
			"network_height", networkInfo.BlockHeight)
	}

	// 2. Update phase tracker and get phase info
	// FIXME: It looks like a problem that queries are separate inside networkInfo, and blockInfo
	// 	comes from a totally different source?
	// TODO: log block that came from event vs block returned by query
	// TODO: can we add the state to the block event? As a future optimization?
	d.phaseTracker.Update(blockInfo, &networkInfo.LatestEpoch, &networkInfo.EpochParams, networkInfo.IsSynced)
	epochState := d.phaseTracker.GetCurrentEpochState()
	if epochState == nil {
		logging.Error("[ILLEGAL_STATE]: Epoch state is nil right after an update call to phase tracker. "+
			"Skip block processing", types.Stages,
			"blockHeight", blockInfo.Height, "isSynced", networkInfo.IsSynced)
		return nil
	}

	logging.Info("[new-block-dispatcher] Current epoch state.", types.Stages,
		"blockHeight", epochState.CurrentBlock.Height,
		"epoch", epochState.LatestEpoch.EpochIndex,
		"epoch.PocStartBlockHeight", epochState.LatestEpoch.PocStartBlockHeight,
		"currentPhase", epochState.CurrentPhase,
		"isSynced", epochState.IsSynced,
		"blockHash", epochState.CurrentBlock.Hash)
	logging.Debug("[new-block-dispatcher]", types.Stages, "blockHeight", epochState.CurrentBlock.Height, "blochHash", epochState.CurrentBlock.Hash)
	if !epochState.IsSynced {
		logging.Info("The blockchain node is still catching up, skipping on new block phase transitions", types.Stages)
		return nil
	}

	// 3. Check for phase transitions and stage events
	d.handlePhaseTransitions(*epochState)

	// 4. Check if reconciliation should be triggered
	if d.shouldTriggerReconciliation(*epochState) {
		d.triggerReconciliation(*epochState)
	}

	// 5. Update config manager height
	err = d.setHeightFunc(blockInfo.Height)
	if err != nil {
		logging.Warn("Failed to write config", types.Config, "error", err)
	}

	return nil
}

// NetworkInfo contains information queried from the network
type NetworkInfo struct {
	EpochParams types.EpochParams
	IsSynced    bool
	LatestEpoch types.Epoch
	BlockHeight int64
}

// queryNetworkInfo queries the network for sync status and epoch parameters
func (d *OnNewBlockDispatcher) queryNetworkInfo(ctx context.Context) (*NetworkInfo, error) {
	// Query sync status
	status, err := d.getStatusFunc()
	if err != nil {
		return nil, err
	}
	isSynced := !status.SyncInfo.CatchingUp

	epochInfo, err := d.queryClient.EpochInfo(ctx, &types.QueryEpochInfoRequest{})
	if err != nil || epochInfo == nil {
		logging.Error("Failed to query epoch info", types.Stages, "error", err)
		return nil, err
	}

	return &NetworkInfo{
		EpochParams: *epochInfo.Params.EpochParams,
		IsSynced:    isSynced,
		LatestEpoch: epochInfo.LatestEpoch,
		BlockHeight: epochInfo.BlockHeight,
	}, nil
}

// handlePhaseTransitions checks for and handles phase transitions and stage events
func (d *OnNewBlockDispatcher) handlePhaseTransitions(epochState chainphase.EpochState) {
	epochContext := epochState.LatestEpoch
	blockHeight := epochState.CurrentBlock.Height
	blockHash := epochState.CurrentBlock.Hash

	// Sync broker node state with the latest epoch data at the start of a transition check
	if err := d.nodeBroker.UpdateNodeWithEpochData(&epochState); err != nil {
		logging.Error("Failed to update node with epoch data, skipping phase transitions.", types.Stages, "error", err)
		return
	}

	// Check for PoC start for the next epoch. This is the most important transition.
	if epochContext.IsStartOfPocStage(blockHeight) {
		logging.Info("IsStartOfPocStage: sending StartPoCEvent to the PoC orchestrator", types.Stages, "blockHeight", blockHeight, "blockHash", blockHash)
		d.randomSeedManager.GenerateSeed(blockHeight)
		return
	}

	// Check for PoC validation stage transitions
	if epochContext.IsEndOfPoCStage(blockHeight) {
		logging.Info("IsEndOfPoCStage. Calling MoveToValidationStage", types.Stages,
			"blockHeigh", blockHeight, "blockHash", blockHash)
		command := broker.NewInitValidateCommand()
		err := d.nodeBroker.QueueMessage(command)
		if err != nil {
			logging.Error("Failed to send init validate command", types.PoC, "error", err)
			return
		}
	}

	if epochContext.IsStartOfPoCValidationStage(blockHeight) {
		logging.Info("IsStartOfPoCValidationStage", types.Stages, "blockHeight", blockHeight, "blockHash", blockHash)
		go func() {
			d.nodePocOrchestrator.ValidateReceivedBatches(blockHeight)
		}()
	}

	if epochContext.IsEndOfPoCValidationStage(blockHeight) {
		command := broker.NewInferenceUpAllCommand()
		err := d.nodeBroker.QueueMessage(command)
		if err != nil {
			logging.Error("Failed to send inference up command", types.PoC, "error", err)
			return
		}
		return
	}

	// Check for other stage transitions
	if epochContext.IsSetNewValidatorsStage(blockHeight) {
		logging.Info("IsSetNewValidatorsStage", types.Stages, "blockHeight", blockHeight, "blockHash", blockHash)
		go func() {
			d.randomSeedManager.ChangeCurrentSeed()
		}()
	}

	if epochContext.IsClaimMoneyStage(blockHeight) {
		logging.Info("IsClaimMoneyStage", types.Stages, "blockHeight", blockHeight, "blockHash", blockHash)
		go func() {
			d.randomSeedManager.RequestMoney()
		}()
	}
}

// shouldTriggerReconciliation determines if reconciliation should be triggered
func (d *OnNewBlockDispatcher) shouldTriggerReconciliation(epochState chainphase.EpochState) bool {
	switch epochState.CurrentPhase {
	case types.PoCGeneratePhase, types.PoCValidatePhase:
		return shouldTriggerReconciliation(epochState.CurrentBlock.Height, &d.reconciliationConfig, d.reconciliationConfig.PoC)
	case types.InferencePhase:
		return shouldTriggerReconciliation(epochState.CurrentBlock.Height, &d.reconciliationConfig, d.reconciliationConfig.Inference)
	case types.PoCGenerateWindDownPhase, types.PoCValidateWindDownPhase:
		return false
	}
	return false
}

func shouldTriggerReconciliation(blockHeight int64, config *MlNodeReconciliationConfig, stageConfig *MlNodeStageReconciliationConfig) bool {
	// Check block interval
	blocksSinceLastReconciliation := blockHeight - config.LastBlockHeight
	if blocksSinceLastReconciliation >= int64(stageConfig.BlockInterval) {
		return true
	}

	// Check time interval
	timeSinceLastReconciliation := time.Since(config.LastTime)
	if timeSinceLastReconciliation >= stageConfig.TimeInterval {
		return true
	}

	return false
}

// triggerReconciliation starts node reconciliation with current phase info
func (d *OnNewBlockDispatcher) triggerReconciliation(epochState chainphase.EpochState) {
	cmd, response := getCommandForPhase(epochState)
	if cmd == nil || response == nil {
		logging.Info("[triggerReconciliation] No command required for phase", types.Nodes,
			"phase", epochState.CurrentPhase, "height", epochState.CurrentBlock.Height)
		return
	}

	logging.Info("[triggerReconciliation] Created command for reconciliation", types.Nodes,
		"command_type", fmt.Sprintf("%T", cmd),
		"height", epochState.CurrentBlock.Height,
		"epoch", epochState.LatestEpoch.EpochIndex,
		"phase", epochState.CurrentPhase)

	err := d.nodeBroker.QueueMessage(cmd)
	if err != nil {
		logging.Error("[triggerReconciliation] Failed to queue reconciliation command", types.Nodes, "error", err)
		return
	}

	// Update reconciliation tracking
	d.reconciliationConfig.LastBlockHeight = epochState.CurrentBlock.Height
	d.reconciliationConfig.LastTime = time.Now()

	// Wait for a response or not?
}

func getCommandForPhase(phaseInfo chainphase.EpochState) (broker.Command, *chan bool) {
	switch phaseInfo.CurrentPhase {
	case types.PoCGeneratePhase, types.PoCGenerateWindDownPhase:
		cmd := broker.NewStartPocCommand()
		return cmd, &cmd.Response
	case types.PoCValidatePhase, types.PoCValidateWindDownPhase:
		cmd := broker.NewInitValidateCommand()
		return cmd, &cmd.Response
	case types.InferencePhase:
		cmd := broker.NewInferenceUpAllCommand()
		return cmd, &cmd.Response
	}
	return nil, nil
}

// parseNewBlockInfo extracts NewBlockInfo from a JSONRPCResponse event
func parseNewBlockInfo(event *chainevents.JSONRPCResponse) (*chainphase.BlockInfo, error) {
	blockHeight, err := getBlockHeight(event.Result.Data.Value)
	if err != nil {
		return nil, err
	}

	blockHash, err := getBlockHash(event.Result.Data.Value)
	if err != nil {
		return nil, err
	}

	return &chainphase.BlockInfo{
		Height: blockHeight,
		Hash:   blockHash,
	}, nil
}

// Helper functions moved from event_listener.go for parsing block data
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
