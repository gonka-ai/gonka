package v0_2_13

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/authz"
	authzkeeper "github.com/cosmos/cosmos-sdk/x/authz/keeper"
	govkeeper "github.com/cosmos/cosmos-sdk/x/gov/keeper"
	govv1 "github.com/cosmos/cosmos-sdk/x/gov/types/v1"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

const BountyCommunitySaleContractAddress = "gonka18pkq9mwxxlmyq7kr5txhm060wemg2s4u94wvsfd9w2kdc0u99d6spk8pz2"
const BountyIbcUsdtDenom = "ibc/115F68FBA220A028C6F6ED08EA0C1A9C8C52798B14FB66E6C89D5D8C06A524D4"

// Replacement approved devshard binary registered by the v0.2.13 upgrade.
// The name stays v1 because the replacement binary is fully compatible with
// the existing devshard v1 interface.
const (
	DevshardV1Name   = "v1"
	DevshardV1Binary = "https://github.com/gonka-ai/gonka/releases/download/release%2Fv0.2.13-devshard-v1/devshardd.zip"
	DevshardV1Sha256 = "dad6f1b97843816c0a33874b89ac403e48b54fe3aa1a0fdccb228d89d2a5594c"
)

func USDT(amount int64) int64 {
	return amount * 1_000_000
}

type BountyReward struct {
	Address string
	Amount  int64
}

var bountyRewards = []BountyReward{
	// Prompt of death:
	// report and investigation of prompts causing vLLM crashes around
	// structured outputs / tool handling.
	// Public name: @blizko
	{Address: "gonka12jaf7m4eysyqt32mrgarum6z96vt55tckvcleq", Amount: USDT(8000)},

	// Kimi experiments:
	// report is available at
	// https://github.com/kaitakuai/experiments/blob/main/reports/2026-04-kimi-qwen-experiments.md
	// Public name: kaitaku.ai
	{Address: "gonka1x45hruazmcqxslj3g8a08988hr5fr3wx33drhp", Amount: USDT(10000)},

	// PR #826:
	// Partial Payment on Claim Failure Causes Permanent Reward Loss.
	// Public name: @ouicate
	{Address: "gonka1f0elpwnx7ezytdlck35003nz6qk8kzvurvnj4a", Amount: USDT(500)},

	// PR #826:
	// Underfunded Work Payout Still Removes Settle Amount.
	// Public name: @ouicate
	{Address: "gonka1f0elpwnx7ezytdlck35003nz6qk8kzvurvnj4a", Amount: USDT(375)},
}

var devshardAllowedCreatorAddressesToAdd = []string{
	// Gonka Labs
	"gonka1r2s0rwgskp6y4ed7qr7d25qdwjwlvpp6demv90",

	// Hyperfusion
	"gonka1ls8wqecwj369du8s2t9a223xu9sgvmzlw2ye9c",

	// 6Blocks
	"gonka10wmset95nhgfjt4wklsyjqpx55m40zy3gha2pn",

	//  https://gonkabroker.com/,
	"gonka17ld2g62230w0erzexefzw03sw0adtuchr425rp",
}

const (
	MaxEscrowsPerEpoch uint32 = 500_000
	MaxNonce           uint32 = 1_000_000
	// Block window after the upgrade in which confirmation PoC is skipped.
	// Same value as v0.2.10; covers the rest of the upgrade epoch on mainnet.
	GraceUpgradeProtectionWindow int64 = 10000

	// EthereumChainName is the chain identifier used in bridge registration state.
	EthereumChainName = "ethereum"

	// Well-known Ethereum mainnet token contract addresses (EIP-55 checksummed).
	// These are standard constants and do not need to be passed via Plan.Info.
	USDCContractAddress = "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"
	USDTContractAddress = "0xdAC17F958D2ee523a2206206994597C13D831ec7"

	qwenModelID        = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"
	kimiModelID        = "moonshotai/Kimi-K2.6"
	minimaxModelID     = "MiniMaxAI/MiniMax-M2.7"
	minimaxModelCommit = "d494266a4affc0d2995ba1fa35c8481cbd84294b"
	minimaxStartEpoch  = uint64(278)

	governanceQuorum = "0.25"
)

// BridgeSetupData is parsed from the upgrade proposal's Plan.Info JSON field.
// Both fields are required; the upgrade handler logs a warning and skips bridge
// setup if either is missing.
//
// Example Plan.Info JSON:
//
//	{"ethereum_bridge_address":"0x1234...abcd","wrapped_token_code_id":42}
type BridgeSetupData struct {
	// EthereumBridgeAddress is the deployed BridgeContract address on Ethereum mainnet.
	EthereumBridgeAddress string `json:"ethereum_bridge_address"`

	// WrappedTokenCodeID is the CW20 code ID obtained by running `tx wasm store`
	// with the wrapped_token.wasm artifact before the upgrade.
	WrappedTokenCodeID uint64 `json:"wrapped_token_code_id"`
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	authzKeeper authzkeeper.Keeper,
	govKeeper *govkeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		if err := setDevshardEscrowParams(ctx, k); err != nil {
			return nil, err
		}
		if err := setDevshardAllowedCreatorAddresses(ctx, k); err != nil {
			return nil, err
		}
		if err := setDevshardApprovedVersions(ctx, k); err != nil {
			return nil, err
		}
		if err := backfillConfirmationWeightScales(ctx, k); err != nil {
			return nil, err
		}
		if err := updateModelParams(ctx, k); err != nil {
			return nil, err
		}
		// Reduce genesis guardian adjusted voting power to 25% and set the
		// chain-wide governance quorum to 0.25. Quorum is computed against total
		// bonded power; with guardians (25%) not voting, this gives an effective
		// 1/3 quorum among the remaining 75% of voting power (0.25 / 0.75 = 0.334).
		if err := setGenesisGuardianMultiplier(ctx, k); err != nil {
			return nil, err
		}
		if err := setGovernanceTallyParams(ctx, govKeeper, k); err != nil {
			return nil, err
		}
		if err := grantRespondDealerComplaintsAuthz(ctx, authzKeeper, k); err != nil {
			return nil, err
		}
		if err := disableConfirmationPocForUpgradeEpoch(ctx, k); err != nil {
			return nil, err
		}

		if err := distributeBountyRewards(ctx, k); err != nil {
			return nil, err
		}

		// Register Ethereum bridge infrastructure from Plan.Info parameters.
		if err := executeBridgeSetup(ctx, k, plan.Info); err != nil {
			return nil, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

func setGenesisGuardianMultiplier(ctx context.Context, k keeper.Keeper) error {
	genesisParams, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		return fmt.Errorf("genesis only params not found")
	}
	genesisParams.GenesisGuardianMultiplier = genesisGuardianMultiplier()
	if err := k.SetGenesisOnlyParams(ctx, &genesisParams); err != nil {
		return err
	}
	k.LogInfo("set genesis guardian multiplier", types.Upgrades,
		"genesis_guardian_multiplier", genesisParams.GenesisGuardianMultiplier.ToDecimal().String())
	return nil
}

func setGovernanceTallyParams(ctx context.Context, govKeeper *govkeeper.Keeper, k keeper.Keeper) error {
	params, err := govKeeper.Params.Get(ctx)
	if err != nil {
		return err
	}
	params = applyGovernanceTallyParams(params)
	if err := params.ValidateBasic(); err != nil {
		return err
	}
	if err := govKeeper.Params.Set(ctx, params); err != nil {
		return err
	}
	k.LogInfo("set governance tally params", types.Upgrades, "quorum", params.Quorum)
	return nil
}

// applyGovernanceTallyParams sets the chain-wide governance quorum to 0.25.
// With genesis guardians at 25% adjusted voting power and not voting, this
// gives an effective 1/3 quorum among the remaining 75% of voting power
// (0.25 / 0.75 = 0.334).
func applyGovernanceTallyParams(params govv1.Params) govv1.Params {
	params.Quorum = governanceQuorum
	return params
}

func genesisGuardianMultiplier() *types.Decimal {
	return &types.Decimal{Value: 33334, Exponent: -5}
}

func setDevshardEscrowParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.DevshardEscrowParams == nil {
		params.DevshardEscrowParams = types.DefaultDevshardEscrowParams()
	}
	params.DevshardEscrowParams.MaxEscrowsPerEpoch = MaxEscrowsPerEpoch
	params.DevshardEscrowParams.MaxNonce = MaxNonce
	params.DevshardEscrowParams.DevshardRequestsEnabled = types.DefaultDevshardRequestsEnabled
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("set devshard escrow params", types.Upgrades,
		"max_escrows_per_epoch", MaxEscrowsPerEpoch,
		"max_nonce", MaxNonce,
		"devshard_requests_enabled", types.DefaultDevshardRequestsEnabled)
	return nil
}

func setDevshardAllowedCreatorAddresses(ctx context.Context, k keeper.Keeper) error {
	return addDevshardAllowedCreatorAddresses(ctx, k, devshardAllowedCreatorAddressesToAdd)
}

func addDevshardAllowedCreatorAddresses(ctx context.Context, k keeper.Keeper, addresses []string) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.DevshardEscrowParams == nil {
		params.DevshardEscrowParams = types.DefaultDevshardEscrowParams()
	}

	seen := make(map[string]struct{}, len(params.DevshardEscrowParams.AllowedCreatorAddresses)+len(addresses))
	for _, address := range params.DevshardEscrowParams.AllowedCreatorAddresses {
		seen[address] = struct{}{}
	}

	added := 0
	for _, address := range addresses {
		if _, ok := seen[address]; ok {
			continue
		}
		params.DevshardEscrowParams.AllowedCreatorAddresses = append(params.DevshardEscrowParams.AllowedCreatorAddresses, address)
		seen[address] = struct{}{}
		added++
	}

	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("set devshard allowed creator addresses", types.Upgrades,
		"total", len(params.DevshardEscrowParams.AllowedCreatorAddresses),
		"added", added)
	return nil
}

func setDevshardApprovedVersions(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.DevshardEscrowParams == nil {
		params.DevshardEscrowParams = types.DefaultDevshardEscrowParams()
	}

	version := &types.DevshardApprovedVersion{
		Name:   DevshardV1Name,
		Binary: DevshardV1Binary,
		Sha256: DevshardV1Sha256,
	}

	replaced := false
	for i, existing := range params.DevshardEscrowParams.ApprovedVersions {
		if existing != nil && existing.Name == DevshardV1Name {
			params.DevshardEscrowParams.ApprovedVersions[i] = version
			replaced = true
			break
		}
	}
	if !replaced {
		params.DevshardEscrowParams.ApprovedVersions = append(params.DevshardEscrowParams.ApprovedVersions, version)
	}

	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set devshard approved versions during upgrade", types.Upgrades, "error", err)
		return err
	}
	k.LogInfo("set devshard approved version", types.Upgrades,
		"name", DevshardV1Name,
		"binary", DevshardV1Binary,
		"sha256", DevshardV1Sha256,
		"replaced", replaced)
	return nil
}

func updateModelParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.PocParams == nil {
		return fmt.Errorf("poc params not found")
	}

	updatedKimi := setPoCModelWeightScale(params.PocParams, kimiModelID, kimiWeightScaleFactor())
	upsertPoCModelConfig(params.PocParams, minimaxPoCModelConfig())

	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	setGovernanceModelValidationThreshold(ctx, k, qwenModelID, qwenValidationThreshold())
	updateKimiGovernanceModel(ctx, k)
	k.SetModel(ctx, minimaxGovernanceModel(k.GetAuthority()))
	k.LogInfo("updated model params", types.Upgrades,
		"kimi_model_id", kimiModelID,
		"kimi_updated", updatedKimi,
		"minimax_model_id", minimaxModelID)
	return nil
}

func setGovernanceModelValidationThreshold(
	ctx context.Context,
	k keeper.Keeper,
	modelID string,
	threshold *types.Decimal,
) {
	model, found := k.GetGovernanceModel(ctx, modelID)
	if !found || model == nil {
		return
	}
	model.ValidationThreshold = threshold
	k.SetModel(ctx, model)
}

func updateKimiGovernanceModel(ctx context.Context, k keeper.Keeper) {
	model, found := k.GetGovernanceModel(ctx, kimiModelID)
	if !found || model == nil {
		return
	}
	model.ValidationThreshold = kimiValidationThreshold()
	if !hasModelArg(model.ModelArgs, "--enable-auto-tool-choice") {
		model.ModelArgs = append([]string{"--enable-auto-tool-choice"}, model.ModelArgs...)
	}
	k.SetModel(ctx, model)
}

func hasModelArg(args []string, arg string) bool {
	for _, existing := range args {
		if existing == arg {
			return true
		}
	}
	return false
}

func setPoCModelWeightScale(poc *types.PocParams, modelID string, weightScaleFactor *types.Decimal) bool {
	for _, model := range poc.Models {
		if model == nil || model.ModelId != modelID {
			continue
		}
		model.WeightScaleFactor = weightScaleFactor
		return true
	}
	return false
}

func upsertPoCModelConfig(poc *types.PocParams, config *types.PoCModelConfig) {
	for i, model := range poc.Models {
		if model == nil || model.ModelId != config.ModelId {
			continue
		}
		poc.Models[i] = config
		return
	}
	poc.Models = append(poc.Models, config)
}

func kimiWeightScaleFactor() *types.Decimal {
	return &types.Decimal{Value: 78, Exponent: -2}
}

func qwenValidationThreshold() *types.Decimal {
	return &types.Decimal{Value: 940, Exponent: -3}
}

func kimiValidationThreshold() *types.Decimal {
	return &types.Decimal{Value: 900, Exponent: -3}
}

func minimaxWeightScaleFactor() *types.Decimal {
	return &types.Decimal{Value: 3024, Exponent: -4}
}

func minimaxValidationThreshold() *types.Decimal {
	return &types.Decimal{Value: 922, Exponent: -3}
}

func minimaxPoCModelConfig() *types.PoCModelConfig {
	return &types.PoCModelConfig{
		ModelId: minimaxModelID,
		SeqLen:  1024,
		StatTest: &types.PoCStatTestParams{
			DistThreshold:   &types.Decimal{Value: 75, Exponent: -2}, // 0.75
			PMismatch:       &types.Decimal{Value: 1, Exponent: -1},  // 0.10
			PValueThreshold: &types.Decimal{Value: 5, Exponent: -2},  // 0.05
		},
		WeightScaleFactor: minimaxWeightScaleFactor(),
		PenaltyStartEpoch: minimaxStartEpoch,
	}
}

func minimaxGovernanceModel(authority string) *types.Model {
	return &types.Model{
		ProposedBy:             authority,
		Id:                     minimaxModelID,
		UnitsOfComputePerToken: 10000,
		HfRepo:                 minimaxModelID,
		HfCommit:               minimaxModelCommit,
		ModelArgs: []string{
			"--enable-auto-tool-choice",
			"--max-model-len", "180000",
			"--kv-cache-dtype", "fp8",
			"--tool-call-parser", "minimax_m2",
			"--reasoning-parser", "minimax_m2_append_think",
		},
		VRam:                320,
		ThroughputPerNonce:  5000,
		ValidationThreshold: minimaxValidationThreshold(),
	}
}

func backfillConfirmationWeightScales(ctx context.Context, k keeper.Keeper) error {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogWarn("confirmation weight scales backfill skipped: no effective epoch", types.Upgrades)
		return nil
	}
	root, found := k.GetEpochGroupData(ctx, epochIndex, "")
	if !found {
		k.LogWarn("confirmation weight scales backfill skipped: root epoch group missing", types.Upgrades,
			"epoch", epochIndex)
		return nil
	}
	activeParticipants, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		k.LogWarn("confirmation weight scales backfill skipped: active participants missing", types.Upgrades,
			"epoch", epochIndex)
		return nil
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	confirmableModels := make(map[string]bool)
	for _, groupData := range k.GetAllEpochGroupData(ctx) {
		if groupData.EpochIndex != epochIndex || groupData.ModelId == "" {
			continue
		}
		for _, vw := range groupData.ValidationWeights {
			if vw != nil && vw.VotingPower > 0 {
				confirmableModels[groupData.ModelId] = true
				break
			}
		}
	}

	root.ConfirmationWeightScales = confirmationWeightScalesFromModels(confirmableModels, params.PocParams)
	coefficients := types.ConfirmationWeightCoefficients(root.ConfirmationWeightScales)
	activeByAddress := make(map[string]*types.ActiveParticipant, len(activeParticipants.Participants))
	for _, p := range activeParticipants.Participants {
		if p != nil {
			activeByAddress[p.Index] = p
		}
	}
	for _, vw := range root.ValidationWeights {
		if vw == nil {
			continue
		}
		p := activeByAddress[vw.MemberAddress]
		if p == nil {
			continue
		}
		expected := types.ConfirmationWeightOfParticipantWithCoefficients(p, coefficients)
		if vw.ConfirmationWeight > expected {
			vw.ConfirmationWeight = expected
		}
	}
	k.SetEpochGroupData(ctx, root)
	k.LogInfo("backfilled confirmation weight scales", types.Upgrades,
		"epoch", epochIndex,
		"models", len(root.ConfirmationWeightScales))
	return nil
}

// grantRespondDealerComplaintsAuthz backfills MsgRespondDealerComplaints authz
// grants on every existing cold->warm ML ops pair. v0.2.12 added the message to
// InferenceOperationKeyPerms but did not migrate existing grants, so DAPIs on
// hosts that joined before v0.2.12 cannot respond to dealer complaints until
// they re-run grant-ml-ops-permissions. Identify pairs by an existing
// MsgStartInference grant (the canonical marker) and reuse its expiration.
func grantRespondDealerComplaintsAuthz(ctx context.Context, authzKeeper authzkeeper.Keeper, k keeper.Keeper) error {
	type grantPair struct {
		granter    sdk.AccAddress
		grantee    sdk.AccAddress
		expiration *time.Time
	}
	seen := make(map[string]bool)
	var pairs []grantPair

	startInferenceMsgType := sdk.MsgTypeURL(&types.MsgStartInference{})
	respondMsgType := sdk.MsgTypeURL(&blstypes.MsgRespondDealerComplaints{})

	authzKeeper.IterateGrants(ctx, func(granterAddr, granteeAddr sdk.AccAddress, grant authz.Grant) bool {
		if grant.Authorization.GetTypeUrl() != "/cosmos.authz.v1beta1.GenericAuthorization" {
			return false
		}
		var genAuth authz.GenericAuthorization
		if err := k.Codec().Unmarshal(grant.Authorization.Value, &genAuth); err != nil {
			return false
		}
		if genAuth.Msg != startInferenceMsgType {
			return false
		}
		key := granterAddr.String() + "->" + granteeAddr.String()
		if seen[key] {
			return false
		}
		seen[key] = true
		pairs = append(pairs, grantPair{granter: granterAddr, grantee: granteeAddr, expiration: grant.Expiration})
		return false
	})

	k.LogInfo("found cold->warm pairs needing MsgRespondDealerComplaints grant", types.Upgrades, "count", len(pairs))

	created := 0
	skipped := 0
	for _, pair := range pairs {
		existing, _ := authzKeeper.GetAuthorization(ctx, pair.grantee, pair.granter, respondMsgType)
		if existing != nil {
			skipped++
			continue
		}
		auth := authz.NewGenericAuthorization(respondMsgType)
		if err := authzKeeper.SaveGrant(ctx, pair.grantee, pair.granter, auth, pair.expiration); err != nil {
			k.LogError("failed to save MsgRespondDealerComplaints grant", types.Upgrades,
				"granter", pair.granter.String(),
				"grantee", pair.grantee.String(),
				"error", err)
			continue
		}
		created++
	}
	k.LogInfo("MsgRespondDealerComplaints grant migration complete", types.Upgrades,
		"created", created, "skipped", skipped)
	return nil
}

// disableConfirmationPocForUpgradeEpoch skips confirmation PoC triggers for
// the rest of the upgrade epoch via the v0.2.10 grace-epoch primitive.
func disableConfirmationPocForUpgradeEpoch(ctx context.Context, k keeper.Keeper) error {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogWarn("confirmation PoC grace-epoch skipped: no effective epoch", types.Upgrades)
		return nil
	}
	binomTestP0 := &types.Decimal{Value: 5, Exponent: -1}
	if err := k.AddPunishmentGraceEpoch(ctx, epochIndex, binomTestP0, GraceUpgradeProtectionWindow); err != nil {
		return err
	}
	k.LogInfo("disabled confirmation PoC for upgrade epoch", types.Upgrades,
		"epoch", epochIndex,
		"upgrade_protection_window", GraceUpgradeProtectionWindow)
	return nil
}

func confirmationWeightScalesFromModels(
	models map[string]bool,
	pocParams *types.PocParams,
) []*types.ConfirmationWeightScale {
	coefficients := make(map[string]*types.Decimal)
	for _, config := range pocParams.GetModelConfigs() {
		if config == nil || config.ModelId == "" {
			continue
		}
		coefficients[config.ModelId] = config.WeightScaleFactor
	}

	modelIDs := make([]string, 0, len(models))
	for modelID := range models {
		modelIDs = append(modelIDs, modelID)
	}
	slices.Sort(modelIDs)

	scales := make([]*types.ConfirmationWeightScale, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		scales = append(scales, &types.ConfirmationWeightScale{
			ModelId:           modelID,
			WeightScaleFactor: coefficients[modelID].CloneOrOne(),
		})
	}
	return scales
}

// ---------------------------------------------------------------------------
// Ethereum bridge setup (parsed from Plan.Info)
// ---------------------------------------------------------------------------

// executeBridgeSetup parses Plan.Info and registers the complete Ethereum bridge
// infrastructure: bridge address, token metadata, trading approvals, and wrapped
// token code ID. Skips gracefully when Plan.Info is empty or incomplete.
func executeBridgeSetup(ctx context.Context, k keeper.Keeper, infoJSON string) error {
	if infoJSON == "" {
		k.LogInfo("no bridge setup data in Plan.Info, skipping", types.Upgrades)
		return nil
	}

	var data BridgeSetupData
	if err := json.Unmarshal([]byte(infoJSON), &data); err != nil {
		k.LogError("failed to unmarshal Plan.Info for bridge setup", types.Upgrades,
			"info", infoJSON, "error", err)
		return nil
	}

	if data.EthereumBridgeAddress == "" || data.WrappedTokenCodeID == 0 {
		k.LogInfo("incomplete bridge setup data in Plan.Info, skipping", types.Upgrades,
			"info", infoJSON)
		return nil
	}

	k.LogInfo("executing bridge setup from Plan.Info", types.Upgrades,
		"ethereum_bridge_address", data.EthereumBridgeAddress,
		"wrapped_token_code_id", data.WrappedTokenCodeID)

	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := registerEthereumBridge(sdkCtx, k, data.EthereumBridgeAddress); err != nil {
		return fmt.Errorf("bridge setup: register bridge address: %w", err)
	}

	if err := registerTokenMetadata(sdkCtx, k, USDCContractAddress, "USD Coin", "USDC", 6); err != nil {
		return fmt.Errorf("bridge setup: register USDC metadata: %w", err)
	}
	if err := registerTokenMetadata(sdkCtx, k, USDTContractAddress, "Tether USD", "USDT", 6); err != nil {
		return fmt.Errorf("bridge setup: register USDT metadata: %w", err)
	}

	if err := approveTokenForTrading(sdkCtx, k, USDCContractAddress); err != nil {
		return fmt.Errorf("bridge setup: approve USDC for trading: %w", err)
	}
	if err := approveTokenForTrading(sdkCtx, k, USDTContractAddress); err != nil {
		return fmt.Errorf("bridge setup: approve USDT for trading: %w", err)
	}

	if err := registerWrappedTokenCodeID(sdkCtx, k, data.WrappedTokenCodeID); err != nil {
		return fmt.Errorf("bridge setup: register wrapped token code ID: %w", err)
	}

	k.LogInfo("bridge setup completed successfully", types.Upgrades)
	return nil
}

// registerEthereumBridge registers the Ethereum bridge contract address.
func registerEthereumBridge(ctx sdk.Context, k keeper.Keeper, bridgeAddress string) error {
	address := strings.ToLower(bridgeAddress)

	if k.HasBridgeContractAddress(ctx, EthereumChainName, address) {
		k.LogInfo("bridge address already registered, skipping", types.Upgrades,
			"chainId", EthereumChainName, "address", address)
		return nil
	}

	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{
		ChainId: EthereumChainName,
		Address: address,
	})

	k.LogInfo("registered ethereum bridge address", types.Upgrades,
		"chainId", EthereumChainName, "address", address)
	return nil
}

// registerTokenMetadata registers token metadata for a known Ethereum token.
// Uses the same keeper method as MsgRegisterTokenMetadata.
func registerTokenMetadata(ctx sdk.Context, k keeper.Keeper, contractAddress, name, symbol string, decimals uint8) error {
	_, found := k.GetTokenMetadata(ctx, EthereumChainName, contractAddress)
	if found {
		k.LogInfo("token metadata already registered, skipping", types.Upgrades,
			"chainId", EthereumChainName, "address", contractAddress, "symbol", symbol)
		return nil
	}

	return k.SetTokenMetadata(ctx, EthereumChainName, contractAddress, keeper.TokenMetadata{
		Name:     name,
		Symbol:   symbol,
		Decimals: decimals,
	})
}

// approveTokenForTrading approves a token for bridge trading.
// Uses the same keeper method as MsgApproveBridgeTokenForTrading.
func approveTokenForTrading(ctx sdk.Context, k keeper.Keeper, contractAddress string) error {
	return k.SetBridgeTradeApprovedToken(ctx, types.BridgeTokenReference{
		ChainId:         EthereumChainName,
		ContractAddress: contractAddress,
	})
}

// registerWrappedTokenCodeID sets the CW20 code ID used for wrapped token instantiation.
func registerWrappedTokenCodeID(ctx sdk.Context, k keeper.Keeper, codeID uint64) error {
	if existingID, found := k.GetWrappedTokenCodeID(ctx); found && existingID > 0 {
		k.LogInfo("wrapped token code ID already registered, skipping", types.Upgrades,
			"existing_code_id", existingID)
		return nil
	}

	if err := k.SetWrappedTokenCodeID(ctx, codeID); err != nil {
		return err
	}

	k.LogInfo("registered wrapped token code ID", types.Upgrades,
		"code_id", codeID)
	return nil
}

func distributeBountyRewards(ctx context.Context, k keeper.Keeper) error {
	if len(bountyRewards) == 0 {
		k.Logger().Info("No bounty rewards to distribute")
		return nil
	}

	communitySaleAddr, err := sdk.AccAddressFromBech32(BountyCommunitySaleContractAddress)
	if err != nil {
		k.Logger().Error("invalid hardcoded community sale contract address", "address", BountyCommunitySaleContractAddress, "error", err)
		return nil
	}
	authorityAddr, err := sdk.AccAddressFromBech32(k.GetAuthority())
	if err != nil {
		k.Logger().Error("invalid authority address", "authority", k.GetAuthority(), "error", err)
		return nil
	}

	var totalRequired int64
	for _, bounty := range bountyRewards {
		totalRequired += bounty.Amount
	}

	available := k.BankView.SpendableCoin(ctx, communitySaleAddr, BountyIbcUsdtDenom).Amount.Int64()
	if available < totalRequired {
		k.Logger().Warn("insufficient community sale balance, skipping bounty distribution",
			"required", totalRequired, "available", available, "denom", BountyIbcUsdtDenom)
		return nil
	}

	k.Logger().Info("community sale balance sufficient for bounty distribution",
		"required", totalRequired, "available", available, "denom", BountyIbcUsdtDenom)

	permissionedKeeper := wasmkeeper.NewGovPermissionKeeper(k.GetWasmKeeper())
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	for _, bounty := range bountyRewards {
		recipient, err := sdk.AccAddressFromBech32(bounty.Address)
		if err != nil {
			k.Logger().Error("invalid bounty address", "address", bounty.Address, "error", err)
			continue
		}

		msgBz, err := json.Marshal(map[string]any{
			"withdraw_ibc": map[string]string{
				"denom":     BountyIbcUsdtDenom,
				"amount":    strconv.FormatInt(bounty.Amount, 10),
				"recipient": recipient.String(),
			},
		})
		if err != nil {
			k.Logger().Error("failed to marshal community sale withdraw message", "address", bounty.Address, "error", err)
			continue
		}

		if _, err := permissionedKeeper.Execute(sdkCtx, communitySaleAddr, authorityAddr, msgBz, sdk.NewCoins()); err != nil {
			k.Logger().Error("failed to distribute bounty from community sale contract",
				"address", bounty.Address, "amount", bounty.Amount, "denom", BountyIbcUsdtDenom, "error", err)
			continue
		}

		k.Logger().Info("bounty distributed from community sale contract",
			"address", bounty.Address, "amount", bounty.Amount, "denom", BountyIbcUsdtDenom)
	}

	return nil
}
