package event_listener

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/rand"
	"strconv"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener/chainevents"
	"decentralized-api/internal/poc"
	"decentralized-api/logging"

	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

// Minimal interface for query operations needed by the dispatcher
type QueryClient interface {
	Params(ctx context.Context, req *types.QueryParamsRequest, opts ...grpc.CallOption) (*types.QueryParamsResponse, error)
}

// Minimal interface for transaction operations needed by the dispatcher
type TransactionClient interface {
	SignBytes(data []byte) ([]byte, error)
	SubmitSeed(msg *inference.MsgSubmitSeed) error
	ClaimRewards(msg *inference.MsgClaimRewards) error
}

// StatusFunc defines the function signature for getting node sync status
type StatusFunc func(chainNodeUrl string) (*coretypes.ResultStatus, error)

// NewBlockInfo contains parsed information from a new block event
type NewBlockInfo struct {
	Height    int64
	Hash      string
	Timestamp time.Time
}

// PhaseInfo contains complete phase and epoch information for a given block
type PhaseInfo struct {
	CurrentEpoch  uint64
	CurrentPhase  chainphase.Phase
	BlockHeight   int64
	BlockHash     string
	PoCParameters *PoCParams
	EpochParams   *types.EpochParams
	IsSynced      bool
}

// PoCParams contains Proof of Compute parameters
type PoCParams struct {
	StartBlockHeight int64
	StartBlockHash   string
}

// ReconciliationConfig defines when reconciliation should be triggered
type ReconciliationConfig struct {
	BlockInterval   int           // Trigger every N blocks
	TimeInterval    time.Duration // OR every N time duration
	LastBlockHeight int64         // Track last reconciliation block
	LastTime        time.Time     // Track last reconciliation time
}

// OnNewBlockDispatcher orchestrates processing of new block events
type OnNewBlockDispatcher struct {
	nodeBroker           *broker.Broker
	configManager        *apiconfig.ConfigManager
	nodePocOrchestrator  *poc.NodePoCOrchestrator
	queryClient          QueryClient
	transactionClient    TransactionClient
	phaseTracker         *chainphase.ChainPhaseTracker
	reconciliationConfig ReconciliationConfig
	getStatusFunc        StatusFunc
}

// StatusResponse matches the structure expected by getStatus function
type StatusResponse struct {
	SyncInfo SyncInfo `json:"sync_info"`
}

type SyncInfo struct {
	CatchingUp bool `json:"catching_up"`
}

// NewOnNewBlockDispatcher creates a new dispatcher with default configuration
func NewOnNewBlockDispatcher(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	nodePocOrchestrator *poc.NodePoCOrchestrator,
	queryClient QueryClient,
	transactionClient TransactionClient,
	phaseTracker *chainphase.ChainPhaseTracker,
	getStatusFunc StatusFunc,
) *OnNewBlockDispatcher {
	return &OnNewBlockDispatcher{
		nodeBroker:          nodeBroker,
		configManager:       configManager,
		nodePocOrchestrator: nodePocOrchestrator,
		queryClient:         queryClient,
		transactionClient:   transactionClient,
		phaseTracker:        phaseTracker,
		reconciliationConfig: ReconciliationConfig{
			BlockInterval: 5,                // Every 5 blocks
			TimeInterval:  30 * time.Second, // OR every 30 seconds
			LastTime:      time.Now(),
		},
		getStatusFunc: getStatusFunc,
	}
}

// NewOnNewBlockDispatcherFromCosmosClient creates a dispatcher using a full cosmos client
// This is a convenience constructor for existing code
func NewOnNewBlockDispatcherFromCosmosClient(
	nodeBroker *broker.Broker,
	configManager *apiconfig.ConfigManager,
	nodePocOrchestrator *poc.NodePoCOrchestrator,
	cosmosClient cosmosclient.InferenceCosmosClient,
	phaseTracker *chainphase.ChainPhaseTracker,
) *OnNewBlockDispatcher {
	// Adapt the cosmos client to our minimal interfaces
	queryClient := cosmosClient.NewInferenceQueryClient()
	transactionClient := &cosmosClientAdapter{cosmosClient: cosmosClient}

	return NewOnNewBlockDispatcher(
		nodeBroker,
		configManager,
		nodePocOrchestrator,
		queryClient,
		transactionClient,
		phaseTracker,
		getStatus,
	)
}

// cosmosClientAdapter adapts the full cosmos client to our TransactionClient interface
type cosmosClientAdapter struct {
	cosmosClient cosmosclient.InferenceCosmosClient
}

func (c *cosmosClientAdapter) SignBytes(data []byte) ([]byte, error) {
	return c.cosmosClient.SignBytes(data)
}

func (c *cosmosClientAdapter) SubmitSeed(msg *inference.MsgSubmitSeed) error {
	return c.cosmosClient.SubmitSeed(msg)
}

func (c *cosmosClientAdapter) ClaimRewards(msg *inference.MsgClaimRewards) error {
	return c.cosmosClient.ClaimRewards(msg)
}

// ProcessNewBlock is the main entry point for processing new block events
func (d *OnNewBlockDispatcher) ProcessNewBlock(ctx context.Context, blockInfo NewBlockInfo) error {
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

	// 2. Update phase tracker (pure functions) and get phase info
	phaseInfo := d.updatePhaseAndGetInfo(blockInfo, networkInfo)

	// 3. Check for phase transitions and stage events
	d.handlePhaseTransitions(phaseInfo)

	// 4. Check if reconciliation should be triggered
	if d.shouldTriggerReconciliation(phaseInfo) {
		d.triggerReconciliation(phaseInfo)
	}

	// 5. Update config manager height
	err = d.configManager.SetHeight(blockInfo.Height)
	if err != nil {
		logging.Warn("Failed to write config", types.Config, "error", err)
	}

	return nil
}

// NetworkInfo contains information queried from the network
type NetworkInfo struct {
	EpochParams *types.EpochParams
	IsSynced    bool
}

// queryNetworkInfo queries the network for sync status and epoch parameters
func (d *OnNewBlockDispatcher) queryNetworkInfo(ctx context.Context) (*NetworkInfo, error) {
	// Query sync status
	chainNodeUrl := d.configManager.GetChainNodeConfig().Url
	status, err := d.getStatusFunc(chainNodeUrl)
	if err != nil {
		return nil, err
	}
	isSynced := !status.SyncInfo.CatchingUp

	// Query epoch parameters using our minimal interface
	params, err := d.queryClient.Params(ctx, &types.QueryParamsRequest{})
	if err != nil {
		return nil, err
	}

	return &NetworkInfo{
		EpochParams: params.Params.EpochParams,
		IsSynced:    isSynced,
	}, nil
}

// updatePhaseAndGetInfo updates the phase tracker and returns complete phase info
func (d *OnNewBlockDispatcher) updatePhaseAndGetInfo(blockInfo NewBlockInfo, networkInfo *NetworkInfo) *PhaseInfo {
	// Update phase tracker with pure functions
	d.phaseTracker.UpdateEpochParams(*networkInfo.EpochParams)
	d.phaseTracker.UpdateBlockHeight(blockInfo.Height, blockInfo.Hash)
	d.phaseTracker.SetSyncStatus(networkInfo.IsSynced)

	// Get current phase and epoch
	currentPhase, _ := d.phaseTracker.GetCurrentPhase()
	currentEpoch := d.phaseTracker.GetCurrentEpoch()

	// Get PoC parameters if available
	var pocParams *PoCParams
	if pocHeight, pocHash, isInPoC := d.phaseTracker.GetPoCParameters(); isInPoC {
		pocParams = &PoCParams{
			StartBlockHeight: pocHeight,
			StartBlockHash:   pocHash,
		}
	}

	return &PhaseInfo{
		CurrentEpoch:  currentEpoch,
		CurrentPhase:  currentPhase,
		BlockHeight:   blockInfo.Height,
		BlockHash:     blockInfo.Hash,
		PoCParameters: pocParams,
		EpochParams:   networkInfo.EpochParams,
		IsSynced:      networkInfo.IsSynced,
	}
}

// handlePhaseTransitions checks for and handles phase transitions and stage events
func (d *OnNewBlockDispatcher) handlePhaseTransitions(phaseInfo *PhaseInfo) {
	if phaseInfo.EpochParams == nil {
		return
	}

	epochParams := phaseInfo.EpochParams
	blockHeight := phaseInfo.BlockHeight
	blockHash := phaseInfo.BlockHash

	// Check for PoC start
	if epochParams.IsStartOfPoCStage(blockHeight) {
		logging.Info("IsStartOfPocStage: sending StartPoCEvent to the PoC orchestrator", types.Stages)
		d.startPoC(blockHeight, blockHash, phaseInfo.CurrentEpoch, phaseInfo.CurrentPhase)
		d.generateSeed(blockHeight)
		return
	}

	// Check for PoC validation stage transitions
	if epochParams.IsEndOfPoCStage(blockHeight) {
		logging.Info("IsEndOfPoCStage. Calling MoveToValidationStage", types.Stages)
		d.nodePocOrchestrator.MoveToValidationStage(blockHeight)
	}

	if epochParams.IsStartOfPoCValidationStage(blockHeight) {
		logging.Info("IsStartOfPoCValidationStage", types.Stages)
		go func() {
			d.nodePocOrchestrator.ValidateReceivedBatches(blockHeight)
		}()
	}

	if epochParams.IsEndOfPoCValidationStage(blockHeight) {
		logging.Info("IsEndOfPoCValidationStage", types.Stages)
		d.nodePocOrchestrator.StopPoC()
		return
	}

	// Check for other stage transitions
	if epochParams.IsSetNewValidatorsStage(blockHeight) {
		logging.Info("IsSetNewValidatorsStage", types.Stages)
		go func() {
			d.changeCurrentSeed()
		}()
	}

	if epochParams.IsClaimMoneyStage(blockHeight) {
		logging.Info("IsClaimMoneyStage", types.Stages)
		go func() {
			d.requestMoney()
		}()
	}
}

// shouldTriggerReconciliation determines if reconciliation should be triggered
func (d *OnNewBlockDispatcher) shouldTriggerReconciliation(phaseInfo *PhaseInfo) bool {
	// Check block interval
	blocksSinceLastReconciliation := phaseInfo.BlockHeight - d.reconciliationConfig.LastBlockHeight
	if blocksSinceLastReconciliation >= int64(d.reconciliationConfig.BlockInterval) {
		return true
	}

	// Check time interval
	timeSinceLastReconciliation := time.Since(d.reconciliationConfig.LastTime)
	if timeSinceLastReconciliation >= d.reconciliationConfig.TimeInterval {
		return true
	}

	return false
}

// triggerReconciliation starts node reconciliation with current phase info
func (d *OnNewBlockDispatcher) triggerReconciliation(phaseInfo *PhaseInfo) {
	logging.Info("Triggering reconciliation", types.Nodes,
		"height", phaseInfo.BlockHeight,
		"epoch", phaseInfo.CurrentEpoch,
		"phase", phaseInfo.CurrentPhase.String())

	// Create reconciliation command with current phase info
	response := make(chan bool, 1)
	err := d.nodeBroker.QueueMessage(broker.ReconcileNodesCommand{
		Response: response,
	})

	if err != nil {
		logging.Error("Failed to queue reconciliation command", types.Nodes, "error", err)
		return
	}

	// Update reconciliation tracking
	d.reconciliationConfig.LastBlockHeight = phaseInfo.BlockHeight
	d.reconciliationConfig.LastTime = time.Now()

	// Note: We don't wait for the response to avoid blocking block processing
}

// Helper functions for stage transitions

func (d *OnNewBlockDispatcher) startPoC(blockHeight int64, blockHash string, currentEpoch uint64, currentPhase chainphase.Phase) {
	d.nodePocOrchestrator.StartPoC(blockHeight, blockHash, currentEpoch, currentPhase)
}

func (d *OnNewBlockDispatcher) generateSeed(blockHeight int64) {
	logging.Debug("Old Seed Signature", types.Claims, d.configManager.GetCurrentSeed())
	newSeed, err := d.createNewSeed(blockHeight)
	if err != nil {
		logging.Error("Failed to get next seed signature", types.Claims, "error", err)
		return
	}
	err = d.configManager.SetUpcomingSeed(*newSeed)
	if err != nil {
		logging.Error("Failed to set upcoming seed", types.Claims, "error", err)
		return
	}
	logging.Debug("New Seed Signature", types.Claims, "seed", d.configManager.GetUpcomingSeed())

	err = d.transactionClient.SubmitSeed(&inference.MsgSubmitSeed{
		BlockHeight: d.configManager.GetUpcomingSeed().Height,
		Signature:   d.configManager.GetUpcomingSeed().Signature,
	})
	if err != nil {
		logging.Error("Failed to send SubmitSeed transaction", types.Claims, "error", err)
	}
}

func (d *OnNewBlockDispatcher) changeCurrentSeed() {
	err := d.configManager.SetPreviousSeed(d.configManager.GetCurrentSeed())
	if err != nil {
		logging.Error("Failed to set previous seed", types.Claims, "error", err)
		return
	}
	err = d.configManager.SetCurrentSeed(d.configManager.GetUpcomingSeed())
	if err != nil {
		logging.Error("Failed to set current seed", types.Claims, "error", err)
		return
	}
	err = d.configManager.SetUpcomingSeed(apiconfig.SeedInfo{})
	if err != nil {
		logging.Error("Failed to set upcoming seed", types.Claims, "error", err)
		return
	}
}

func (d *OnNewBlockDispatcher) requestMoney() {
	// FIXME: we can also imagine a scenario where we weren't updating the seed for a few epochs
	//  e.g. generation fails a few times in a row for some reason
	//  Solution: query seed here?
	seed := d.configManager.GetPreviousSeed()

	logging.Info("IsSetNewValidatorsStage: sending ClaimRewards transaction", types.Claims, "seed", seed)
	err := d.transactionClient.ClaimRewards(&inference.MsgClaimRewards{
		Seed:           seed.Seed,
		PocStartHeight: uint64(seed.Height),
	})
	if err != nil {
		logging.Error("Failed to send ClaimRewards transaction", types.Claims, "error", err)
	}
}

func (d *OnNewBlockDispatcher) createNewSeed(blockHeight int64) (*apiconfig.SeedInfo, error) {
	newSeed := rand.Int63()
	newHeight := blockHeight
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, uint64(newSeed))
	signature, err := d.transactionClient.SignBytes(seedBytes)
	if err != nil {
		logging.Error("Failed to sign bytes", types.Claims, "error", err)
		return nil, err
	}
	return &apiconfig.SeedInfo{
		Seed:      newSeed,
		Height:    newHeight,
		Signature: hex.EncodeToString(signature),
	}, nil
}

// parseNewBlockInfo extracts NewBlockInfo from a JSONRPCResponse event
func parseNewBlockInfo(event *chainevents.JSONRPCResponse) (*NewBlockInfo, error) {
	blockHeight, err := getBlockHeight(event.Result.Data.Value)
	if err != nil {
		return nil, err
	}

	blockHash, err := getBlockHash(event.Result.Data.Value)
	if err != nil {
		return nil, err
	}

	return &NewBlockInfo{
		Height:    blockHeight,
		Hash:      blockHash,
		Timestamp: time.Now(), // We could parse this from the event if needed
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
