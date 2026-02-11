package event_listener

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/seed"
	"decentralized-api/internal/validation"
	"decentralized-api/logging"
	"decentralized-api/poc"

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
	pocOrchestrator      poc.Orchestrator
	queryClient          ChainStateClient
	phaseTracker         *chainphase.ChainPhaseTracker
	reconciliationConfig MlNodeReconciliationConfig
	getStatusFunc        StatusFunc
	setHeightFunc        SetHeightFunc
	randomSeedManager    seed.RandomSeedManager
	configManager        *apiconfig.ConfigManager
	validator            *validation.InferenceValidator
	epochGroupDataCache  *internal.EpochGroupDataCache
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
		BlockInterval: 5,
		TimeInterval:  30 * time.Second,
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
	pocOrchestrator poc.Orchestrator,
	queryClient ChainStateClient,
	phaseTracker *chainphase.ChainPhaseTracker,
	getStatusFunc StatusFunc,
	setHeightFunc SetHeightFunc,
	randomSeedManager seed.RandomSeedManager,
	reconciliationConfig MlNodeReconciliationConfig,
	configManager *apiconfig.ConfigManager,
	validator *validation.InferenceValidator,
) *OnNewBlockDispatcher {
	return &OnNewBlockDispatcher{
		nodeBroker:           nodeBroker,
		pocOrchestrator:      pocOrchestrator,
		queryClient:          queryClient,
		phaseTracker:         phaseTracker,
		reconciliationConfig: reconciliationConfig,
		getStatusFunc:        getStatusFunc,
		setHeightFunc:        setHeightFunc,
		randomSeedManager:    randomSeedManager,
		configManager:        configManager,
		validator:            validator,
	}
}

// NewOnNewBlockDispatcherFromCosmosClient creates a dispatcher using a full cosmos client
// This is a convenience constructor for existing code
func NewOnNewBlockDispatcherFromCosmosClient(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	pocOrchestrator poc.Orchestrator,
	cosmosClient cosmosclient.CosmosMessageClient,
	phaseTracker *chainphase.ChainPhaseTracker,
	reconciliationConfig MlNodeReconciliationConfig,
	validator *validation.InferenceValidator,
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

	randomSeedManager := seed.NewRandomSeedManager(cosmosClient, configManager)
	epochGroupDataCache := internal.NewEpochGroupDataCache(cosmosClient)

	dispatcher := NewOnNewBlockDispatcher(
		nodeBroker,
		pocOrchestrator,
		queryClient,
		phaseTracker,
		getStatusFunc,
		setHeightFunc,
		randomSeedManager,
		reconciliationConfig,
		configManager,
		validator,
	)
	dispatcher.epochGroupDataCache = epochGroupDataCache
	return dispatcher
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
				ExpirationBlocks:    params.Params.ValidationParams.ExpirationBlocks,
			}

			logging.Debug("Updating validation parameters", types.Validation,
				"timestampExpiration", validationParams.TimestampExpiration,
				"timestampAdvance", validationParams.TimestampAdvance,
				"expirationBlocks", validationParams.ExpirationBlocks)

			err = d.configManager.SetValidationParams(validationParams)
			if err != nil {
				logging.Warn("Failed to update validation parameters", types.Config, "error", err)
			}

			if params.Params.BandwidthLimitsParams != nil {
				bandwidthParams := apiconfig.BandwidthParamsCache{
					EstimatedLimitsPerBlockKb: params.Params.BandwidthLimitsParams.EstimatedLimitsPerBlockKb,
					KbPerInputToken:           params.Params.BandwidthLimitsParams.KbPerInputToken.ToFloat(),
					KbPerOutputToken:          params.Params.BandwidthLimitsParams.KbPerOutputToken.ToFloat(),
					MaxInferencesPerBlock:     params.Params.BandwidthLimitsParams.MaxInferencesPerBlock,
				}

				logging.Debug("Updated bandwidth parameters from chain", types.Config,
					"estimatedLimitsPerBlockKb", bandwidthParams.EstimatedLimitsPerBlockKb,
					"kbPerInputToken", bandwidthParams.KbPerInputToken,
					"kbPerOutputToken", bandwidthParams.KbPerOutputToken,
					"maxInferencesPerBlock", bandwidthParams.MaxInferencesPerBlock)

				err = d.configManager.SetBandwidthParams(bandwidthParams)
				if err != nil {
					logging.Warn("Failed to update bandwidth parameters", types.Config, "error", err)
				}
			}

			// Update Transfer Agent access cache from chain params
			if params.Params.TransferAgentAccessParams != nil {
				addresses := params.Params.TransferAgentAccessParams.AllowedTransferAddresses
				cache := apiconfig.TransferAgentAccessCache{
					AllowedAddresses: make(map[string]struct{}, len(addresses)),
					IsEnabled:        len(addresses) > 0,
				}
				for _, addr := range addresses {
					cache.AllowedAddresses[addr] = struct{}{}
				}
				d.configManager.SetTransferAgentAccessCache(cache)

				logging.Debug("Updated transfer agent access cache from chain", types.Config,
					"enabled", cache.IsEnabled, "count", len(addresses))
			}

			// Update PoC V2 enabled flags for runtime V1/V2 switching
			if params.Params.PocParams != nil {
				d.phaseTracker.UpdatePocV2Enabled(params.Params.PocParams.PocV2Enabled)
				d.phaseTracker.UpdateConfirmationPocV2Enabled(params.Params.PocParams.ConfirmationPocV2Enabled)
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
	d.phaseTracker.Update(blockInfo, &networkInfo.LatestEpoch, &networkInfo.EpochParams, networkInfo.IsSynced, networkInfo.ActiveConfirmationPoCEvent)
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
	EpochParams                types.EpochParams
	IsSynced                   bool
	LatestEpoch                types.Epoch
	BlockHeight                int64
	ActiveConfirmationPoCEvent *types.ConfirmationPoCEvent
}

// queryNetworkInfo queries the network for sync status and epoch parameters
func (d *OnNewBlockDispatcher) queryNetworkInfo(ctx context.Context) (NetworkInfo, error) {
	// Query sync status
	status, err := d.getStatusFunc()
	if err != nil {
		return NetworkInfo{}, err
	}
	isSynced := !status.SyncInfo.CatchingUp

	epochInfo, err := d.queryClient.EpochInfo(ctx, &types.QueryEpochInfoRequest{})
	if err != nil || epochInfo == nil {
		logging.Error("Failed to query epoch info", types.Stages, "error", err)
		return NetworkInfo{}, err
	}

	// Extract confirmation PoC event if active
	var confirmationEvent *types.ConfirmationPoCEvent
	if epochInfo.IsConfirmationPocActive && epochInfo.ActiveConfirmationPocEvent != nil {
		confirmationEvent = epochInfo.ActiveConfirmationPocEvent
	}

	return NetworkInfo{
		EpochParams:                *epochInfo.Params.EpochParams,
		IsSynced:                   isSynced,
		LatestEpoch:                epochInfo.LatestEpoch,
		BlockHeight:                epochInfo.BlockHeight,
		ActiveConfirmationPoCEvent: confirmationEvent,
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
		logging.Info("DapiStage:IsStartOfPocStage: sending StartPoCEvent to the PoC orchestrator", types.Stages, "blockHeight", blockHeight, "blockHash", blockHash)
		d.randomSeedManager.GenerateSeedInfo(epochContext.EpochIndex)
		return
	}

	// Check for PoC validation stage transitions
	if epochContext.IsEndOfPoCStage(blockHeight) {
		logging.Info("DapiStage:IsEndOfPoCStage. Calling MoveToValidationStage", types.Stages,
			"blockHeigh", blockHeight, "blockHash", blockHash)
		command := broker.NewInitValidateCommand()
		err := d.nodeBroker.QueueMessage(command)
		if err != nil {
			logging.Error("Failed to send init validate command", types.PoC, "error", err)
			return
		}
	}

	if epochContext.IsStartOfPoCValidationStage(blockHeight) {
		logging.Info("DapiStage:IsStartOfPoCValidationStage", types.Stages, "blockHeight", blockHeight, "blockHash", blockHash, "pocStartBlockHeight", epochContext.PocStartBlockHeight)
		pocStartBlockHeight := epochContext.PocStartBlockHeight
		go func() {
			pocStartBlockHash, err := d.nodeBroker.GetChainBridge().GetBlockHash(pocStartBlockHeight)
			if err != nil {
				logging.Error("Failed to get PoC start block hash", types.PoC,
					"pocStartBlockHeight", pocStartBlockHeight, "error", err)
				return
			}
			d.pocOrchestrator.ValidateReceivedArtifacts(pocStartBlockHeight, pocStartBlockHash)
		}()
	}

	if epochContext.IsEndOfPoCValidationStage(blockHeight) {
		logging.Info("DapiStage:IsEndOfPoCValidationStage", types.Stages, "blockHeight", blockHeight, "blockHash", blockHash)
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
		logging.Info("DapiStage:IsSetNewValidatorsStage", types.Stages, "blockHeight", blockHeight, "blockHash", blockHash)
		go func() {
			d.randomSeedManager.ChangeCurrentSeed()
		}()
	}

	// Compute a deterministic number in [1, 500] based on participant address
	randomDelay := 0
	participantAddress := d.nodeBroker.GetParticipantAddress()
	if blockHeight > 500 && participantAddress != "" {
		hash := sha256.Sum256([]byte(participantAddress))
		randomDelay = int(binary.BigEndian.Uint64(hash[:8])%500) + 1

		// Cap the delay to not exceed half of the gap until the next PoC start
		claimHeight := epochContext.ClaimMoney()
		nextPoCStart := epochContext.NextPoCStart()
		if nextPoCStart-claimHeight < 1000 {
			randomDelay = 0
		}
	}

	if epochContext.IsClaimMoneyStage(blockHeight - int64(randomDelay)) {
		logging.Info("DapiStage:IsClaimMoneyStage", types.Stages, "blockHeight", blockHeight, "blockHash", blockHash)

		// Calculate previous epoch index
		expectedPreviousEpochIndex := epochContext.EpochIndex - 1
		// Get the previous epoch seed for validation recovery
		previousSeed := d.randomSeedManager.GetSeedForEpoch(expectedPreviousEpochIndex)

		// Verify the seed is from the correct epoch
		if previousSeed.EpochIndex != expectedPreviousEpochIndex {
			logging.Warn("Previous seed epoch mismatch for recovery", types.Validation,
				"previousSeedEpoch", previousSeed.EpochIndex,
				"expectedPreviousEpoch", expectedPreviousEpochIndex,
				"currentEpoch", epochContext.EpochIndex)
		}

		// Execute missed validation recovery BEFORE claiming rewards
		go func() {
			// First, recover any missed validations from the previous epoch
			d.executeMissedValidationRecoveryWithSeed(expectedPreviousEpochIndex, previousSeed)

			// Then, claim rewards (this ensures we've validated everything before claiming)
			d.randomSeedManager.RequestMoney(expectedPreviousEpochIndex)

			// Mark the seed as claimed to prevent duplicate claims
			err := d.configManager.MarkPreviousSeedClaimed()
			if err != nil {
				logging.Error("Failed to mark seed as claimed", types.Claims, "epochIndex", expectedPreviousEpochIndex, "error", err)
			}
		}()
	}

	// Confirmation PoC transitions (during inference phase)
	if epochState.CurrentPhase == types.InferencePhase && epochState.ActiveConfirmationPoCEvent != nil {
		// Skip confirmation PoC if not an active participant
		selfAddress := d.nodeBroker.GetParticipantAddress()
		if isActive, _ := d.epochGroupDataCache.IsActiveParticipant(context.Background(), epochState.LatestEpoch.EpochIndex, selfAddress); !isActive {
			logging.Debug("Skipping confirmation PoC - not active participant", types.PoC, "address", selfAddress)
			return
		}

		event := epochState.ActiveConfirmationPoCEvent
		epochParams := &epochState.LatestEpoch.EpochParams

		// Start generation
		if event.ShouldStartGeneration(blockHeight) {
			logging.Info("Confirmation PoC generation starting", types.PoC,
				"trigger_height", event.TriggerHeight,
				"block_hash", event.PocSeedBlockHash)

			command := broker.NewStartPocCommand()
			if err := d.nodeBroker.QueueMessage(command); err != nil {
				logging.Error("Failed to send confirmation PoC start command", types.PoC, "error", err)
			}
		}

		// End of exchange period - initiate validation transition
		if event.ShouldInitValidation(blockHeight, epochParams) {
			logging.Info("Confirmation PoC: initiating validation transition", types.PoC,
				"trigger_height", event.TriggerHeight,
				"exchange_end", event.GetExchangeEnd(epochParams),
				"validation_starts_at", event.GetValidationStart(epochParams))

			command := broker.NewInitValidateCommand()
			if err := d.nodeBroker.QueueMessage(command); err != nil {
				logging.Error("Failed to send confirmation PoC validate command", types.PoC, "error", err)
			}
		}

		// Start validation (now has proper gap from InitValidateCommand)
		if event.ShouldStartValidation(blockHeight, epochParams) {
			logging.Info("Confirmation PoC validation starting", types.PoC,
				"trigger_height", event.TriggerHeight,
				"poc_seed_block_hash", event.PocSeedBlockHash)

			go func() {
				d.pocOrchestrator.ValidateReceivedArtifacts(event.TriggerHeight, event.PocSeedBlockHash)
			}()
		}

		// End of event - return to inference
		if event.ShouldReturnToInference(blockHeight, epochParams) {
			logging.Info("Confirmation PoC completed", types.PoC,
				"trigger_height", event.TriggerHeight)

			command := broker.NewInferenceUpAllCommand()
			if err := d.nodeBroker.QueueMessage(command); err != nil {
				logging.Error("Failed to send inference up command", types.PoC, "error", err)
			}
		}
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
	// Handle confirmation PoC during inference phase
	if phaseInfo.CurrentPhase == types.InferencePhase && phaseInfo.ActiveConfirmationPoCEvent != nil {
		event := phaseInfo.ActiveConfirmationPoCEvent

		switch event.Phase {
		case types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION:
			cmd := broker.NewStartPocCommand()
			return cmd, &cmd.Response
		case types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION:
			cmd := broker.NewInitValidateCommand()
			return cmd, &cmd.Response
		}
		// GRACE_PERIOD or COMPLETED - return to inference
		cmd := broker.NewInferenceUpAllCommand()
		return cmd, &cmd.Response
	}

	// Regular phase commands
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

// executeMissedValidationRecoveryWithSeed performs missed validation recovery for the previous epoch
// This function runs during the Set New Validators stage to recover any missed validations
// It accepts the seed as a parameter to avoid race conditions with ChangeCurrentSeed()
func (d *OnNewBlockDispatcher) executeMissedValidationRecoveryWithSeed(previousEpochIndex uint64, previousSeed apiconfig.SeedInfo) {
	if d.validator == nil {
		logging.Warn("Missed validation recovery skipped: validator not available", types.ValidationRecovery)
		return
	}

	// Check for genesis epoch
	if previousEpochIndex == 0 && previousSeed.EpochIndex == 0 {
		logging.Info("Missed validation recovery skipped: genesis epoch", types.ValidationRecovery, "previousEpochIndex", previousEpochIndex)
		return
	}

	// Check if seed is valid
	if previousSeed.Seed == 0 {
		logging.Warn("Empty seed, try to reproduce", types.ValidationRecovery,
			"previousEpochIndex", previousEpochIndex,
			"seedEpochIndex", previousSeed.EpochIndex)
		regeneratedSeed, err := d.randomSeedManager.CreateNewSeed(previousSeed.EpochIndex)
		if err != nil {
			logging.Error("Error regenerating seed", types.ValidationRecovery,
				"err", err,
				"previousEpochIndex", previousEpochIndex,
				"seedEpochIndex", previousSeed.EpochIndex)
			return
		}
		previousSeed.Seed = regeneratedSeed.Seed
	}

	// Verify seed epoch matches (this should always be true now, but good to verify)
	if previousSeed.EpochIndex != previousEpochIndex {
		logging.Warn("Missed validation recovery skipped: seed epoch mismatch", types.ValidationRecovery,
			"previousEpochIndex", previousEpochIndex,
			"seedEpochIndex", previousSeed.EpochIndex)
		return
	}

	logging.Info("Starting missed validation recovery", types.ValidationRecovery,
		"previousEpochIndex", previousEpochIndex,
		"seed", previousSeed.Seed)

	// Detect missed validations for the previous epoch
	missedInferences, err := d.validator.DetectMissedValidations(previousEpochIndex, previousSeed.Seed)
	if err != nil {
		logging.Error("Failed to detect missed validations", types.ValidationRecovery,
			"previousEpochIndex", previousEpochIndex,
			"error", err)
		return
	}

	if len(missedInferences) == 0 {
		logging.Info("No missed validations found for recovery", types.ValidationRecovery, "previousEpochIndex", previousEpochIndex)
		return
	}

	logging.Info("Found missed validations, executing recovery", types.ValidationRecovery,
		"previousEpochIndex", previousEpochIndex,
		"missedCount", len(missedInferences))

	// Execute recovery validations
	recoveredCount, err := d.validator.ExecuteRecoveryValidations(missedInferences)
	if err != nil {
		logging.Warn("Failed to execute recovery validations", types.ValidationRecovery, "error", err)
	}

	if recoveredCount > 0 {
		logging.Info("Recovered validations", types.ValidationRecovery, "recoveredCount", recoveredCount)
		d.validator.WaitForValidationsToBeRecorded()
	}

	logging.Info("Missed validation recovery completed", types.ValidationRecovery,
		"previousEpochIndex", previousEpochIndex,
		"recoveredCount", len(missedInferences))
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
