package v0_2_13

import (
	"cmp"
	"context"
	"slices"
	"time"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/authz"
	authzkeeper "github.com/cosmos/cosmos-sdk/x/authz/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

const (
	MaxEscrowsPerEpoch uint32 = 500_000
	MaxNonce           uint32 = 1_000_000
	// Block window after the upgrade in which confirmation PoC is skipped.
	// Same value as v0.2.10; covers the rest of the upgrade epoch on mainnet.
	GraceUpgradeProtectionWindow int64 = 3000
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	authzKeeper authzkeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		if err := setDevshardEscrowParams(ctx, k); err != nil {
			return nil, err
		}
		if err := backfillConfirmationWeightScales(ctx, k); err != nil {
			return nil, err
		}
		if err := grantRespondDealerComplaintsAuthz(ctx, authzKeeper, k); err != nil {
			return nil, err
		}
		if err := disableConfirmationPocForUpgradeEpoch(ctx, k); err != nil {
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
	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("set devshard escrow params", types.Upgrades,
		"max_escrows_per_epoch", MaxEscrowsPerEpoch,
		"max_nonce", MaxNonce)
	return nil
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
	slices.SortFunc(modelIDs, cmp.Compare)

	scales := make([]*types.ConfirmationWeightScale, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		scales = append(scales, &types.ConfirmationWeightScale{
			ModelId:           modelID,
			WeightScaleFactor: coefficients[modelID].CloneOrOne(),
		})
	}
	return scales
}
