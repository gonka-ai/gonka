// Package v0_2_16 holds the upgrade handler scaffold for the v0.2.16 release.
//
// At bootstrap time this stays intentionally small: capability-version fix
// plus RunMigrations. As upgrade work lands, add migration steps below the
// capability fix and above RunMigrations.
//
// If later work bumps a module ConsensusVersion, it must also register the
// corresponding migration in app/upgrades.go's registerMigrations().
package v0_2_16

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
	"time"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authz "github.com/cosmos/cosmos-sdk/x/authz"

	coefficient "github.com/productscience/inference/x/inference/coefficients"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

type AuthzMigrationKeeper interface {
	IterateGrants(ctx context.Context, handler func(granterAddr, granteeAddr sdk.AccAddress, grant authz.Grant) bool)
	GetAuthorization(ctx context.Context, grantee, granter sdk.AccAddress, msgType string) (authz.Authorization, *time.Time)
	SaveGrant(ctx context.Context, grantee, granter sdk.AccAddress, authorization authz.Authorization, expiration *time.Time) error
}

// UpgradeInfo is extra JSON in the software-upgrade proposal's `info` /
// --upgrade-info field. Cosmovisor already stores binaries/api_binaries in the
// same object; unknown keys are ignored.
//
// Omitted enabled_fee_groups preserves migration defaults; an empty list disables fees.
// Example override:
//
//	"enabled_fee_groups": ["epoch"],
//	"min_gas_prices": {"epoch": 10}
type UpgradeInfo struct {
	EnabledFeeGroups []string          `json:"enabled_fee_groups"`
	MinGasPrices     map[string]uint64 `json:"min_gas_prices"`
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	authzKeeper AuthzMigrationKeeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		// Future v0.2.16 migration steps land below this line.
		if err := grantPoCChallengeAuthz(ctx, authzKeeper, k); err != nil {
			return fromVM, err
		}
		if err := migrateDevshardApprovedVersions(ctx, k); err != nil {
			return fromVM, err
		}
		if err := migrateDynamicCoefficientParams(ctx, k); err != nil {
			return fromVM, err
		}
		if err := freezeUpcomingCoefficientConfig(ctx, k); err != nil {
			return fromVM, err
		}
		if err := migrateCurrentEffectiveCoefficients(ctx, k); err != nil {
			return fromVM, err
		}
		if err := migratePoCChallengeParams(ctx, k); err != nil {
			return fromVM, err
		}
		if err := grantDeclarePoCIntentAuthz(ctx, authzKeeper, k); err != nil {
			return fromVM, err
		}
		if err := distributeBountyRewards(ctx, k); err != nil {
			return fromVM, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		// Apply overrides after RunMigrations installs the default fee groups.
		if err := applyFeeGroupUpgradeInfo(ctx, k, plan.Info); err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
}

func migratePoCChallengeParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.PocChallengeParams != nil {
		return nil
	}
	params.PocChallengeParams = types.DefaultPoCChallengeParams()
	return k.SetParams(ctx, params)
}

func migrateDynamicCoefficientParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.PocParams == nil {
		return nil
	}
	if params.PocParams.DynamicCoefficientParams != nil {
		changed := params.PocParams.WeightScaleFactor != nil
		params.PocParams.WeightScaleFactor = nil
		for _, model := range params.PocParams.Models {
			if model != nil && model.WeightScaleFactor != nil {
				model.WeightScaleFactor = nil
				changed = true
			}
		}
		if changed {
			return k.SetParams(ctx, params)
		}
		return nil
	}

	enabled := make([]string, 0, len(params.PocParams.Models))
	modelByID := make(map[string]*types.PoCModelConfig)
	for _, model := range params.PocParams.Models {
		if model == nil || model.ModelId == "" {
			continue
		}
		canonical, err := canonicalMigrationDecimal(model.WeightScaleFactor)
		if err != nil {
			return fmt.Errorf("dynamic coefficient migration model %q: %w", model.ModelId, err)
		}
		legacy, err := canonical.ToLegacyDec()
		if err != nil {
			return fmt.Errorf("dynamic coefficient migration model %q: %w", model.ModelId, err)
		}
		if !legacy.IsPositive() {
			model.DynamicCoefficient = nil
			model.WeightScaleFactor = nil
			continue
		}
		model.WeightScaleFactor = canonical
		enabled = append(enabled, model.ModelId)
		modelByID[model.ModelId] = model
	}
	if len(enabled) == 0 || len(enabled) > 5000 {
		return nil
	}
	slices.Sort(enabled)

	baseTarget := uint32(10000 / len(enabled))
	if baseTarget < 2 {
		return nil
	}
	remainder := uint32(10000 % len(enabled))
	remainderModel := enabled[0]
	if params.DelegationParams != nil {
		initial := params.DelegationParams.InitialModelId
		if _, ok := modelByID[initial]; ok {
			remainderModel = initial
		}
	}

	initialState := make([]*types.ConfirmationWeightScale, 0, len(enabled))
	smallestTarget := baseTarget
	for _, modelID := range enabled {
		target := baseTarget
		if modelID == remainderModel {
			target += remainder
		}
		model := modelByID[modelID]
		config, err := initialDynamicCoefficientConfig(model)
		if err != nil {
			return fmt.Errorf("dynamic coefficient migration model %q: %w", modelID, err)
		}
		config.TargetShareBps = target
		model.DynamicCoefficient = config
		initialState = append(initialState, &types.ConfirmationWeightScale{
			ModelId:         modelID,
			BaseCoefficient: cloneMigrationDecimal(model.WeightScaleFactor),
			AdaptiveStep:    &types.Decimal{Value: 25, Exponent: -3}, // step_max / 2
		})
		model.WeightScaleFactor = nil
	}
	targetZone := uint32(500)
	if smallestTarget <= targetZone {
		targetZone = smallestTarget - 1
	}
	params.PocParams.DynamicCoefficientParams = &types.DynamicCoefficientParams{
		TargetZoneBps:     targetZone,
		StepMin:           &types.Decimal{Value: 5, Exponent: -3},
		StepMax:           &types.Decimal{Value: 5, Exponent: -2},
		BootstrapStepMax:  &types.Decimal{Value: 25, Exponent: -2},
		BootstrapShareBps: 100,
	}
	params.PocParams.WeightScaleFactor = nil
	if err := params.Validate(); err != nil {
		return fmt.Errorf("dynamic coefficient migration produced invalid params: %w", err)
	}
	if err := seedCurrentCoefficientState(ctx, k, initialState); err != nil {
		return err
	}
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("migrated dynamic coefficient params", types.Upgrades,
		"enabled_models", len(enabled),
		"target_zone_bps", targetZone)
	return nil
}

// seedCurrentCoefficientState lets the first dynamic epoch carry the current scales
// through the normal controller path without changing current-epoch reward weights.
func seedCurrentCoefficientState(ctx context.Context, k keeper.Keeper, initialState []*types.ConfirmationWeightScale) error {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return fmt.Errorf("cannot seed coefficient state: effective epoch index not found")
	}
	data, found, err := k.GetEpochGroupDataWithError(ctx, epochIndex, "")
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("cannot seed coefficient state: current epoch %d has no root epoch group data", epochIndex)
	}
	scales := make(map[string]*types.ConfirmationWeightScale, len(data.ConfirmationWeightScales))
	for _, scale := range data.ConfirmationWeightScales {
		if scale != nil {
			scales[scale.ModelId] = scale
		}
	}
	for _, seed := range initialState {
		scale := scales[seed.ModelId]
		if scale == nil {
			// A configured model absent from this epoch has no current reward weight.
			scale = &types.ConfirmationWeightScale{
				ModelId:                 seed.ModelId,
				EffectiveCoefficient:    &types.Decimal{},
				ExcludeFromConfirmation: true,
			}
			data.ConfirmationWeightScales = append(data.ConfirmationWeightScales, scale)
		}
		scale.BaseCoefficient = seed.BaseCoefficient
		scale.AdaptiveStep = seed.AdaptiveStep
		scale.PrevSign = 0
	}
	k.SetEpochGroupData(ctx, data)
	return nil
}

func initialDynamicCoefficientConfig(model *types.PoCModelConfig) (*types.DynamicCoefficientModelConfig, error) {
	lower, err := scaleMigrationDecimal(model.WeightScaleFactor, 9)
	if err != nil {
		return nil, err
	}
	upper, err := scaleMigrationDecimal(model.WeightScaleFactor, 11)
	if err != nil {
		return nil, err
	}
	config := &types.DynamicCoefficientModelConfig{
		RelativeDifficulty: &types.Decimal{Value: 1, Exponent: 0},
	}
	// Target values from proposals/multi-model-poc/dynamic-coeff-init.md.
	//
	// Model                   CoeffMin        CoeffMax        RelativeDifficulty
	// MiniMax M2.7            0.3024          0.3024          1
	// GLM 5.3 Flash           0.508468965517  0.847197025352  2.668169014084
	// DeepSeek V4 Flash 0731  0.183272727272  0.48951         1.541666666666
	//
	// Initial values with +/-10% limits, within the target bounds:
	//
	// Model                   Current scale  Initial min  Initial max
	// MiniMax M2.7            0.3024         0.3024       0.3024
	// GLM 5.3 Flash           0.62           0.558        0.682
	// DeepSeek V4 Flash 0731  0.246          0.2214       0.2706
	//
	// These initial values use the scales shown. Actual scales are read at upgrade height.
	switch model.ModelId {
	case "MiniMaxAI/MiniMax-M2.7":
		config.CoeffMin = &types.Decimal{Value: 3024, Exponent: -4}
		config.CoeffMax = &types.Decimal{Value: 3024, Exponent: -4}
	case "zai-org/GLM-5.3-Flash":
		config.CoeffMin = &types.Decimal{Value: 508468965517, Exponent: -12}
		config.CoeffMax = &types.Decimal{Value: 847197025352, Exponent: -12}
		config.RelativeDifficulty = &types.Decimal{Value: 2668169014084, Exponent: -12}
	case "deepseek-ai/DeepSeek-V4-Flash-0731":
		config.CoeffMin = &types.Decimal{Value: 183272727272, Exponent: -12}
		config.CoeffMax = &types.Decimal{Value: 48951, Exponent: -5}
		config.RelativeDifficulty = &types.Decimal{Value: 1541666666666, Exponent: -12}
	}
	// Limit initial movement to +/-10% of the current scale, within benchmark bounds.
	if config.CoeffMin == nil || lower.ToDecimal().GreaterThan(config.CoeffMin.ToDecimal()) {
		config.CoeffMin = lower
	}
	if config.CoeffMax == nil || upper.ToDecimal().LessThan(config.CoeffMax.ToDecimal()) {
		config.CoeffMax = upper
	}
	if config.CoeffMin.ToDecimal().GreaterThan(config.CoeffMax.ToDecimal()) {
		return nil, fmt.Errorf("current coefficient +/-10%% does not overlap benchmark bounds")
	}
	return config, nil
}

// scaleMigrationDecimal multiplies by tenths without floating point or int64 overflow.
func scaleMigrationDecimal(value *types.Decimal, tenths int64) (*types.Decimal, error) {
	coefficient := new(big.Int).Mul(big.NewInt(value.Value), big.NewInt(tenths))
	exponent := value.Exponent - 1
	ten := big.NewInt(10)
	for coefficient.Sign() != 0 && new(big.Int).Mod(coefficient, ten).Sign() == 0 {
		coefficient.Quo(coefficient, ten)
		exponent++
	}
	if !coefficient.IsInt64() {
		return nil, fmt.Errorf("scaled coefficient does not fit int64")
	}
	return &types.Decimal{Value: coefficient.Int64(), Exponent: exponent}, nil
}

func canonicalMigrationDecimal(value *types.Decimal) (*types.Decimal, error) {
	if value == nil {
		return &types.Decimal{Value: 1, Exponent: 0}, nil
	}
	coefficient := value.Value
	exponent := value.Exponent
	for coefficient != 0 && coefficient%10 == 0 {
		coefficient /= 10
		exponent++
	}
	return &types.Decimal{Value: coefficient, Exponent: exponent}, nil
}

func cloneMigrationDecimal(value *types.Decimal) *types.Decimal {
	if value == nil {
		return nil
	}
	return &types.Decimal{Value: value.Value, Exponent: value.Exponent}
}

func migrateCurrentEffectiveCoefficients(ctx context.Context, k keeper.Keeper) error {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil
	}
	data, found, err := k.GetEpochGroupDataWithError(ctx, epochIndex, "")
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	changed := false
	for _, scale := range data.ConfirmationWeightScales {
		if scale == nil || scale.EffectiveCoefficient != nil || scale.WeightScaleFactor == nil {
			continue
		}
		scale.EffectiveCoefficient = cloneMigrationDecimal(scale.WeightScaleFactor)
		scale.WeightScaleFactor = nil
		changed = true
	}
	if changed {
		k.SetEpochGroupData(ctx, data)
	}
	return nil
}

func freezeUpcomingCoefficientConfig(ctx context.Context, k keeper.Keeper) error {
	upcoming, found := k.GetUpcomingEpoch(ctx)
	if !found || upcoming == nil {
		return nil
	}
	data, found, err := k.GetEpochGroupDataWithError(ctx, upcoming.Index, "")
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("upcoming epoch %d has no root epoch group data", upcoming.Index)
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	frozen, err := coefficient.Freeze(params.PocParams, nil)
	if err != nil {
		return err
	}
	data.DynamicCoefficientParams = frozen.Params
	data.ConfirmationWeightScales = frozen.Scales
	k.SetEpochGroupData(ctx, data)
	return nil
}

func applyFeeGroupUpgradeInfo(ctx context.Context, k keeper.Keeper, infoJSON string) error {
	if infoJSON == "" {
		k.LogInfo("no upgrade info, keeping default fee groups", types.Upgrades)
		return nil
	}

	var info UpgradeInfo
	if err := json.Unmarshal([]byte(infoJSON), &info); err != nil {
		return fmt.Errorf("unmarshal v0.2.16 upgrade info: %w", err)
	}
	if info.EnabledFeeGroups == nil {
		if len(info.MinGasPrices) != 0 {
			return fmt.Errorf("min_gas_prices requires enabled_fee_groups")
		}
		k.LogInfo("enabled_fee_groups omitted, keeping default fee groups", types.Upgrades)
		return nil
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("get inference params: %w", err)
	}
	if params.FeeParams == nil {
		params.FeeParams = types.DefaultFeeParams()
	}

	for _, name := range info.EnabledFeeGroups {
		if !types.IsKnownFeeGroup(name) {
			return fmt.Errorf("unknown fee group %q", name)
		}
		price, ok := info.MinGasPrices[name]
		if !ok || price == 0 {
			return fmt.Errorf("enabled fee group %q requires min_gas_prices[%q] > 0", name, name)
		}
		group := params.FeeParams.GroupByName(name)
		if group == nil {
			group = &types.FeeGroup{Name: name}
			params.FeeParams.Groups = append(params.FeeParams.Groups, group)
		}
		group.MinGasPrice = price
	}
	for name := range info.MinGasPrices {
		found := false
		for _, enabled := range info.EnabledFeeGroups {
			if enabled == name {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("min_gas_prices[%q] is not in enabled_fee_groups", name)
		}
	}

	params.FeeParams.EnabledFeeGroups = info.EnabledFeeGroups
	params.FeeParams.MinGasPriceNgonka = 0
	if err := params.FeeParams.Validate(); err != nil {
		return fmt.Errorf("fee params after applying upgrade info: %w", err)
	}
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("enabled fee groups from upgrade info", types.Upgrades,
		"enabled_fee_groups", info.EnabledFeeGroups,
		"min_gas_prices", info.MinGasPrices)
	return nil
}

func migrateDevshardApprovedVersions(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.DevshardEscrowParams == nil {
		return nil
	}
	for i, v := range params.DevshardEscrowParams.ApprovedVersions {
		if v == nil {
			return fmt.Errorf("approved_versions[%d] cannot be null", i)
		}
		if err := v.Validate(); err != nil {
			return fmt.Errorf("approved_versions[%d]: %w", i, err)
		}
		if err := k.SetApprovedVersion(ctx, *v); err != nil {
			return err
		}
	}
	n := len(params.DevshardEscrowParams.ApprovedVersions)
	params.DevshardEscrowParams.ApprovedVersions = nil
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("migrated approved devshard versions out of params", types.Upgrades, "count", n)
	return nil
}

// grantPoCChallengeAuthz backfills challenge commit/vote msgs on existing
// cold->warm pairs. Identify pairs by WarmKeyGrantMarkerTypeURL (live after
// v0.2.15), not leftover MsgStartInference.
func grantPoCChallengeAuthz(ctx context.Context, authzKeeper AuthzMigrationKeeper, k keeper.Keeper) error {
	type grantPair struct {
		granter    sdk.AccAddress
		grantee    sdk.AccAddress
		expiration *time.Time
	}
	seen := make(map[string]bool)
	var pairs []grantPair
	authzKeeper.IterateGrants(ctx, func(granterAddr, granteeAddr sdk.AccAddress, grant authz.Grant) bool {
		if grant.Authorization.GetTypeUrl() != "/cosmos.authz.v1beta1.GenericAuthorization" {
			return false
		}
		var genAuth authz.GenericAuthorization
		if err := k.Codec().Unmarshal(grant.Authorization.Value, &genAuth); err != nil {
			return false
		}
		if genAuth.Msg != types.WarmKeyGrantMarkerTypeURL {
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

	msgTypes := []string{
		sdk.MsgTypeURL(&types.MsgPoCChallengeStoreCommit{}),
		sdk.MsgTypeURL(&types.MsgSubmitPoCChallengeValidations{}),
	}
	blockTime := sdk.UnwrapSDKContext(ctx).BlockTime()
	for _, pair := range pairs {
		if pair.expiration != nil && !pair.expiration.After(blockTime) {
			continue
		}
		for _, msgType := range msgTypes {
			existing, _ := authzKeeper.GetAuthorization(ctx, pair.grantee, pair.granter, msgType)
			if existing != nil {
				continue
			}
			auth := authz.NewGenericAuthorization(msgType)
			if err := authzKeeper.SaveGrant(ctx, pair.grantee, pair.granter, auth, pair.expiration); err != nil {
				return err
			}
		}
	}
	return nil
}

// grantDeclarePoCIntentAuthz backfills MsgDeclarePoCIntent authz grants on
// every existing cold->warm ML ops pair. Identify pairs by the live warm-key
// marker (MsgClaimRewards) and reuse its expiration so hosts that already
// ran grant-ml-ops-permissions can submit bootstrap-model intents without
// re-granting.
func grantDeclarePoCIntentAuthz(ctx context.Context, authzKeeper AuthzMigrationKeeper, k keeper.Keeper) error {
	type grantPair struct {
		granter    sdk.AccAddress
		grantee    sdk.AccAddress
		expiration *time.Time
	}

	intentMsgType := sdk.MsgTypeURL(&types.MsgDeclarePoCIntent{})
	seen := make(map[string]bool)
	var pairs []grantPair
	authzKeeper.IterateGrants(ctx, func(granter, grantee sdk.AccAddress, grant authz.Grant) bool {
		if grant.Authorization.GetTypeUrl() != "/cosmos.authz.v1beta1.GenericAuthorization" {
			return false
		}
		var authorization authz.GenericAuthorization
		if err := k.Codec().Unmarshal(grant.Authorization.Value, &authorization); err != nil {
			return false
		}
		if authorization.Msg != types.WarmKeyGrantMarkerTypeURL {
			return false
		}
		key := granter.String() + "->" + grantee.String()
		if !seen[key] {
			seen[key] = true
			pairs = append(pairs, grantPair{granter: granter, grantee: grantee, expiration: grant.Expiration})
		}
		return false
	})

	k.LogInfo("found cold->warm pairs needing MsgDeclarePoCIntent grant", types.Upgrades, "count", len(pairs))

	created := 0
	skipped := 0
	for _, pair := range pairs {
		existing, _ := authzKeeper.GetAuthorization(ctx, pair.grantee, pair.granter, intentMsgType)
		if existing != nil {
			skipped++
			continue
		}
		authorization := authz.NewGenericAuthorization(intentMsgType)
		if err := authzKeeper.SaveGrant(ctx, pair.grantee, pair.granter, authorization, pair.expiration); err != nil {
			k.LogError("failed to save MsgDeclarePoCIntent grant", types.Upgrades,
				"granter", pair.granter.String(),
				"grantee", pair.grantee.String(),
				"error", err)
			continue
		}
		created++
	}

	k.LogInfo("MsgDeclarePoCIntent grant migration complete", types.Upgrades,
		"created", created, "skipped", skipped)
	return nil
}
