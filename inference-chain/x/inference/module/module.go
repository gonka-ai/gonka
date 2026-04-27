package inference

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"cosmossdk.io/collections"
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
func (AppModule) ConsensusVersion() uint64 { return 14 }

// BeginBlock contains the logic that is automatically triggered at the beginning of each block.
func (am AppModule) BeginBlock(ctx context.Context) error {
	// Precompute SPRT values for the block
	err := am.keeper.PrecomputeSPRTValues(ctx)
	// We continue if there is something wrong with SPRT. Invalidation will effectively be turned off, but
	// this will only happen if the governance values have been set wrong anyhow, so that's a rational choice
	if err != nil {
		am.LogError("Failed to precompute SPRT values", types.Validation, "error", err)
	}

	// Update dynamic pricing for all models at the start of each block
	// This ensures consistent pricing for all inferences processed in this block
	err = am.keeper.UpdateDynamicPricing(ctx)
	if err != nil {
		am.LogError("Failed to update dynamic pricing", types.Pricing, "error", err)
		// Don't return error - allow block processing to continue even if pricing update fails
	}

	// Cache epoch model metadata in transient store.
	// This avoids repeated heavy model-group reads in MsgValidation.
	err = am.keeper.BuildEpochDataTransientCache(ctx)
	if err != nil {
		am.LogError("Failed to build epoch data transient cache", types.Validation, "error", err)
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

// expireInferenceAndIssueRefund marks an inference as expired and issues a refund.
// Returns the updated inference.
func (am AppModule) expireInferenceAndIssueRefund(ctx context.Context, inference types.Inference) types.Inference {
	inference.Status = types.InferenceStatus_EXPIRED
	inference.ActualCost = 0

	err := am.keeper.IssueRefund(ctx, inference.EscrowAmount, inference.RequestedBy, "expired_inference:"+inference.InferenceId)
	if err != nil {
		am.LogError("Error issuing refund", types.Inferences, "error", err, "inferenceId", inference.InferenceId)
	}

	err = am.keeper.SetInference(ctx, inference)
	if err != nil {
		am.LogError("Error updating inference", types.Inferences, "error", err, "inferenceId", inference.InferenceId)
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
	hasNode := am.HasNodeForModel(ctx, inference.AssignedTo, inference.Model, checkPreserveNode, activeParticipants)

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
//
// Error handling philosophy:
//   - UNRECOVERABLE (return err, halts chain): Missing params (line 357), failed epoch
//     state writes -- SetEffectiveEpochIndex (line 443), SetEpoch (line 452),
//     CreateEpochGroup (line 459), CreateGroup (line 464). These mean the chain cannot
//     advance to the next epoch and would be in an inconsistent state if we continued.
//   - RECOVERABLE (log + continue): Inference expiry failures, pruning errors, upgrade
//     tracking errors, compute result errors, confirmation PoC failures. These affect
//     individual operations but the chain can safely continue without them.
//   - CROSS-MODULE (log + continue): Collateral AdvanceEpoch, StreamVesting AdvanceEpoch,
//     BLS key generation. Failures here should not block the inference module's epoch
//     transition.
//
// Sub-functions (onEndOfPoCValidationStage, onSetNewValidatorsStage) handle errors
// internally with log+return patterns. They do NOT propagate errors to EndBlock.
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
		// UNRECOVERABLE: Missing params means chain state is corrupt or uninitialized.
		// Cannot proceed with epoch processing without params.
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

	am.processFinishedInferencesInBlock(ctx, blockHeight, currentEpoch, currentEpochGroup, &params)

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
	//
	// NOTE: Validator activation is intentionally delayed by two blocks: the new validator
	// set becomes active at H+2, not H+1. This provides a buffer for nodes to
	// prepare before the validator set rotates.

	if epochContext.IsEndOfPoCValidationStage(blockHeight) {
		am.LogInfo("StartStage:onEndOfPoCValidationStage", types.Stages, "blockHeight", blockHeight)
		am.onEndOfPoCValidationStage(ctx, blockHeight, blockTime)
	}

	if epochContext.IsSetNewValidatorsStage(blockHeight) {
		am.LogInfo("StartStage:onSetNewValidatorsStage", types.Stages, "blockHeight", blockHeight)
		am.onSetNewValidatorsStage(ctx, blockHeight, blockTime)
		// UNRECOVERABLE: Failed to set effective epoch index means the chain would
		// continue on the old epoch indefinitely, processing stale data.
		if err := am.keeper.SetEffectiveEpochIndex(ctx, getNextEpochIndex(*currentEpoch)); err != nil {
			return err
		}
		am.LogInfo("Epoch index flipped; new validator set activates at H+2",
			types.Stages,
			"blockHeight", blockHeight,
			"expectedActivation", blockHeight+2,
		)
	}

	if epochContext.IsStartOfPocStage(blockHeight) {
		upcomingEpoch := createNewEpoch(*currentEpoch, blockHeight)
		// UNRECOVERABLE: SetEpoch failure means the upcoming epoch cannot be persisted.
		// Without a valid epoch record, all subsequent epoch processing would be invalid.
		err = am.keeper.SetEpoch(ctx, upcomingEpoch)
		if err != nil {
			am.LogError("Unable to set upcoming epoch", types.EpochGroup, "error", err.Error())
			return err
		}

		am.LogInfo("StartStage:PocStart", types.Stages, "blockHeight", blockHeight)
		// UNRECOVERABLE: CreateEpochGroup failure means the DKG/BLS group for the new
		// epoch cannot be formed. Validators would have no group to sign under.
		newGroup, err := am.keeper.CreateEpochGroup(ctx, uint64(blockHeight), upcomingEpoch.Index)
		if err != nil {
			am.LogError("Unable to create epoch group", types.EpochGroup, "error", err.Error())
			return err
		}
		// UNRECOVERABLE: CreateGroup failure means the DKG group record cannot be
		// persisted. The epoch group exists but has no underlying signing group.
		err = newGroup.CreateGroup(ctx)
		if err != nil {
			am.LogError("Unable to create epoch group", types.EpochGroup, "error", err.Error())
			return err
		}
		err = am.initializeUpcomingEpochModelGroups(ctx, newGroup)
		if err != nil {
			am.LogError("Unable to initialize epoch sub-groups", types.EpochGroup, "error", err.Error())
			return err
		}

		modelAssigner := NewModelAssigner(am.keeper, am.keeper)
		preservedSnapshot, err := modelAssigner.SamplePreservedForEpisode(ctx, *currentEpoch, upcomingEpoch.PocStartBlockHeight)
		if err != nil {
			// Downstream readers (broker filter, claim validation) soft-fail on a missing
			// preserved snapshot, so a transient sampler failure should not halt the chain.
			am.LogError("Unable to sample preserved nodes for regular PoC", types.PoC,
				"pocStartBlockHeight", upcomingEpoch.PocStartBlockHeight, "error", err.Error())
		} else if err := am.captureGenerationStartTimestamp(ctx, blockTime, upcomingEpoch.PocStartBlockHeight, preservedSnapshot); err != nil {
			am.LogError("Unable to store generation start snapshots for regular PoC", types.PoC,
				"pocStartBlockHeight", upcomingEpoch.PocStartBlockHeight,
				"error", err.Error())
		}
	}

	// Capture the pre-eligibility snapshot at start_poc - deploy_window.
	if params.DelegationParams != nil &&
		epochContext.IsDelegationSnapshotHeight(blockHeight, params.DelegationParams.DeployWindow) {
		am.captureBootstrapDelegationSnapshot(ctx, blockHeight)
	}

	// Capture validation snapshot at poc_validation_start for deterministic sampling
	if epochContext.IsStartOfPoCValidationStage(blockHeight) {
		upcomingEpoch, found := am.keeper.GetUpcomingEpoch(ctx)
		if found && upcomingEpoch != nil {
			am.captureDelegationSnapshot(ctx, blockHeight, upcomingEpoch.PocStartBlockHeight)
			am.captureValidationSnapshot(ctx, blockHeight, upcomingEpoch.PocStartBlockHeight, "regular PoC")
		} else {
			am.LogError("captureValidationSnapshot: Unable to get upcoming epoch", types.PoC)
		}
	}

	// Intentionally operates on the previous epoch's group here; the new group
	// triggers SetComputeValidators on the next block. See activation note above.
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

func (am AppModule) initializeUpcomingEpochModelGroups(ctx context.Context, parentGroup *epochgroup.EpochGroup) error {
	if parentGroup == nil {
		return nil
	}

	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return err
	}

	for _, modelConfig := range params.PocParams.GetModelConfigs() {
		if modelConfig == nil || modelConfig.ModelId == "" {
			continue
		}

		model, found := am.keeper.GetGovernanceModel(ctx, modelConfig.ModelId)
		if !found || model == nil {
			return fmt.Errorf("poc model %q missing from governance models", modelConfig.ModelId)
		}

		if _, err := parentGroup.CreateSubGroup(ctx, model); err != nil {
			return err
		}
	}

	return nil
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
			sdkCtx := sdk.UnwrapSDKContext(ctx)
			sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
				"epoch_error",
				sdk.NewAttribute("stage", "advance_collateral_epoch"),
				sdk.NewAttribute("epoch", fmt.Sprintf("%d", effectiveEpoch.Index)),
				sdk.NewAttribute("error_category", "cross_module"),
			))
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
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			"epoch_error",
			sdk.NewAttribute("stage", "settle_accounts"),
			sdk.NewAttribute("epoch", fmt.Sprintf("%d", effectiveEpoch.Index)),
			sdk.NewAttribute("error_category", "settlement"),
		))
	}

	upcomingEpoch, found := am.keeper.GetUpcomingEpoch(ctx)
	if !found || upcomingEpoch == nil {
		am.LogError("onEndOfPoCValidationStage: Unable to get upcoming epoch group", types.EpochGroup)
		return
	}

	activeParticipants := am.ComputeNewWeights(ctx, *upcomingEpoch)
	if activeParticipants == nil {
		am.LogError("onEndOfPoCValidationStage: computeResult == nil && activeParticipants == nil", types.PoC)
		return
	}

	modelAssigner := NewModelAssigner(am.keeper, am.keeper)
	modelAssigner.setModelsForParticipants(ctx, activeParticipants, *upcomingEpoch)

	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		am.LogError("onEndOfPoCValidationStage: Unable to get params", types.PoC, "error", err)
		return
	}

	participationState, err := am.prepareEpochParticipationState(
		ctx,
		activeParticipants,
		params,
		upcomingEpoch.PocStartBlockHeight,
	)
	if err != nil {
		am.LogError("onEndOfPoCValidationStage: failed to prepare participation state", types.PoC, "error", err)
		return
	}

	// Compute consensus weights with caps applied and write to participants
	consensusWeights, groupSummaries := participationState.calculator.ComputeConsensusWeights(participationState.eligibleModels)
	for _, p := range activeParticipants {
		p.Weight = consensusWeights[p.Index]
	}

	// Delegation and bootstrap penalties are accumulated additively across all
	// models and applied once, capped at 1.0.
	adjParams := am.delegationAdjustmentParams(params)
	penaltyStartEpochByModel := modelPenaltyStartEpochs(params.PocParams)
	acc := NewPenaltyAccumulator(activeParticipants)
	AccumulateDelegationPenalties(
		acc,
		participationState.calculator,
		participationState.eligibleModels,
		participationState.participationByModel,
		adjParams,
		upcomingEpoch.Index,
		penaltyStartEpochByModel,
	)
	AccumulateBootstrapPenalties(
		acc,
		participationState.bootstrapPenaltyByModel,
		participationState.eligibleModels,
		adjParams,
		upcomingEpoch.Index,
		penaltyStartEpochByModel,
	)
	acc.Apply(activeParticipants)

	afterPenalty := make(map[string]int64, len(activeParticipants))
	for _, p := range activeParticipants {
		afterPenalty[p.Index] = p.Weight
	}

	// Adjust weights based on collateral after the grace period. This modifies the weights in-place.
	if err := am.keeper.AdjustWeightsByCollateral(ctx, activeParticipants); err != nil {
		am.LogError("onSetNewValidatorsStage: failed to adjust weights by collateral", types.Tokenomics, "error", err)
		// Depending on chain policy, we might want to halt on error. For now, we log and continue,
		// which means participants will proceed with their unadjusted PotentialWeight.
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		sdkCtx.EventManager().EmitEvent(sdk.NewEvent(
			"epoch_error",
			sdk.NewAttribute("stage", "adjust_weights_by_collateral"),
			sdk.NewAttribute("error_category", "cross_module"),
		))
	}

	// Apply universal power capping to epoch powers
	activeParticipants = am.applyEpochPowerCapping(ctx, activeParticipants)

	// Write per-model voting powers to ActiveParticipant for visibility.
	// Pass the governance-controlled per-model concentration cap, which
	// defaults to zero (disabled) until governance sets a concrete value.
	am.computeAndSetVotingPowers(
		activeParticipants,
		participationState.calculator,
		participationState.eligibleModels,
		participationState.participationByModel,
		am.delegationVotingPowerCapParams(params),
	)

	emitWeightPipelineLogs(am, upcomingEpoch.Index, groupSummaries,
		participationState.eligibleModels, activeParticipants,
		participationState.participationByModel,
		consensusWeights, afterPenalty, acc)

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
	if upcomingEpoch.Index > 3 {
		outOfDateActiveParticipants := collections.NewPrefixedPairRange[uint64, sdk.AccAddress](upcomingEpoch.Index - 2)
		err = am.keeper.ActiveParticipantsSet.Clear(ctx, outOfDateActiveParticipants)
		if err != nil {
			am.LogWarn("onEndOfPoCValidationStage: Unable to clear old active participants cache", types.EpochGroup, "epochIndex", upcomingEpoch.Index-2, "error", err.Error())
		}
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

	// Cleanup: delete consumed PoCRefusal and PoCDirectIntent entries
	if err := am.keeper.DeleteAllPoCRefusals(ctx); err != nil {
		am.LogWarn("onEndOfPoCValidationStage: failed to clear PoC refusals", types.PoC, "error", err)
	}
	if err := am.keeper.DeleteAllPoCDirectIntents(ctx); err != nil {
		am.LogWarn("onEndOfPoCValidationStage: failed to clear PoC direct intents", types.PoC, "error", err)
	}
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
// The new validator set becomes active at H+2.
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

	// The validation snapshot is only needed during PoC validation of this epoch and
	// can be dropped now. The preserved-nodes snapshot must survive until settlement
	// and claims for this epoch are done -- the Prune pass reclaims it later.
	if err := am.keeper.DeletePoCValidationSnapshot(ctx, upcomingEpoch.PocStartBlockHeight); err != nil {
		am.LogWarn("onSetNewValidatorsStage: Failed to delete validation snapshot", types.PoC,
			"pocStartBlockHeight", upcomingEpoch.PocStartBlockHeight, "error", err)
	}
}

func (am AppModule) captureGenerationStartTimestamp(
	ctx context.Context,
	blockTime, pocStartBlockHeight int64,
	preservedSnapshot types.PreservedNodesSnapshot,
) error {
	validationSnapshot := types.PoCValidationSnapshot{
		PocStageStartHeight:      pocStartBlockHeight,
		GenerationStartTimestamp: blockTime,
	}
	if err := am.keeper.SetPoCValidationSnapshot(ctx, validationSnapshot); err != nil {
		am.LogError("captureGenerationStartTimestamp: Failed to store validation snapshot", types.PoC,
			"pocStartBlockHeight", pocStartBlockHeight, "error", err)
		return err
	}
	if err := am.keeper.SetPreservedNodesSnapshot(ctx, preservedSnapshot); err != nil {
		am.LogError("captureGenerationStartTimestamp: Failed to store preserved snapshot", types.PoC,
			"pocStartBlockHeight", pocStartBlockHeight, "error", err)
		return err
	}
	am.LogInfo("captureGenerationStartTimestamp: Stored", types.PoC,
		"pocStartBlockHeight", pocStartBlockHeight,
		"generationStartTimestamp", blockTime)
	return nil
}

// captureValidationSnapshot stores per-model voting powers and app_hash at validation phase start
// for deterministic sampling synchronization between chain and DAPI.
//
// For regular PoC:
// - models already present in AP(N).voting_powers reuse those voting powers
// - new models derive voting powers from bootstrap delegation + AP(N).weight + store commits
// DIRECT = who submitted store commits for the new-model branch.
//
// For confirmation PoC: voting powers come from AP(N).voting_powers (already delegation-resolved
// at epoch formation). DIRECT = who was assigned models in AP(N).
func (am AppModule) captureValidationSnapshot(ctx context.Context, blockHeight, snapshotKey int64, logContext string) {
	baseState := am.getEffectiveValidationBaseState(ctx)
	modelWeights, totalWeight := am.computeStoreCommitVotingPowers(ctx, baseState, snapshotKey, logContext)
	am.writeValidationSnapshot(ctx, blockHeight, snapshotKey, logContext, modelWeights, totalWeight)
}

// captureConfirmationValidationSnapshot is like captureValidationSnapshot but uses
// stored voting powers instead of computing from store commits. Uses the filtered
// path to exclude members removed mid-epoch.
func (am AppModule) captureConfirmationValidationSnapshot(ctx context.Context, blockHeight, snapshotKey int64) {
	baseState := am.getEffectiveValidationBaseState(ctx)
	am.writeValidationSnapshot(ctx, blockHeight, snapshotKey, "confirmation PoC",
		baseState.existingModelVotingPowers, baseState.totalWeight)
}

type effectiveValidationBaseState struct {
	participants              []*types.ActiveParticipant
	weights                   map[string]int64
	totalWeight               int64
	existingModelVotingPowers []*types.ModelVotingPowers
	perModelPocWeights        map[string]map[string]int64 // model_id -> member -> raw pocWeight from N-1
}

// getEffectiveValidationBaseState reads consensus weights and per-model voting
// powers from EpochGroupData, filtered by SDK group membership. Members removed
// mid-epoch (weight set to 0 in SDK group) are excluded because GetGroupMembers
// does not return them.
//
// Epoch 0 has no model-aware voting powers yet.
//
// TODO: upgrade handler must populate ValidationWeight.voting_power in existing
// EpochGroupData from AP.VotingPowers so the first post-upgrade epoch reads
// correct values.
func (am AppModule) getEffectiveValidationBaseState(ctx context.Context) effectiveValidationBaseState {
	epochIndex, found := am.keeper.GetEffectiveEpochIndex(ctx)
	if !found {
		return emptyValidationBaseState()
	}

	if epochIndex == 0 {
		return am.getEpochZeroValidationBaseState(ctx)
	}

	currentGroup, err := am.keeper.GetCurrentEpochGroup(ctx)
	if err != nil || currentGroup == nil {
		am.LogError("getEffectiveValidationBaseState: failed to get current epoch group", types.PoC, "error", err)
		return emptyValidationBaseState()
	}

	rootMembers, err := currentGroup.GetGroupMembers(ctx)
	if err != nil {
		am.LogError("getEffectiveValidationBaseState: failed to get root group members", types.PoC, "error", err)
		return emptyValidationBaseState()
	}
	liveMemberSet := make(map[string]bool, len(rootMembers))
	for _, m := range rootMembers {
		liveMemberSet[m.Member.Address] = true
	}

	rootGroupData := currentGroup.GroupData
	consensusWeights := make(map[string]int64, len(rootGroupData.ValidationWeights))
	totalWeight := int64(0)
	participants := make([]*types.ActiveParticipant, 0, len(rootGroupData.ValidationWeights))
	for _, vw := range rootGroupData.ValidationWeights {
		if vw == nil || !liveMemberSet[vw.MemberAddress] {
			continue
		}
		consensusWeights[vw.MemberAddress] = vw.Weight
		totalWeight += vw.Weight
		participants = append(participants, &types.ActiveParticipant{
			Index:  vw.MemberAddress,
			Weight: vw.Weight,
		})
	}

	modelVPMap := make(map[string]map[string]int64)
	perModelPocWeights := make(map[string]map[string]int64)
	for _, modelID := range rootGroupData.SubGroupModels {
		subGroup, err := currentGroup.GetSubGroup(ctx, modelID)
		if err != nil || subGroup == nil {
			continue
		}
		subMembers, err := subGroup.GetGroupMembers(ctx)
		if err != nil {
			continue
		}
		liveSubSet := make(map[string]bool, len(subMembers))
		for _, m := range subMembers {
			liveSubSet[m.Member.Address] = true
		}
		for _, vw := range subGroup.GroupData.ValidationWeights {
			if vw == nil || !liveSubSet[vw.MemberAddress] {
				continue
			}
			if vw.VotingPower > 0 {
				if modelVPMap[modelID] == nil {
					modelVPMap[modelID] = make(map[string]int64)
				}
				modelVPMap[modelID][vw.MemberAddress] = vw.VotingPower
			}
			if vw.Weight > 0 {
				if perModelPocWeights[modelID] == nil {
					perModelPocWeights[modelID] = make(map[string]int64)
				}
				perModelPocWeights[modelID][vw.MemberAddress] = vw.Weight
			}
		}
	}

	return effectiveValidationBaseState{
		participants:              participants,
		weights:                   consensusWeights,
		totalWeight:               totalWeight,
		existingModelVotingPowers: modelVPMapToSlice(modelVPMap),
		perModelPocWeights:        perModelPocWeights,
	}
}

func modelVPMapToSlice(modelVPMap map[string]map[string]int64) []*types.ModelVotingPowers {
	modelWeights := make([]*types.ModelVotingPowers, 0, len(modelVPMap))
	for modelID, vps := range modelVPMap {
		modelWeights = append(modelWeights, &types.ModelVotingPowers{
			ModelId:      modelID,
			VotingPowers: types.VotingPowerMapToSlice(vps),
		})
	}
	slices.SortFunc(modelWeights, func(a, b *types.ModelVotingPowers) int {
		return cmp.Compare(a.ModelId, b.ModelId)
	})
	return modelWeights
}

func emptyValidationBaseState() effectiveValidationBaseState {
	return effectiveValidationBaseState{
		weights: map[string]int64{},
	}
}

// computeStoreCommitVotingPowers builds validation-time voting powers by combining:
// - existing model voting powers from the provided base state
// - bootstrap-model voting powers derived from bootstrap delegation + consensus weights + store commits
func (am AppModule) computeStoreCommitVotingPowers(ctx context.Context, baseState effectiveValidationBaseState, snapshotKey int64, logContext string) ([]*types.ModelVotingPowers, int64) {
	consensusWeights := baseState.weights
	totalNetworkWeight := baseState.totalWeight
	if totalNetworkWeight == 0 {
		return nil, 0
	}

	mergedValidationVotingPowers := make(map[string]map[string]int64, len(baseState.existingModelVotingPowers))
	for _, mvw := range baseState.existingModelVotingPowers {
		mergedValidationVotingPowers[mvw.ModelId] = types.VotingPowerSliceToMap(mvw.VotingPowers)
	}

	_, bootstrapDelegations, _, found := am.loadBootstrapSnapshotState(ctx)
	if !found {
		am.LogError("computeStoreCommitVotingPowers: bootstrap delegation snapshot not found", types.PoC,
			"context", logContext)
		bootstrapDelegations = map[string]map[string]string{}
	}

	allStoreCommits, err := am.keeper.GetAllPoCV2StoreCommitsForStage(ctx, snapshotKey)
	if err != nil {
		am.LogError("computeStoreCommitVotingPowers: Failed to get store commits", types.PoC,
			"context", logContext, "error", err)
		return nil, totalNetworkWeight
	}
	// Bootstrap intent is frozen at start_poc - deploy_window. If a participant
	// switches from intent to delegation after that snapshot, the late delegation
	// is intentionally ignored for the current bootstrap-model validation path.
	bootstrapModelStoreCommitKeys := make([]types.PoCParticipantModelKey, 0, len(allStoreCommits))
	for _, key := range sortedStoreCommitKeys(allStoreCommits) {
		if _, alreadyActive := mergedValidationVotingPowers[key.ModelID]; alreadyActive {
			continue
		}
		bootstrapModelStoreCommitKeys = append(bootstrapModelStoreCommitKeys, key)
	}

	bootstrapModelVotingPowers := ComputeModelVotingPowers(bootstrapModelStoreCommitKeys, consensusWeights, bootstrapDelegations)
	for _, modelID := range sortedKeys(bootstrapModelVotingPowers) {
		vps := bootstrapModelVotingPowers[modelID]
		mergedValidationVotingPowers[modelID] = vps
	}
	if len(allStoreCommits) > 0 && len(mergedValidationVotingPowers) == 0 {
		am.LogError("computeStoreCommitVotingPowers: store commits exist but validation snapshot has no models", types.PoC,
			"context", logContext,
			"numStoreCommits", len(allStoreCommits),
		)
	}

	var modelWeights []*types.ModelVotingPowers
	for modelID, vps := range mergedValidationVotingPowers {
		modelWeights = append(modelWeights, &types.ModelVotingPowers{
			ModelId:      modelID,
			VotingPowers: types.VotingPowerMapToSlice(vps),
		})
	}
	slices.SortFunc(modelWeights, func(a, b *types.ModelVotingPowers) int {
		return cmp.Compare(a.ModelId, b.ModelId)
	})
	return modelWeights, totalNetworkWeight
}

// writeValidationSnapshot writes a PoCValidationSnapshot with the given voting powers.
func (am AppModule) writeValidationSnapshot(
	ctx context.Context, blockHeight, snapshotKey int64, logContext string,
	modelWeights []*types.ModelVotingPowers, totalWeight int64,
) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockTime := sdkCtx.BlockTime().Unix()

	var generationStartTimestamp int64
	existingSnapshot, found, _ := am.keeper.GetPoCValidationSnapshot(ctx, snapshotKey)
	if found {
		generationStartTimestamp = existingSnapshot.GenerationStartTimestamp
	}

	snapshot := types.PoCValidationSnapshot{
		PocStageStartHeight:      snapshotKey,
		SnapshotHeight:           blockHeight,
		AppHash:                  hex.EncodeToString(sdkCtx.HeaderInfo().AppHash),
		ModelVotingPowers:        modelWeights,
		TotalNetworkWeight:       totalWeight,
		GenerationStartTimestamp: generationStartTimestamp,
		ExchangeEndTimestamp:     blockTime,
	}

	if err := am.keeper.SetPoCValidationSnapshot(ctx, snapshot); err != nil {
		am.LogError("writeValidationSnapshot: Failed to store", types.PoC,
			"context", logContext, "error", err)
		return
	}

	am.LogInfo("writeValidationSnapshot: Stored validation snapshot", types.PoC,
		"context", logContext,
		"snapshotKey", snapshotKey,
		"snapshotHeight", blockHeight,
		"numModels", len(modelWeights),
		"totalNetworkWeight", totalWeight,
		"exchangeEndTimestamp", blockTime,
	)
}

func (am AppModule) addEpochMembers(ctx context.Context, upcomingEg *epochgroup.EpochGroup, activeParticipants []*types.ActiveParticipant) {
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		am.LogError("addEpochMembers: Unable to get params", types.EpochGroup, "error", err.Error())
		return
	}
	validationParams := params.ValidationParams
	coefficients := ModelCoefficients(params.PocParams)

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

		// Confirmation events can only lower ConfirmationWeight via min-take, never raise it.
		initialConfirmationWeight := epochgroup.CalculateMLNodesTotalWeight(p.Models, p.MlNodes, coefficients)
		member := epochgroup.NewEpochMemberFromActiveParticipant(p, reputation, initialConfirmationWeight, coefficients)
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
	cmd.AddCommand(SettleDevshardEscrowCmd())

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

	StoreService          store.KVStoreService
	TransientStoreService store.TransientStoreService
	Cdc                   codec.Codec
	Config                *modulev1.Module
	Logger                log.Logger

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
	BlsHooks        blstypes.BlsHooksWrapper
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
		in.TransientStoreService,
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
		BlsHooks:        blstypes.BlsHooksWrapper{BlsHooks: NewBlsHooks(k)},
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

	// Fetch BLS params to compute maximum allowed warm keys per participant
	blsParams, err := am.keeper.BlsKeeper.GetParams(ctx)
	var maxAdditionalKeys int
	if err != nil || blsParams.ITotalSlots == 0 {
		am.LogError("Failed to get BLS params or ITotalSlots is zero, defaulting to minimal allowed keys", types.EpochGroup, "epochID", epochID, "error", err)
		maxAdditionalKeys = 0
	} else {
		// Cap keys so that slotCount * (1 + additionalKeys) <= MaxEncryptedSharesPerParticipantCount.
		// Since slotCount can be at most ITotalSlots, dividing the max limit by ITotalSlots gives
		// the safe upper bound per slot.
		maxKeysPerSlot := blstypes.MaxEncryptedSharesPerParticipantCount / int(blsParams.ITotalSlots)
		maxAdditionalKeys = maxKeysPerSlot - 1
		if maxAdditionalKeys < 0 {
			maxAdditionalKeys = 0 // Paranoia fallback
		}
	}

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
		additionalPubKeys := am.collectAdditionalBLSParticipantPubKeys(ctx, ap.Index, pubKeyBytes, maxAdditionalKeys)

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
			Address:                    ap.Index,
			PercentageWeight:           percentage,
			Secp256k1PublicKey:         pubKeyBytes,
			AllowedSecp256k1PublicKeys: additionalPubKeys,
		}
		finalizedParticipants = append(finalizedParticipants, blsParticipant)

		am.LogInfo("Prepared participant for BLS key generation using AccountKeeper PubKey", types.EpochGroup,
			"participant", ap.Index,
			"weight", ap.Weight,
			"percentage", percentage.String(),
			"epochID", epochID,
			"keyLength", len(pubKeyBytes),
			"additionalKeyCount", len(additionalPubKeys))
	}

	if len(finalizedParticipants) == 0 {
		am.LogError("No valid participants after conversion for BLS key generation", types.EpochGroup, "epochID", epochID)
		return
	}

	// Call the BLS module to initiate key generation
	err = am.keeper.BlsKeeper.InitiateKeyGenerationForEpoch(sdkCtx, epochID, finalizedParticipants)
	if err != nil {
		am.LogError("Failed to initiate BLS key generation", types.EpochGroup, "epochID", epochID, "error", err.Error())
		return
	}

	am.LogInfo("Successfully initiated BLS key generation", types.EpochGroup,
		"epochID", epochID,
		"participantCount", len(finalizedParticipants))
}

func (am AppModule) collectAdditionalBLSParticipantPubKeys(ctx context.Context, participantAddress string, primaryPubKey []byte, maxAdditionalKeys int) [][]byte {
	const blsDealerPartMsgTypeURL = "/inference.bls.MsgSubmitDealerPart"

	resp, err := am.keeper.GranteesByMessageType(ctx, &types.QueryGranteesByMessageTypeRequest{
		GranterAddress: participantAddress,
		MessageTypeUrl: blsDealerPartMsgTypeURL,
	})
	if err != nil {
		am.LogWarn("Failed to query BLS grantee keys, falling back to primary key only", types.EpochGroup,
			"participant", participantAddress,
			"messageType", blsDealerPartMsgTypeURL,
			"error", err)
		return nil
	}

	seen := make(map[string]struct{}, len(resp.Grantees))
	additionalPubKeys := make([][]byte, 0, len(resp.Grantees))
	for _, grantee := range resp.Grantees {
		if grantee == nil || grantee.PubKey == "" {
			continue
		}

		pubKeyBytes, err := base64.StdEncoding.DecodeString(grantee.PubKey)
		if err != nil {
			am.LogWarn("Skipping invalid grantee public key encoding for BLS snapshot", types.EpochGroup,
				"participant", participantAddress,
				"grantee", grantee.Address,
				"error", err)
			continue
		}
		if len(pubKeyBytes) != 33 {
			am.LogWarn("Skipping invalid grantee secp256k1 public key length for BLS snapshot", types.EpochGroup,
				"participant", participantAddress,
				"grantee", grantee.Address,
				"length", len(pubKeyBytes))
			continue
		}
		if pubKeyBytes[0] != 0x02 && pubKeyBytes[0] != 0x03 {
			am.LogWarn("Skipping invalid grantee secp256k1 public key prefix for BLS snapshot", types.EpochGroup,
				"participant", participantAddress,
				"grantee", grantee.Address,
				"prefix", fmt.Sprintf("0x%x", pubKeyBytes[0]))
			continue
		}
		if bytes.Equal(pubKeyBytes, primaryPubKey) {
			continue
		}

		keyID := string(pubKeyBytes)
		if _, exists := seen[keyID]; exists {
			continue
		}
		seen[keyID] = struct{}{}
		additionalPubKeys = append(additionalPubKeys, append([]byte(nil), pubKeyBytes...))
	}

	// Keep deterministic key ordering so ciphertext index mapping stays aligned across chain and DAPI
	slices.SortFunc(additionalPubKeys, func(a, b []byte) int {
		return bytes.Compare(a, b)
	})

	if len(additionalPubKeys) > maxAdditionalKeys {
		am.LogWarn("Pruning excess additional BLS participant public keys", types.EpochGroup,
			"participant", participantAddress,
			"found", len(additionalPubKeys),
			"max_allowed", maxAdditionalKeys)
		additionalPubKeys = additionalPubKeys[:maxAdditionalKeys]
	}

	return additionalPubKeys
}
