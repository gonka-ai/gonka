// Package v0_2_15 holds the upgrade handler scaffold for the v0.2.15 release.
//
// At bootstrap time this stays intentionally small: capability-version fix
// plus RunMigrations. As upgrade work lands, add migration steps below the
// capability fix and above RunMigrations.
//
// If later work bumps a module ConsensusVersion, it must also register the
// corresponding migration in app/upgrades.go's registerMigrations().
package v0_2_15

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
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

const BountyCommunitySaleContractAddress = "gonka18pkq9mwxxlmyq7kr5txhm060wemg2s4u94wvsfd9w2kdc0u99d6spk8pz2"
const BountyIbcUsdtDenom = "ibc/115F68FBA220A028C6F6ED08EA0C1A9C8C52798B14FB66E6C89D5D8C06A524D4"

func USDT(amount int64) int64 {
	return amount * 1_000_000
}

type BountyReward struct {
	Address string
	Amount  int64
}

type UpgradeInfo struct {
	ApprovedVersions []*types.DevshardApprovedVersion `json:"approved_versions"`
}

var bountyRewards = []BountyReward{
	// RM: devshard v4 release management.
	// Public name: @akup
	{Address: "gonka1ejkupq3cy6p8xd64ew2wlzveml86ckpzn9dl56", Amount: USDT(22000)},

	// RM: devshard v4 upgrade review.
	// Public name: @Ryanchen911
	{Address: "gonka1zqss46r6jf6dhhyaa777kc2ppvjhn0ufkx4y57", Amount: USDT(1000)},

	// PR #1308: devshard contribution.
	// Public name: @redstartechno
	{Address: "gonka105ce4495mj0mwkxqeasgdzqfq5jjrfq32eza5l", Amount: USDT(200)},

	// PR #1283: report and fix of medium severity vulnerability.
	// Public name: @0xMayoor
	{Address: "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8", Amount: USDT(3000)},

	// PR #1311: report and fix of low severity vulnerability.
	// Public name: @0xMayoor
	{Address: "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8", Amount: USDT(500)},

	// PR #1144, #1152, #1126, #1154: devshard contribution.
	// Public name: @pixelplex
	{Address: "gonka1vu28c7w5zxqe28lakrrfdrkvscft326rxur3dv", Amount: USDT(7000)},

	// PR #1332, #1295: testing proposal and implementation.
	// Public name: @aikuznetsov
	{Address: "gonka1frlfyz2wtltdy47dq3w9pwc8ruvjvlthp2lh53", Amount: USDT(3125)},

	// PR #1437, #1490: devshard contribution.
	// Public name: @snevolin
	{Address: "gonka1vnupswg7qz2w5k5ax6zrp02mxmln6arnvjc87h", Amount: USDT(3000)},
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

		if err := migrateWarmKeyGrantMarker(ctx, authzKeeper, k); err != nil {
			return fromVM, err
		}

		if err := applyDevshardApprovedVersions(ctx, k, plan.Info); err != nil {
			return nil, err
		}

		if err := distributeBountyRewards(ctx, k); err != nil {
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

func migrateWarmKeyGrantMarker(ctx context.Context, authzKeeper AuthzMigrationKeeper, k keeper.Keeper) error {
	type grantPair struct {
		granter    sdk.AccAddress
		grantee    sdk.AccAddress
		expiration *time.Time
	}

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
		if authorization.Msg != types.LegacyMsgStartInferenceTypeURL {
			return false
		}
		key := granter.String() + "->" + grantee.String()
		if !seen[key] {
			seen[key] = true
			pairs = append(pairs, grantPair{granter: granter, grantee: grantee, expiration: grant.Expiration})
		}
		return false
	})

	for _, pair := range pairs {
		existing, _ := authzKeeper.GetAuthorization(ctx, pair.grantee, pair.granter, types.WarmKeyGrantMarkerTypeURL)
		if existing != nil {
			continue
		}
		authorization := authz.NewGenericAuthorization(types.WarmKeyGrantMarkerTypeURL)
		if err := authzKeeper.SaveGrant(ctx, pair.grantee, pair.granter, authorization, pair.expiration); err != nil {
			return err
		}
	}
	return nil
}

func applyDevshardApprovedVersions(ctx context.Context, k keeper.Keeper, infoJSON string) error {
	if infoJSON == "" {
		k.LogInfo("no upgrade info, skipping devshard approved versions", types.Upgrades)
		return nil
	}

	var info UpgradeInfo
	if err := json.Unmarshal([]byte(infoJSON), &info); err != nil {
		return fmt.Errorf("unmarshal upgrade info: %w", err)
	}
	if len(info.ApprovedVersions) == 0 {
		k.LogInfo("no devshard approved versions in upgrade info, skipping", types.Upgrades)
		return nil
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("get inference params: %w", err)
	}
	if params.DevshardEscrowParams == nil {
		params.DevshardEscrowParams = types.DefaultDevshardEscrowParams()
	}

	replacedCount := 0
	for i, version := range info.ApprovedVersions {
		if version == nil {
			return fmt.Errorf("approved_versions[%d] cannot be null", i)
		}

		replaced := false
		for j, existing := range params.DevshardEscrowParams.ApprovedVersions {
			if existing != nil && existing.Name == version.Name {
				params.DevshardEscrowParams.ApprovedVersions[j] = version
				replaced = true
				break
			}
		}
		if !replaced {
			params.DevshardEscrowParams.ApprovedVersions = append(
				params.DevshardEscrowParams.ApprovedVersions,
				version,
			)
		} else {
			replacedCount++
		}
	}

	if err := k.SetParams(ctx, params); err != nil {
		return fmt.Errorf("set inference params: %w", err)
	}
	k.LogInfo("set devshard approved versions from upgrade info", types.Upgrades,
		"provided", len(info.ApprovedVersions),
		"appended", len(info.ApprovedVersions)-replacedCount,
		"replaced", replacedCount)
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
