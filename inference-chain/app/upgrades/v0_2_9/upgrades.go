package v0_2_9

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

const preservedModelId = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"

// allowedTransferAgents is the list of bech32 addresses allowed to act as Transfer Agents.
var allowedTransferAgents = []string{
	"gonka1y2a9p56kv044327uycmqdexl7zs82fs5ryv5le",
	"gonka1dkl4mah5erqggvhqkpc8j3qs5tyuetgdy552cp",
	"gonka1kx9mca3xm8u8ypzfuhmxey66u0ufxhs7nm6wc5",
	"gonka1ddswmmmn38esxegjf6qw36mt4aqyw6etvysy5x",
	"gonka10fynmy2npvdvew0vj2288gz8ljfvmjs35lat8n",
	"gonka1v8gk5z7gcv72447yfcd2y8g78qk05yc4f3nk4w",
	"gonka1gndhek2h2y5849wf6tmw6gnw9qn4vysgljed0u",
}

var toRemoveFromParticipantAllowlist = []string{
	"gonka1slnscxc26qm7vvpn5qs9yzq9n0l82r4ywkl054",
	"gonka1u60q4n3ymcglvhglylchpyvc04kvvurl967mgk",
	"gonka19m47kxgeecyvl4xll882zxwcc7hva5slfxnvq5",
	"gonka1z4ldfav9tl7x3w9aqfry89zd0kt7sa2lhff6te",
	"gonka1vttxzd68fur26l9lew5netz6wxnx7hmmzdkqrn",
	"gonka1s5vtmnn8tqvqfh7gd9hv48kkt2t4mkh7s85zh6",
	"gonka15ye48c3rp57hwt9t6tzc6h0t5sh88zvj0yznwn",
	"gonka178p3vw9zxs885l4a29g3sep0v9uplfq4pzvmrq",
	"gonka1l4vfzkwd555pvxqzr3ksphgwgv8xsh89ytmjp0",
	"gonka1qmpm8tsynnfkqe9902dd6ny4lc5hvp8q7wkrpc",
	"gonka1zv89w02sdfzwy74s9vcthk38dnfsw36s4h7s93",
	"gonka199lgrq8l9xcqqnr0agajzl78c4dpfvwnsc4elm",
	"gonka1fxdt48vp78uxa7apuuamv4clwafxagnjg9eulc",
	"gonka1v5ggga7lslfg2e57m9anxud40v2s4t9dw8yj68",
	"gonka1d0cekvf368psxff6ur7pl42vvs225rzu9a76nj",
	"gonka1000rv0ddp9yk6djr4y29mt0gu3m4p8mg4kmjcv",
	"gonka1000xjetteyu7gy726vnhdxq69y3meq04gvnk4d",
	"gonka1000xmydnfvphwy4n5yww4ex6nwk9mqslf2gnhs",
	"gonka1nkmwp905ka4dzvuwvdaudgjj7yjk8h5aslfw73",
	"gonka14a0856a3q78pmesxpc946xm9nsj3f275l9f5pa",
	"gonka1hkz4qdpl2z4zvlyt6mxfsx04yxfp7sjhnyu2xn",
	"gonka1l44nacmpmt83kavvevmhlh8elspjtjn6wvwzm0",
	"gonka1lecmns7dj5wm8f53pe6c8nueukqafknel2wt2m",
	"gonka1lmgutdlel0fny79zvlsh9aw3c0dw75gtscka3g",
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.Logger().Info("starting upgrade to " + UpgradeName)

		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		removeNonQwen235BModels(ctx, k)
		setAllowedTransferAgents(ctx, k)
		setParticipantAccessParams(ctx, k)
		enablePocV2(ctx, k)
		removeFromParticipantAllowlist(ctx, k)
		resetPocSlotsForEffectiveEpoch(ctx, k)
		resetPocSlotsInEpochGroupData(ctx, k)

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.Logger().Info("successfully upgraded to " + UpgradeName)
		return toVM, nil
	}
}

func removeNonQwen235BModels(ctx context.Context, k keeper.Keeper) {
	models, err := k.GetGovernanceModels(ctx)
	if err != nil {
		k.LogError("failed to get governance models during upgrade", types.Upgrades, "error", err)
		return
	}

	for _, model := range models {
		if model.Id != preservedModelId {
			k.DeleteGovernanceModel(ctx, model.Id)
			k.LogInfo("removed governance model", types.Upgrades, "model_id", model.Id)
		}
	}
}

func setAllowedTransferAgents(ctx context.Context, k keeper.Keeper) {
	if len(allowedTransferAgents) == 0 {
		k.LogInfo("no allowed transfer agents configured, skipping TA whitelist setup", types.Upgrades)
		return
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("failed to get params during upgrade", types.Upgrades, "error", err)
		return
	}

	params.TransferAgentAccessParams = &types.TransferAgentAccessParams{
		AllowedTransferAddresses: allowedTransferAgents,
	}

	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set params with transfer agent whitelist", types.Upgrades, "error", err)
		return
	}

	k.LogInfo("set allowed transfer agents", types.Upgrades, "count", len(allowedTransferAgents))
}

func enablePocV2(ctx context.Context, k keeper.Keeper) {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("failed to get params during upgrade", types.Upgrades, "error", err)
		return
	}

	if params.PocParams == nil {
		k.LogError("poc params not initialized", types.Upgrades)
		return
	}

	params.PocParams.PocV2Enabled = true
	params.PocParams.ConfirmationPocV2Enabled = true
	params.PocParams.WeightScaleFactor = &types.Decimal{Value: 262, Exponent: -3}

	if params.EpochParams == nil {
		k.LogError("epoch params not initialized", types.Upgrades)
		return
	}
	params.EpochParams.InferenceValidationCutoff = 2
	params.EpochParams.PocValidationDuration = 480

	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set params with poc v2 enabled", types.Upgrades, "error", err)
		return
	}

	k.LogInfo("enabled POC v2", types.Upgrades,
		"poc_v2_enabled", params.PocParams.PocV2Enabled,
		"confirmation_poc_v2_enabled", params.PocParams.ConfirmationPocV2Enabled,
		"weight_scale_factor", 0.262,
		"inference_validation_cutoff", params.EpochParams.InferenceValidationCutoff)
}

func setParticipantAccessParams(ctx context.Context, k keeper.Keeper) {
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("failed to get params during upgrade", types.Upgrades, "error", err)
		return
	}

	if params.ParticipantAccessParams == nil {
		params.ParticipantAccessParams = &types.ParticipantAccessParams{}
	}

	params.ParticipantAccessParams.NewParticipantRegistrationStartHeight = 2475000
	params.ParticipantAccessParams.ParticipantAllowlistUntilBlockHeight = 2475000

	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set participant access params", types.Upgrades, "error", err)
		return
	}

	k.LogInfo("set participant access params", types.Upgrades,
		"new_participant_registration_start_height", params.ParticipantAccessParams.NewParticipantRegistrationStartHeight,
		"participant_allowlist_until_block_height", params.ParticipantAccessParams.ParticipantAllowlistUntilBlockHeight)
}

func removeFromParticipantAllowlist(ctx context.Context, k keeper.Keeper) {
	removedCount := 0
	for _, addrStr := range toRemoveFromParticipantAllowlist {
		addr, err := sdk.AccAddressFromBech32(addrStr)
		if err != nil {
			k.LogError("failed to parse address for allowlist removal", types.Upgrades,
				"address", addrStr, "error", err)
			continue
		}

		if err := k.ParticipantAllowListSet.Remove(ctx, addr); err != nil {
			k.LogError("failed to remove address from participant allowlist", types.Upgrades,
				"address", addrStr, "error", err)
			continue
		}
		removedCount++
	}

	k.LogInfo("removed participants from allowlist", types.Upgrades,
		"removed_count", removedCount, "total_requested", len(toRemoveFromParticipantAllowlist))
}

// resetPocSlotsForUpcomingEpoch clears POC_SLOT=true allocations for all nodes in the upcoming epoch.
//
// # Background
//
// Each epoch has a POC (Proof of Compute) phase at its beginning where nodes prove their compute capacity.
// To maintain network availability during POC, some nodes are allocated to continue serving inference
// instead of participating in POC. This is controlled by the TimeslotAllocation field in MLNodeInfo:
//
//	TimeslotAllocation[0] = PRE_POC_SLOT (always true for active nodes)
//	TimeslotAllocation[1] = POC_SLOT (true = serve inference during POC, false = participate in POC)
//
// # Data Structures
//
//	ActiveParticipants (stored per epoch):
//	  └── Participants []*ActiveParticipant
//	        └── MlNodes []*ModelMLNodes
//	              └── MlNodes []*MLNodeInfo
//	                    └── TimeslotAllocation []bool  <-- We reset index [1] to false
//
// # Why This Reset is Needed
//
// When enabling POC V2, we want ALL nodes to participate in POC at the start of the first V2 epoch.
// This ensures:
//  1. Fresh POC data from all nodes for the new V2 system
//  2. No nodes carry over preserved weight without proving their compute
//
// # Timing
//
// This runs during the upgrade, which happens in the inference phase of epoch A:
//
//	Epoch A: [PocStart, PocEnd, SetNewValidators, ...upgrade HERE..., NextPocStart]
//	                              ↓                       ↓                 ↓
//	                    ActiveParticipants(A)      Reset POC_SLOT      POC A+1 reads
//	                    created at PocEnd          in upgrade          ActiveParticipants(A)
//
// During POC A+1, GetPreservedNodesByParticipant reads ActiveParticipants(A) to determine
// which nodes have POC_SLOT=true and should preserve their weight. By resetting
// ActiveParticipants(A) here, all nodes will participate in POC A+1.
func resetPocSlotsForEffectiveEpoch(ctx context.Context, k keeper.Keeper) {
	effectiveEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogWarn("resetPocSlotsForEffectiveEpoch: no effective epoch found, skipping", types.Upgrades)
		return
	}

	participants, found := k.GetActiveParticipants(ctx, effectiveEpochIndex)
	if !found {
		k.LogWarn("resetPocSlotsForEffectiveEpoch: no active participants for effective epoch", types.Upgrades,
			"epoch", effectiveEpochIndex)
		return
	}

	resetCount := 0
	for _, p := range participants.Participants {
		for _, modelMLNodes := range p.MlNodes {
			if modelMLNodes == nil {
				continue
			}
			for _, mlNode := range modelMLNodes.MlNodes {
				if mlNode == nil {
					continue
				}
				// TimeslotAllocation[1] is POC_SLOT: true means node serves inference during POC
				// We set it to false so all nodes participate in POC
				if len(mlNode.TimeslotAllocation) > 1 && mlNode.TimeslotAllocation[1] {
					mlNode.TimeslotAllocation[1] = false
					resetCount++
				}
			}
		}
	}

	if resetCount > 0 {
		if err := k.SetActiveParticipants(ctx, participants); err != nil {
			k.LogError("resetPocSlotsForEffectiveEpoch: failed to save reset allocations", types.Upgrades,
				"error", err)
			return
		}
		k.LogInfo("resetPocSlotsForEffectiveEpoch: reset POC_SLOT allocations for first V2 epoch", types.Upgrades,
			"epoch", effectiveEpochIndex, "nodes_reset", resetCount)
	} else {
		k.LogInfo("resetPocSlotsForEffectiveEpoch: no POC_SLOT allocations to reset", types.Upgrades,
			"epoch", effectiveEpochIndex)
	}
}

// resetPocSlotsInEpochGroupData resets POC_SLOT=true allocations in EpochGroupData for all model subgroups.
//
// # Why This is Needed
//
// The broker reads TimeslotAllocation from EpochGroupData (not ActiveParticipants) to determine
// which nodes should continue serving inference during POC via ShouldContinueInference().
// EpochGroupData is created at EndOfPoCValidationStage BEFORE the upgrade runs, so we must
// also reset it here to ensure the broker sees the correct values.
//
// # Data Structures
//
//	EpochGroupData (stored per epoch + model):
//	  └── ValidationWeights []*ValidationWeight
//	        └── MlNodes []*MLNodeInfo
//	              └── TimeslotAllocation []bool  <-- We reset index [1] to false
//
// Note: The parent EpochGroupData (modelId="") has no MlNodes, only model subgroups do.
func resetPocSlotsInEpochGroupData(ctx context.Context, k keeper.Keeper) {
	effectiveEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogWarn("resetPocSlotsInEpochGroupData: no effective epoch found, skipping", types.Upgrades)
		return
	}

	// Get parent EpochGroupData to find all model subgroups
	parentData, found := k.GetEpochGroupData(ctx, effectiveEpochIndex, "")
	if !found {
		k.LogWarn("resetPocSlotsInEpochGroupData: parent epoch group data not found", types.Upgrades,
			"epoch", effectiveEpochIndex)
		return
	}

	totalResetCount := 0

	// Reset each model subgroup (parent has no MlNodes, only subgroups do)
	for _, modelId := range parentData.SubGroupModels {
		subgroupData, found := k.GetEpochGroupData(ctx, effectiveEpochIndex, modelId)
		if !found {
			k.LogWarn("resetPocSlotsInEpochGroupData: subgroup not found", types.Upgrades,
				"epoch", effectiveEpochIndex, "model", modelId)
			continue
		}

		resetCount := 0
		for _, vw := range subgroupData.ValidationWeights {
			if vw == nil {
				continue
			}
			for _, mlNode := range vw.MlNodes {
				if mlNode == nil {
					continue
				}
				// TimeslotAllocation[1] is POC_SLOT: true means node serves inference during POC
				// We set it to false so all nodes participate in POC
				if len(mlNode.TimeslotAllocation) > 1 && mlNode.TimeslotAllocation[1] {
					mlNode.TimeslotAllocation[1] = false
					resetCount++
				}
			}
		}

		if resetCount > 0 {
			k.SetEpochGroupData(ctx, subgroupData)
			totalResetCount += resetCount
			k.LogInfo("resetPocSlotsInEpochGroupData: reset POC_SLOT in subgroup", types.Upgrades,
				"epoch", effectiveEpochIndex, "model", modelId, "nodes_reset", resetCount)
		}
	}

	if totalResetCount > 0 {
		k.LogInfo("resetPocSlotsInEpochGroupData: reset POC_SLOT in EpochGroupData complete", types.Upgrades,
			"epoch", effectiveEpochIndex, "total_nodes_reset", totalResetCount)
	} else {
		k.LogInfo("resetPocSlotsInEpochGroupData: no POC_SLOT allocations to reset in EpochGroupData", types.Upgrades,
			"epoch", effectiveEpochIndex)
	}
}
