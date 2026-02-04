package inference

import (
	"context"
	"encoding/json"
	"fmt"

	"cosmossdk.io/core/appmodule"
	"cosmossdk.io/core/store"
	"cosmossdk.io/depinject"
	"cosmossdk.io/log"
	"cosmossdk.io/math"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	authzkeeper "github.com/cosmos/cosmos-sdk/x/authz/keeper"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/productscience/inference/testenv"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/shopspring/decimal"
	"github.com/spf13/cobra"

	// this line is used by starport scaffolding # 1

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	modulev1 "github.com/productscience/inference/api/inference/inference/module"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

var (
	_ module.AppModuleBasic      = (*AppModule)(nil)
	_ module.AppModuleSimulation = (*AppModule)(nil)
	_ module.HasGenesis          = (*AppModule)(nil)
	_ module.HasInvariants       = (*AppModule)(nil)
	_ module.HasConsensusVersion = (*AppModule)(nil)

	_ appmodule.AppModule       = (*AppModule)(nil)
	_ appmodule.HasBeginBlocker = (*AppModule)(nil)
	_ appmodule.HasEndBlocker   = (*AppModule)(nil)
)

const (
	defaultInferencePruningThreshold = 4
	defaultPocPruningThreshold       = 4
)

// ----------------------------------------------------------------------------
// AppModuleBasic
// ----------------------------------------------------------------------------

// AppModuleBasic implements the AppModuleBasic interface that defines the
// independent methods a Cosmos SDK module needs to implement.
type AppModuleBasic struct {
	cdc codec.BinaryCodec
}

func NewAppModuleBasic(cdc codec.BinaryCodec) AppModuleBasic {
	return AppModuleBasic{cdc: cdc}
}

// Name returns the name of the module as a string.
func (AppModuleBasic) Name() string {
	return types.ModuleName
}

// RegisterLegacyAminoCodec registers the amino codec for the module, which is used
// to marshal and unmarshal structs to/from []byte in order to persist them in the module's KVStore.
func (AppModuleBasic) RegisterLegacyAminoCodec(cdc *codec.LegacyAmino) {}

// RegisterInterfaces registers a module's interface types and their concrete implementations as proto.Message.
func (a AppModuleBasic) RegisterInterfaces(reg cdctypes.InterfaceRegistry) {
	types.RegisterInterfaces(reg)
}

// DefaultGenesis returns a default GenesisState for the module, marshalled to json.RawMessage.
// The default GenesisState need to be defined by the module developer and is primarily used for testing.
func (AppModuleBasic) DefaultGenesis(cdc codec.JSONCodec) json.RawMessage {
	//nolint:forbidigo // Genesis code
	return cdc.MustMarshalJSON(types.DefaultGenesis())
}

// ValidateGenesis used to validate the GenesisState, given in its json.RawMessage form.
func (AppModuleBasic) ValidateGenesis(cdc codec.JSONCodec, config client.TxEncodingConfig, bz json.RawMessage) error {
	var genState types.GenesisState
	if err := cdc.UnmarshalJSON(bz, &genState); err != nil {
		return fmt.Errorf("failed to unmarshal %s genesis state: %w", types.ModuleName, err)
	}
	return genState.Validate()
}

// RegisterGRPCGatewayRoutes registers the gRPC Gateway routes for the module.
func (AppModuleBasic) RegisterGRPCGatewayRoutes(clientCtx client.Context, mux *runtime.ServeMux) {
	if err := types.RegisterQueryHandlerClient(context.Background(), mux, types.NewQueryClient(clientCtx)); err != nil {
		//nolint:forbidigo // init code
		panic(err)
	}
}

// ----------------------------------------------------------------------------
// AppModule
// ----------------------------------------------------------------------------

// AppModule implements the AppModule interface that defines the inter-dependent methods that modules need to implement
type AppModule struct {
	AppModuleBasic

	keeper           keeper.Keeper
	accountKeeper    types.AccountKeeper
	bankKeeper       types.BankKeeper
	groupMsgServer   types.GroupMessageKeeper
	collateralKeeper types.CollateralKeeper
}

func NewAppModule(
	cdc codec.Codec,
	keeper keeper.Keeper,
	accountKeeper types.AccountKeeper,
	bankKeeper types.BankKeeper,
	groupMsgServer types.GroupMessageKeeper,
	collateralKeeper types.CollateralKeeper,
) AppModule {
	return AppModule{
		AppModuleBasic:   NewAppModuleBasic(cdc),
		keeper:           keeper,
		accountKeeper:    accountKeeper,
		bankKeeper:       bankKeeper,
		groupMsgServer:   groupMsgServer,
		collateralKeeper: collateralKeeper,
	}
}

// RegisterServices registers a gRPC query service to respond to the module-specific gRPC queries
func (am AppModule) RegisterServices(cfg module.Configurator) {
	types.RegisterMsgServer(cfg.MsgServer(), keeper.NewMsgServerImpl(am.keeper))
	types.RegisterQueryServer(cfg.QueryServer(), am.keeper)
}

// RegisterInvariants registers the invariants of the module. If an invariant deviates from its predicted value, the InvariantRegistry triggers appropriate logic (most often the chain will be halted)
func (am AppModule) RegisterInvariants(_ sdk.InvariantRegistry) {}

// InitGenesis performs the module's genesis initialization. It returns no validator updates.
func (am AppModule) InitGenesis(ctx sdk.Context, cdc codec.JSONCodec, gs json.RawMessage) {
	var genState types.GenesisState
	// Initialize global index to index in genesis state
	//nolint:forbidigo // Genesis code
	cdc.MustUnmarshalJSON(gs, &genState)

	InitGenesis(ctx, am.keeper, genState)
}

// ExportGenesis returns the module's exported genesis state as raw JSON bytes.
func (am AppModule) ExportGenesis(ctx sdk.Context, cdc codec.JSONCodec) json.RawMessage {
	genState := ExportGenesis(ctx, am.keeper)
	//nolint:forbidigo // Genesis code
	return cdc.MustMarshalJSON(genState)
}

// ConsensusVersion is a sequence number for state-breaking change of the module.
// It should be incremented on each consensus-breaking change introduced by the module.
func (AppModule) ConsensusVersion() uint64 { return 12 }

// BeginBlock contains the logic that is automatically triggered at the beginning of each block.
func (am AppModule) BeginBlock(ctx context.Context) error {
	// Update dynamic pricing for all models at the start of each block
	// This ensures consistent pricing for all inferences processed in this block
	err := am.keeper.UpdateDynamicPricing(ctx)
	if err != nil {
		am.LogError("Failed to update dynamic pricing", types.Pricing, "error", err)
		// Don't return error - allow block processing to continue even if pricing update fails
	}

	return nil
}

func (am AppModule) expireInferences(
	ctx context.Context,
	timeouts []types.InferenceTimeout,
	blockHeight int64,
	currentEpoch *types.Epoch,
	params *types.Params,
) error {
	if len(timeouts) == 0 {
		return nil
	}

	// Create expiry context once for efficiency (reuse already-loaded params and epoch data)
	expiryCtx, err := am.NewInferenceExpiryContextWithEpoch(ctx, blockHeight, currentEpoch, params)
	if err != nil {
		am.LogError("Failed to create inference expiry context", types.Inferences, "error", err)
		return err
	}

	for _, i := range timeouts {
		inference, found := am.keeper.GetInference(ctx, i.InferenceId)
		if !found {
			continue
		}
		if inference.Status == types.InferenceStatus_STARTED {
			am.handleExpiredInferenceWithContext(ctx, inference, expiryCtx)
		}
	}
	return nil
}

// expireInferenceAndIssueRefund marks an inference as expired and issues a refund
// Returns the updated inference
func (am AppModule) expireInferenceAndIssueRefund(ctx context.Context, inference types.Inference) types.Inference {
	inference.Status = types.InferenceStatus_EXPIRED
	inference.ActualCost = 0

	err := am.keeper.IssueRefund(ctx, inference.EscrowAmount, inference.RequestedBy, "expired_inference:"+inference.InferenceId)
	if err != nil {
		am.LogError("Error issuing refund", types.Inferences, "error", err)
	}

	err = am.keeper.SetInference(ctx, inference)
	if err != nil {
		am.LogError("Error updating inference", types.Inferences, "error", err)
	}

	return inference
}

func (am AppModule) handleExpiredInferenceWithContext(ctx context.Context, inference types.Inference, expiryCtx *InferenceExpiryContext) {
	executor, found := am.keeper.GetParticipant(ctx, inference.AssignedTo)
	if !found {
		am.LogWarn("Unable to find participant for expired inference", types.Inferences, "inferenceId", inference.InferenceId, "executedBy", inference.ExecutedBy)
		return
	}

	// Determine which epoch to check based on timing
	// This may lazy-load previous epoch data if the inference started before current epoch
	epochToCheck := expiryCtx.GetEpochForInference(ctx, am.keeper, inference)
	if epochToCheck == nil {
		am.LogWarn("No epoch available for expired inference check", types.Inferences, "inferenceId", inference.InferenceId)
		am.expireInferenceAndIssueRefund(ctx, inference)
		return
	}

	// Get the cached active participants for the appropriate epoch
	var activeParticipants *types.ActiveParticipants
	if expiryCtx.CurrentEpoch != nil && epochToCheck.Index == expiryCtx.CurrentEpoch.Index {
		activeParticipants = expiryCtx.CurrentActiveParticipants
	} else if expiryCtx.PreviousEpoch != nil && epochToCheck.Index == expiryCtx.PreviousEpoch.Index {
		activeParticipants = expiryCtx.PreviousActiveParticipants
	}

	if activeParticipants == nil {
		am.LogWarn("No active participants available for expired inference check", types.Inferences,
			"inferenceId", inference.InferenceId, "epochIndex", epochToCheck.Index)
		am.expireInferenceAndIssueRefund(ctx, inference)
		return
	}

	// Determine whether to check preserve nodes or regular mlnodes
	checkPreserveNode := expiryCtx.ShouldCheckPreserveNode(inference)

	// Check if executor has the required node for the model (using cached active participants)
	hasNode := am.HasNodeForModel(inference.AssignedTo, inference.Model, checkPreserveNode, activeParticipants)

	if !hasNode {
		nodeType := "mlnode"
		if checkPreserveNode {
			nodeType = "preserve node"
		}
		am.LogWarn("Executor doesn't have required node for expired inference, skipping penalty",
			types.Inferences,
			"inferenceId", inference.InferenceId,
			"executor", inference.AssignedTo,
			"model", inference.Model,
			"nodeType", nodeType,
			"epochIndex", epochToCheck.Index,
			"inPoCRange", expiryCtx.IsBlockInPoCRange(inference.StartBlockHeight) || expiryCtx.IsBlockInPoCRange(expiryCtx.CurrentBlockHeight))

		// Still issue refund and mark as expired, but don't penalize executor
		am.expireInferenceAndIssueRefund(ctx, inference)
		return
	}

	// Executor has the required node, proceed with normal expiry handling (with penalty)
	am.LogInfo("Inference expired, not finished. Issuing refund and penalizing executor",
		types.Inferences,
		"inferenceId", inference.InferenceId,
		"executor", inference.AssignedTo,
		"model", inference.Model,
		"epochIndex", epochToCheck.Index)

	inference = am.expireInferenceAndIssueRefund(ctx, inference)

	executor.CurrentEpochStats.MissedRequests++
	err := am.keeper.SetParticipant(ctx, executor)
	if err != nil {
		am.LogError("Error updating participant for expired inference", types.Participants, "error", err)
	}
}

// EndBlock contains the logic that is automatically triggered at the end of each block.
func (am AppModule) EndBlock(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockHeight := sdkCtx.BlockHeight()
	blockTime := sdkCtx.BlockTime().Unix()

	// Handle confirmation PoC trigger decisions and phase transitions
	err := am.handleConfirmationPoC(ctx, blockHeight)
	if err != nil {
		am.LogError("Failed to handle confirmation PoC", types.PoC, "error", err)
		// Don't return error - allow block processing to continue
	}

	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		am.LogError("Unable to get parameters", types.Settle, "error", err.Error())
		return err
	}
	epochParams := params.EpochParams
	currentEpoch, found := am.keeper.GetEffectiveEpoch(ctx)
	if !found || currentEpoch == nil {
		am.LogError("Unable to get effective epoch", types.EpochGroup, "blockHeight", blockHeight)
		return nil
	}
	epochContext, err := types.NewEpochContextFromEffectiveEpoch(*currentEpoch, *epochParams, blockHeight)
	if err != nil {
		am.LogError("Unable to create epoch context", types.EpochGroup, "error", err.Error())
		return nil
	}

	currentEpochGroup, err := am.keeper.GetEpochGroupForEpoch(ctx, *currentEpoch)
	if err != nil {
		am.LogError("Unable to get current epoch group", types.EpochGroup, "error", err.Error())
		return nil
	}

	timeouts := am.keeper.GetAllInferenceTimeoutForHeight(ctx, uint64(blockHeight))
	err = am.expireInferences(ctx, timeouts, blockHeight, currentEpoch, &params)
	if err != nil {
		am.LogError("Error expiring inferences", types.Inferences)
	}
	for _, t := range timeouts {
		am.keeper.RemoveInferenceTimeout(ctx, t.ExpirationHeight, t.InferenceId)
	}

	err = am.keeper.Prune(ctx, int64(currentEpoch.Index))
	if err != nil {
		am.LogError("Error during pruning", types.Pruning, "error", err.Error())
	}

	// Track full chain upgrades from UpgradeKeeper
	upgradePlan, err := am.keeper.GetUpgradePlan(ctx)
	if err == nil && upgradePlan.Height > 0 && upgradePlan.Height == blockHeight {
		am.LogInfo("FullUpgradeActive - tracking height", types.Upgrades,
			"upgradeHeight", upgradePlan.Height, "blockHeight", blockHeight, "name", upgradePlan.Name)
		err = am.keeper.SetLastUpgradeHeight(ctx, blockHeight)
		if err != nil {
			am.LogError("Failed to set last upgrade height for full upgrade", types.Upgrades, "error", err)
		}
	}

	partialUpgrades := am.keeper.GetAllPartialUpgrade(ctx)
	for _, pu := range partialUpgrades {
		if pu.Height == uint64(blockHeight) {
			if pu.NodeVersion != "" {
				am.LogInfo("PartialUpgradeActive - updating current MLNode version", types.Upgrades,
					"partialUpgradeHeight", pu.Height, "blockHeight", blockHeight, "nodeVersion", pu.NodeVersion)
				am.keeper.SetMLNodeVersion(ctx, types.MLNodeVersion{
					CurrentVersion: pu.NodeVersion,
				})
			}

			// Track last upgrade height
			err = am.keeper.SetLastUpgradeHeight(ctx, blockHeight)
			if err != nil {
				am.LogError("Failed to set last upgrade height", types.Upgrades, "error", err)
			}
		} else if pu.Height < uint64(blockHeight) {
			am.LogInfo("PartialUpgradeExpired", types.Upgrades, "partialUpgradeHeight", pu.Height, "blockHeight", blockHeight)
			am.keeper.RemovePartialUpgrade(ctx, pu.Height)
		}
	}

	// Stage execution order for epoch transitions:
	// 1. IsEndOfPoCValidationStage: Complete all epoch formation (onEndOfPoCValidationStage)
	// 2. IsSetNewValidatorsStage: Switch validators and activate epoch (onSetNewValidatorsStage)
	// This separation ensures clean boundaries between epoch preparation and validator switching
	// and allow time for api nodes to load models on ml nodes.

	if epochContext.IsEndOfPoCValidationStage(blockHeight) {
		am.LogInfo("StartStage:onEndOfPoCValidationStage", types.Stages, "blockHeight", blockHeight)
		am.onEndOfPoCValidationStage(ctx, blockHeight, blockTime)
	}

	if epochContext.IsSetNewValidatorsStage(blockHeight) {
		am.LogInfo("StartStage:onSetNewValidatorsStage", types.Stages, "blockHeight", blockHeight)
		am.onSetNewValidatorsStage(ctx, blockHeight, blockTime)
		if err := am.keeper.SetEffectiveEpochIndex(ctx, getNextEpochIndex(*currentEpoch)); err != nil {
			return err
		}
	}

	if epochContext.IsStartOfPocStage(blockHeight) {
		upcomingEpoch := createNewEpoch(*currentEpoch, blockHeight)
		err = am.keeper.SetEpoch(ctx, upcomingEpoch)
		if err != nil {
			am.LogError("Unable to set upcoming epoch", types.EpochGroup, "error", err.Error())
			return err
		}

		am.LogInfo("StartStage:PocStart", types.Stages, "blockHeight", blockHeight)
		newGroup, err := am.keeper.CreateEpochGroup(ctx, uint64(blockHeight), upcomingEpoch.Index)
		if err != nil {
			am.LogError("Unable to create epoch group", types.EpochGroup, "error", err.Error())
			return err
		}
		err = newGroup.CreateGroup(ctx)
		if err != nil {
			am.LogError("Unable to create epoch group", types.EpochGroup, "error", err.Error())
			return err
		}
	}

	if currentEpochGroup.IsChanged(ctx) {
		am.LogInfo("EpochGroupChanged", types.EpochGroup, "blockHeight", blockHeight)
		computeResult, err := currentEpochGroup.GetComputeResults(ctx)
		if err != nil {
			am.LogError("Unable to get compute results", types.EpochGroup, "error", err.Error())
			return nil
		}
		am.LogInfo("EpochGroupChanged", types.EpochGroup, "computeResult", computeResult, "error", err)

		// Apply early network protection if conditions are met
		finalComputeResult := am.applyEarlyNetworkProtection(ctx, computeResult)

		_, err = am.keeper.Staking.SetComputeValidators(ctx, finalComputeResult, testenv.IsTestNet())
		if err != nil {
			am.LogError("Unable to update epoch group", types.EpochGroup, "error", err.Error())
		}
		currentEpochGroup.MarkUnchanged(ctx)
	}

	return nil
}

func createNewEpoch(prevEpoch types.Epoch, blockHeight int64) *types.Epoch {
	return &types.Epoch{
		Index:               getNextEpochIndex(prevEpoch),
		PocStartBlockHeight: int64(blockHeight),
	}
}

func getNextEpochIndex(prevEpoch types.Epoch) uint64 {
	return prevEpoch.Index + 1
}

// onEndOfPoCValidationStage handles all epoch formation logic at the end of PoC validation.
// This stage is responsible for:
// - Account settling from the previous epoch
// - Computing new weights based on PoC results
// - Setting models for participants (MLNode allocation)
// - Registering top miners
// - Setting active participants for the upcoming epoch
// - Adding epoch members to the upcoming epoch group
// This stage executes at IsEndOfPoCValidationStage(blockHeight) and must complete
// before validator switching occurs in onSetNewValidatorsStage.
func (am AppModule) onEndOfPoCValidationStage(ctx context.Context, blockHeight int64, blockTime int64) {
	effectiveEpoch, found := am.keeper.GetEffectiveEpoch(ctx)
	if !found {
		am.LogError("onEndOfPoCValidationStage: Unable to get effective epoch", types.EpochGroup, "blockHeight", blockHeight)
		return
	}

	// Signal to the collateral module that the epoch has advanced.
	// This will trigger its internal unbonding queue processing.
	if am.keeper.GetCollateralKeeper() != nil {
		am.LogInfo("onEndOfPoCValidationStage: Advancing collateral epoch", types.Tokenomics, "effectiveEpoch.Index", effectiveEpoch.Index)
		if err := am.keeper.GetCollateralKeeper().AdvanceEpoch(ctx, effectiveEpoch.Index); err != nil {
			am.LogError("onEndOfPoCValidationStage: Unable to advance collateral epoch", types.Tokenomics, "error", err.Error())
		}
	} else {
		am.LogError("collateral keeper is null", types.Tokenomics)
	}

	// Signal to the streamvesting module that the epoch has advanced.
	// This will trigger vested token unlocking for the completed epoch.
	if am.keeper.GetStreamVestingKeeper() != nil {
		if err := am.keeper.GetStreamVestingKeeper().AdvanceEpoch(ctx, effectiveEpoch.Index); err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to advance streamvesting epoch", types.Tokenomics, "error", err.Error())
		}
	}

	previousEpoch, found := am.keeper.GetPreviousEpoch(ctx)
	previousEpochIndex := uint64(0)
	if found {
		previousEpochIndex = previousEpoch.Index
	}

	err := am.keeper.SettleAccounts(ctx, effectiveEpoch.Index, previousEpochIndex)
	if err != nil {
		am.LogError("onEndOfPoCValidationStage: Unable to settle accounts", types.Settle, "error", err.Error())
	}

	upcomingEpoch, found := am.keeper.GetUpcomingEpoch(ctx)
	if !found || upcomingEpoch == nil {
		am.LogError("onEndOfPoCValidationStage: Unable to get upcoming epoch group", types.EpochGroup)
		return
	}

	// Dispatch to V1 or V2 weight calculation based on poc_v2_enabled flag
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		am.LogError("onEndOfPoCValidationStage: Unable to get params", types.PoC, "error", err.Error())
		return
	}
	var activeParticipants []*types.ActiveParticipant
	if params.PocParams.PocV2Enabled {
		activeParticipants = am.ComputeNewWeights(ctx, *upcomingEpoch)
	} else {
		activeParticipants = am.ComputeNewWeightsV1(ctx, *upcomingEpoch)
	}
	if activeParticipants == nil {
		am.LogError("onEndOfPoCValidationStage: computeResult == nil && activeParticipants == nil", types.PoC)
		return
	}

	modelAssigner := NewModelAssigner(am.keeper, am.keeper)
	modelAssigner.setModelsForParticipants(ctx, activeParticipants, *upcomingEpoch)

	// Adjust weights based on collateral after the grace period. This modifies the weights in-place.
	if err := am.keeper.AdjustWeightsByCollateral(ctx, activeParticipants); err != nil {
		am.LogError("onSetNewValidatorsStage: failed to adjust weights by collateral", types.Tokenomics, "error", err)
		// Depending on chain policy, we might want to halt on error. For now, we log and continue,
		// which means participants will proceed with their unadjusted PotentialWeight.
	}

	// Apply universal power capping to epoch powers
	activeParticipants = am.applyEpochPowerCapping(ctx, activeParticipants)

	modelAssigner.AllocateMLNodesForPoC(ctx, *upcomingEpoch, activeParticipants)
	am.LogInfo("Finished PoC allocation for all participants", types.EpochGroup, "step", "poc_allocation_complete")

	err = am.RegisterTopMiners(ctx, activeParticipants, blockTime)
	if err != nil {
		am.LogError("onEndOfPoCValidationStage: Unable to register top miners", types.Tokenomics, "error", err.Error())
		return
	}

	am.LogInfo("onEndOfPoCValidationStage: computed new weights", types.Stages,
		"upcomingEpoch.Index", upcomingEpoch.Index,
		"PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
		"len(activeParticipants)", len(activeParticipants))

	err = am.keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		Participants:        activeParticipants,
		EpochGroupId:        upcomingEpoch.Index,
		EpochId:             upcomingEpoch.Index,
		PocStartBlockHeight: upcomingEpoch.PocStartBlockHeight,
		// TODO [PRTODO]: not sure EffectiveBlockHeight is set by now
		EffectiveBlockHeight: blockHeight + 2, // FIXME: verify it's +2, I'm not sure
		CreatedAtBlockHeight: blockHeight,
	})
	if err != nil {
		am.LogError("onEndOfPoCValidationStage: Unable to set active participants", types.EpochGroup, "error", err.Error())
		return
	}

	upcomingEg, err := am.keeper.GetEpochGroupForEpoch(ctx, *upcomingEpoch)
	if err != nil {
		am.LogError("onEndOfPoCValidationStage: Unable to get epoch group for upcoming epoch", types.EpochGroup,
			"upcomingEpoch.Index", upcomingEpoch.Index, "upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight, "error", err.Error())
		return
	}

	am.addEpochMembers(ctx, upcomingEg, activeParticipants)

	// Call BLS module to initiate key generation for the new epoch
	am.InitiateBLSKeyGeneration(ctx, upcomingEpoch.Index, activeParticipants)
}

// onSetNewValidatorsStage handles validator switching and epoch group activation.
// This stage is responsible for:
// - Computing unit of compute price for the upcoming epoch
// - Moving the upcoming epoch group to effective status
// - Switching the active validator set
// - Setting the effective epoch index
// This stage executes at IsSetNewValidatorsStage(blockHeight) and should run after
// all epoch formation logic has completed in onEndOfPoCValidationStage.
// The stage focuses solely on validator switching, with all epoch preparation
// handled by the previous stage for clean separation of concerns.
func (am AppModule) onSetNewValidatorsStage(ctx context.Context, blockHeight int64, blockTime int64) {
	am.LogInfo("onSetNewValidatorsStage start", types.Stages, "blockHeight", blockHeight)

	upcomingEpoch, found := am.keeper.GetUpcomingEpoch(ctx)
	if !found || upcomingEpoch == nil {
		am.LogError("onSetNewValidatorsStage: Unable to get upcoming epoch group", types.EpochGroup)
		return
	}

	upcomingEg, err := am.keeper.GetEpochGroupForEpoch(ctx, *upcomingEpoch)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to get epoch group for upcoming epoch", types.EpochGroup,
			"upcomingEpoch.Index", upcomingEpoch.Index, "upcomingEpoch.PocStartBlockHeight", upcomingEpoch.PocStartBlockHeight, "error", err.Error())
		return
	}

	// Cache model capacities for the new epoch to enable fast dynamic pricing calculations
	err = am.keeper.CacheAllModelCapacities(ctx)
	if err != nil {
		am.LogError("Failed to cache model capacities for new epoch", types.Pricing, "error", err, "blockHeight", blockHeight)
		// Don't return error - epoch transition should continue even if capacity caching fails
	}

	unitOfComputePrice, err := am.computePrice(ctx, *upcomingEpoch, upcomingEg)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to compute price", types.Pricing, "error", err.Error())
		return
	}

	// TODO: Move this so active participants are set 1 block before new validators
	am.moveUpcomingToEffectiveGroup(ctx, blockHeight, unitOfComputePrice)
}

func (am AppModule) addEpochMembers(ctx context.Context, upcomingEg *epochgroup.EpochGroup, activeParticipants []*types.ActiveParticipant) {
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		am.LogError("addEpochMembers: Unable to get params", types.EpochGroup, "error", err.Error())
		return
	}
	validationParams := params.ValidationParams

	for _, p := range activeParticipants {
		reputation, err := am.calculateParticipantReputation(ctx, p, validationParams)
		if err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to calculate participant reputation", types.EpochGroup, "error", err.Error())
			reputation = 0
		}
		if p.Seed == nil {
			am.LogError("onSetNewValidatorsStage: addEpochMembers. ILLEGAL STATE. Participant seed is nil. Skipping this participant", types.EpochGroup,
				"participantIndex", p.Index)
			continue
		}

		member := epochgroup.NewEpochMemberFromActiveParticipant(p, reputation, 0)
		err = upcomingEg.AddMember(ctx, member)
		if err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to add member", types.EpochGroup, "error", err.Error())
			continue
		}
	}
}

func (am AppModule) computePrice(ctx context.Context, upcomingEpoch types.Epoch, upcomingEg *epochgroup.EpochGroup) (uint64, error) {
	var defaultPrice int64
	if upcomingEpoch.Index > 1 {
		currentEg, err := am.keeper.GetCurrentEpochGroup(ctx)
		if err != nil {
			am.LogError("onSetNewValidatorsStage: Unable to get current epoch group", types.EpochGroup, "error", err.Error())
			return 0, err
		}
		defaultPrice = currentEg.GroupData.UnitOfComputePrice
	} else {
		params, err := am.keeper.GetParams(ctx)
		if err != nil {
			am.LogError("computePrice: Unable to get params", types.Pricing, "error", err.Error())
			return 0, err
		}
		defaultPrice = params.EpochParams.DefaultUnitOfComputePrice
	}

	proposals, err := am.keeper.AllUnitOfComputePriceProposals(ctx)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: Unable to get all unit of compute price proposals", types.Pricing, "error", err.Error())
		return 0, err
	}

	am.LogInfo("onSetNewValidatorsStage: unitOfCompute: retrieved proposals", types.Pricing, "len(proposals)", len(proposals))

	medianProposal, err := upcomingEg.ComputeUnitOfComputePrice(ctx, proposals, uint64(defaultPrice))
	am.LogInfo("onSetNewValidatorsStage: unitOfCompute: ", types.Pricing, "medianProposal", medianProposal)
	if err != nil {
		am.LogError("onSetNewValidatorsStage: unitOfCompute: onSetNewValidatorsStage: Unable to compute unit of compute price", types.Pricing, "error", err.Error())
		return 0, err
	}

	return medianProposal, nil
}

func (am AppModule) calculateParticipantReputation(ctx context.Context, p *types.ActiveParticipant, params *types.ValidationParams) (int64, error) {
	summaries := am.keeper.GetEpochPerformanceSummariesByParticipant(ctx, p.Index)

	reputationContext := calculations.ReputationContext{
		EpochCount:           int64(len(summaries)),
		EpochMissPercentages: make([]decimal.Decimal, len(summaries)),
		ValidationParams:     params,
	}

	for i, summary := range summaries {
		inferenceCount := decimal.NewFromInt(int64(summary.InferenceCount))
		if inferenceCount.IsZero() {
			reputationContext.EpochMissPercentages[i] = decimal.Zero
			continue
		}

		missed := decimal.NewFromInt(int64(summary.MissedRequests))
		reputationMetric := missed.Div(inferenceCount)
		reputationContext.EpochMissPercentages[i] = reputationMetric
	}

	reputation := calculations.CalculateReputation(&reputationContext)
	am.LogInfo("ReputationCalculated", types.EpochGroup, "participantIndex", p.Index, "reputation", reputation)

	return reputation, nil
}

func (am AppModule) moveUpcomingToEffectiveGroup(ctx context.Context, blockHeight int64, unitOfComputePrice uint64) {
	newEpochIndex, found := am.keeper.GetUpcomingEpochIndex(ctx)
	if !found {
		am.LogError("MoveUpcomingToEffectiveGroup: Unable to get upcoming epoch group id", types.EpochGroup, "blockHeight", blockHeight)
		return
	}

	previousEpochIndex, found := am.keeper.GetEffectiveEpochIndex(ctx)
	if !found {
		am.LogError("MoveUpcomingToEffectiveGroup: Unable to get upcoming epoch group id", types.EpochGroup, "blockHeight", blockHeight)
		return
	}

	am.LogInfo("NewEpochGroup", types.EpochGroup, "blockHeight", blockHeight, "newEpochIndex", newEpochIndex)
	newGroupData, found := am.keeper.GetEpochGroupData(ctx, newEpochIndex, "")
	if !found {
		am.LogWarn("NewEpochGroupDataNotFound", types.EpochGroup, "blockHeight", blockHeight, "newEpochIndex", newEpochIndex)
		return
	}
	previousGroupData, found := am.keeper.GetEpochGroupData(ctx, previousEpochIndex, "")
	if !found {
		am.LogWarn("PreviousEpochGroupDataNotFound", types.EpochGroup, "blockHeight", blockHeight, "previousEpochIndex", previousEpochIndex)
		return
	}
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		am.LogError("MoveUpcomingToEffectiveGroup: Unable to get params", types.EpochGroup, "blockHeight", blockHeight, "error", err.Error())
		return
	}
	newGroupData.EffectiveBlockHeight = blockHeight
	newGroupData.UnitOfComputePrice = int64(unitOfComputePrice)
	newGroupData.PreviousEpochRequests = previousGroupData.NumberOfRequests
	newGroupData.ValidationParams = params.ValidationParams

	previousGroupData.LastBlockHeight = blockHeight - 1

	am.keeper.SetEpochGroupData(ctx, newGroupData)
	am.keeper.SetEpochGroupData(ctx, previousGroupData)

	// Set all current ActiveParticipants as ParticipantStatus_ACTIVE
	activeParticipants, found := am.keeper.GetActiveParticipants(ctx, newEpochIndex)
	if !found {
		am.LogError("Unable to get active participants", types.EpochGroup, "epochIndex", newEpochIndex)
		return
	}
	ids := make([]string, len(activeParticipants.Participants))
	for i, participant := range activeParticipants.Participants {
		ids[i] = participant.Index
	}
	participants := am.keeper.GetParticipants(ctx, ids)

	am.LogInfo("Setting participants to active", types.EpochGroup, "len(participants)", len(participants))
	for _, participant := range participants {
		participant.Status = types.ParticipantStatus_ACTIVE
		err := am.keeper.SetParticipant(ctx, participant)
		if err != nil {
			am.LogError("Unable to set participant to active", types.EpochGroup, "participantIndex", participant.Index, "error", err.Error())
			continue
		}
	}

	// At this point, clear all active invalidations in case of any hanging invalidations
	err = am.keeper.ActiveInvalidations.Clear(ctx, nil)
	if err != nil {
		am.LogError("Unable to clear active invalidations", types.EpochGroup, "error", err.Error())
	}
}

// applyEpochPowerCapping applies universal power capping to activeParticipants after ComputeNewWeights
// This system is applied universally regardless of network maturity
func (am AppModule) applyEpochPowerCapping(ctx context.Context, activeParticipants []*types.ActiveParticipant) []*types.ActiveParticipant {
	// Apply universal power capping
	result := ApplyPowerCapping(ctx, am.keeper, activeParticipants)

	// Log capping application results
	originalTotal := int64(0)
	for _, participant := range activeParticipants {
		originalTotal += participant.Weight
	}

	if result.WasCapped {
		am.LogInfo("Universal power capping applied to epoch powers", types.PoC,
			"originalTotalPower", originalTotal,
			"cappedTotalPower", result.TotalPower,
			"participantCount", len(activeParticipants))
	} else {
		am.LogInfo("Universal power capping evaluated but not applied to epoch powers", types.PoC,
			"totalPower", originalTotal,
			"participantCount", len(activeParticipants),
			"reason", "no participant exceeded 30% limit")
	}

	return result.CappedParticipants
}

// applyEarlyNetworkProtection applies genesis guardian enhancement to compute results before validator set updates
// This system only applies when network is immature (below maturity threshold)
func (am AppModule) applyEarlyNetworkProtection(ctx context.Context, computeResults []stakingkeeper.ComputeResult) []stakingkeeper.ComputeResult {
	// Apply genesis guardian enhancement (only when network immature)
	result := ApplyGenesisGuardianEnhancement(ctx, am.keeper, computeResults)

	// Log enhancement application results
	originalTotal := int64(0)
	for _, cr := range computeResults {
		originalTotal += cr.Power
	}

	if result.WasEnhanced {
		genesisGuardianAddresses := am.keeper.GetGenesisGuardianAddresses(ctx)

		// Count enhanced guardians and calculate their individual powers
		enhancedGuardians := []string{}
		guardianPowers := []int64{}
		guardianAddressMap := make(map[string]bool)
		for _, address := range genesisGuardianAddresses {
			guardianAddressMap[address] = true
		}

		for _, cr := range result.ComputeResults {
			if guardianAddressMap[cr.OperatorAddress] {
				enhancedGuardians = append(enhancedGuardians, cr.OperatorAddress)
				guardianPowers = append(guardianPowers, cr.Power)
			}
		}

		am.LogInfo("Genesis guardian enhancement applied to staking powers", types.EpochGroup,
			"originalTotalPower", originalTotal,
			"enhancedTotalPower", result.TotalPower,
			"participantCount", len(computeResults),
			"guardianCount", len(enhancedGuardians),
			"enhancedGuardians", enhancedGuardians,
			"guardianPowers", guardianPowers)
	} else {
		genesisGuardianAddresses := am.keeper.GetGenesisGuardianAddresses(ctx)
		am.LogInfo("Genesis guardian enhancement evaluated but not applied to staking powers", types.EpochGroup,
			"totalPower", originalTotal,
			"participantCount", len(computeResults),
			"configuredGuardianCount", len(genesisGuardianAddresses),
			"reason", "network mature, insufficient participants, or no genesis guardians found")
	}

	return result.ComputeResults
}

// IsOnePerModuleType implements the depinject.OnePerModuleType interface.
func (am AppModule) IsOnePerModuleType() {}

// IsAppModule implements the appmodule.AppModule interface.
func (am AppModule) IsAppModule() {}

// GetTxCmd returns the transaction commands for this module
func GetTxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        "inference",
		Short:                      "Inference transaction subcommands",
		DisableFlagParsing:         true,
		SuggestionsMinimumDistance: 2,
		RunE:                       client.ValidateCmd,
	}

	cmd.AddCommand(GrantMLOpsPermissionsCmd())

	return cmd
}

// ----------------------------------------------------------------------------
// App Wiring Setup
// ----------------------------------------------------------------------------

func init() {
	appmodule.Register(
		&modulev1.Module{},
		appmodule.Provide(ProvideModule),
	)
}

type ModuleInputs struct {
	depinject.In

	StoreService store.KVStoreService
	Cdc          codec.Codec
	Config       *modulev1.Module
	Logger       log.Logger

	AccountKeeper       types.AccountKeeper
	BankKeeper          types.BankKeeper
	BankEscrowKeeper    types.BookkeepingBankKeeper
	ValidatorSet        types.ValidatorSet
	StakingKeeper       types.StakingKeeper
	GroupServer         types.GroupMessageKeeper
	BlsKeeper           types.BlsKeeper
	CollateralKeeper    types.CollateralKeeper
	StreamVestingKeeper types.StreamVestingKeeper
	AuthzKeeper         authzkeeper.Keeper
	GetWasmKeeper       func() wasmkeeper.Keeper `optional:"true"`
	UpgradeKeeper       types.UpgradeKeeper
}

type ModuleOutputs struct {
	depinject.Out

	InferenceKeeper keeper.Keeper
	Module          appmodule.AppModule
	Hooks           stakingtypes.StakingHooksWrapper
}

func ProvideModule(in ModuleInputs) ModuleOutputs {
	// default to governance authority if not provided
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)
	if in.Config.Authority != "" {
		authority = authtypes.NewModuleAddressOrBech32Address(in.Config.Authority)
	}

	k := keeper.NewKeeper(
		in.Cdc,
		in.StoreService,
		in.Logger,
		authority.String(),
		in.BankEscrowKeeper,
		in.BankKeeper,
		in.GroupServer,
		in.ValidatorSet,
		in.StakingKeeper,
		in.AccountKeeper,
		in.BlsKeeper,
		in.CollateralKeeper,
		in.StreamVestingKeeper,
		in.AuthzKeeper,
		in.GetWasmKeeper,
		in.UpgradeKeeper,
	)

	m := NewAppModule(
		in.Cdc,
		k,
		in.AccountKeeper,
		in.BankKeeper,
		in.GroupServer,
		in.CollateralKeeper,
	)

	return ModuleOutputs{
		InferenceKeeper: k,
		Module:          m,
		Hooks:           stakingtypes.StakingHooksWrapper{StakingHooks: StakingHooksLogger{}},
	}
}

func (am AppModule) LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	kvWithSubsystem := append([]interface{}{"subsystem", subSystem.String()}, keyvals...)
	am.keeper.Logger().Info(msg, kvWithSubsystem...)
}

func (am AppModule) LogError(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	kvWithSubsystem := append([]interface{}{"subsystem", subSystem.String()}, keyvals...)
	am.keeper.Logger().Error(msg, kvWithSubsystem...)
}

func (am AppModule) LogWarn(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	kvWithSubsystem := append([]interface{}{"subsystem", subSystem.String()}, keyvals...)
	am.keeper.Logger().Warn(msg, kvWithSubsystem...)
}

func (am AppModule) LogDebug(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	kvWithSubsystem := append([]interface{}{"subsystem", subSystem.String()}, keyvals...)
	am.keeper.Logger().Debug(msg, kvWithSubsystem...)
}

// initiateBLSKeyGeneration calls the BLS module to start DKG for the new epoch
func (am AppModule) InitiateBLSKeyGeneration(ctx context.Context, epochID uint64, activeParticipants []*types.ActiveParticipant) {
	if len(activeParticipants) == 0 {
		am.LogWarn("No active participants for BLS key generation", types.EpochGroup, "epochID", epochID)
		return
	}

	// Convert ActiveParticipants to ParticipantWithWeightAndKey format expected by BLS module
	finalizedParticipants := make([]blstypes.ParticipantWithWeightAndKey, 0, len(activeParticipants))

	// Calculate total weight
	totalWeight := int64(0)
	for _, p := range activeParticipants {
		totalWeight += p.Weight
	}

	if totalWeight == 0 {
		am.LogError("Total weight is zero, cannot initiate BLS key generation", types.EpochGroup, "epochID", epochID)
		return
	}

	// Compute adjusted percentages if genesis guardian reservation applies
	adjustedPercentages := ApplyBLSGuardianSlotReservation(ctx, am.keeper, activeParticipants)

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, ap := range activeParticipants {
		accAddr, err := sdk.AccAddressFromBech32(ap.Index)
		if err != nil {
			am.LogError("Failed to parse participant address for BLS key generation", types.EpochGroup, "participantAddress", ap.Index, "epochID", epochID, "error", err)
			continue
		}

		account := am.accountKeeper.GetAccount(sdkCtx, accAddr)
		if account == nil {
			am.LogError("Account not found for BLS participant", types.EpochGroup, "participantAddress", ap.Index, "epochID", epochID)
			continue
		}

		pubKey := account.GetPubKey()
		if pubKey == nil {
			am.LogError("Public key not found for BLS participant account", types.EpochGroup, "participantAddress", ap.Index, "epochID", epochID)
			continue
		}

		secpPubKey, ok := pubKey.(*secp256k1.PubKey)
		if !ok || secpPubKey == nil {
			am.LogError("Participant account public key is not secp256k1 for BLS", types.EpochGroup, "participantAddress", ap.Index, "keyType", pubKey.Type(), "epochID", epochID)
			continue
		}
		pubKeyBytes := secpPubKey.Bytes()
		if len(pubKeyBytes) == 0 {
			am.LogError("Participant secp256k1 public key bytes are empty for BLS", types.EpochGroup, "participantAddress", ap.Index, "epochID", epochID)
			continue
		}

		// Determine percentage weight: use adjusted reservation if present, else raw share
		var percentage math.LegacyDec
		if adjustedPercentages != nil {
			if p, ok := adjustedPercentages[ap.Index]; ok {
				percentage = p
			} else {
				// Participant not present in adjusted map, compute from raw weight
				percentage = math.LegacyNewDec(ap.Weight).Quo(math.LegacyNewDec(totalWeight)).Mul(math.LegacyNewDec(100))
			}
		} else {
			percentage = math.LegacyNewDec(ap.Weight).Quo(math.LegacyNewDec(totalWeight)).Mul(math.LegacyNewDec(100))
		}

		blsParticipant := blstypes.ParticipantWithWeightAndKey{
			Address:            ap.Index,
			PercentageWeight:   percentage,
			Secp256k1PublicKey: pubKeyBytes,
		}
		finalizedParticipants = append(finalizedParticipants, blsParticipant)

		am.LogInfo("Prepared participant for BLS key generation using AccountKeeper PubKey", types.EpochGroup,
			"participant", ap.Index,
			"weight", ap.Weight,
			"percentage", percentage.String(),
			"epochID", epochID,
			"keyLength", len(pubKeyBytes))
	}

	if len(finalizedParticipants) == 0 {
		am.LogError("No valid participants after conversion for BLS key generation", types.EpochGroup, "epochID", epochID)
		return
	}

	// Call the BLS module to initiate key generation
	err := am.keeper.BlsKeeper.InitiateKeyGenerationForEpoch(sdkCtx, epochID, finalizedParticipants)
	if err != nil {
		am.LogError("Failed to initiate BLS key generation", types.EpochGroup, "epochID", epochID, "error", err.Error())
		return
	}

	am.LogInfo("Successfully initiated BLS key generation", types.EpochGroup,
		"epochID", epochID,
		"participantCount", len(finalizedParticipants))
}
