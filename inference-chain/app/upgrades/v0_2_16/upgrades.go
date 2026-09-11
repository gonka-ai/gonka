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
	"time"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	authz "github.com/cosmos/cosmos-sdk/x/authz"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

type AuthzMigrationKeeper interface {
	IterateGrants(ctx context.Context, handler func(granterAddr, granteeAddr sdk.AccAddress, grant authz.Grant) bool)
	GetAuthorization(ctx context.Context, grantee, granter sdk.AccAddress, msgType string) (authz.Authorization, *time.Time)
	SaveGrant(ctx context.Context, grantee, granter sdk.AccAddress, authorization authz.Authorization, expiration *time.Time) error
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	authzKeeper AuthzMigrationKeeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Capability state can already exist even when the version map entry is
		// missing. Set it explicitly so RunMigrations does not re-run InitGenesis.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		if err := grantDeclarePoCIntentAuthz(ctx, authzKeeper, k); err != nil {
			return fromVM, err
		}

		toVM, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return toVM, err
		}

		k.LogInfo("successfully upgraded", types.Upgrades, "version", UpgradeName)
		return toVM, nil
	}
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
