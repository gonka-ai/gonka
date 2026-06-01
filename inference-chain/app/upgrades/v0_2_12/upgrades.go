package v0_2_12

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	sdkmath "cosmossdk.io/math"
	"cosmossdk.io/x/feegrant"
	feegrantkeeper "cosmossdk.io/x/feegrant/keeper"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/authz"
	authzkeeper "github.com/cosmos/cosmos-sdk/x/authz/keeper"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

// MigratedFeeAllowance is the BasicAllowance limit auto-granted during the
// v0.2.12 upgrade for every existing cold→warm authz pair. Sized to comfortably
// cover many months of routine DAPI operation; hosts can refresh by re-running
// `inferenced tx inference grant-ml-ops-permissions` when depleted.
var MigratedFeeAllowance = sdk.NewCoins(sdk.NewCoin("ngonka", sdkmath.NewInt(100_000_000_000))) // 100 GNK

// MigratedFeeAllowanceExpiration is how long the auto-granted allowance lasts.
const MigratedFeeAllowanceExpiration = 365 * 24 * time.Hour

const kimiModelID = "moonshotai/Kimi-K2.6"

// Initial approved devshard binary registered by the v0.2.12 upgrade so that
// `versiond` has at least one approved version to download and run after the
// upgrade goes live. Governance can append new versions later via
// MsgUpdateParams.
const (
	DevshardV1Name   = "v1"
	DevshardV1Binary = "https://github.com/gonka-ai/gonka/releases/download/release%2Fv0.2.12/devshardd.zip"
	DevshardV1Sha256 = "15f722444e6545bc787f1ef6d1011557d25a8b05cb9f6aaf1a514349d36d4715"
)

const BountyCommunitySaleContractAddress = "gonka18pkq9mwxxlmyq7kr5txhm060wemg2s4u94wvsfd9w2kdc0u99d6spk8pz2"
const DefaultBountyIbcUsdtDenom = "ibc/115F68FBA220A028C6F6ED08EA0C1A9C8C52798B14FB66E6C89D5D8C06A524D4"

func USDT(amount int64) int64 {
	return amount * 1_000_000
}

type BountyReward struct {
	Address string
	Amount  int64
}

var bountyRewards = []BountyReward{
	// CertiK audit fixes (GEB-29, GEB-35, GEB-44, GEB-45, GEB-51).
	// PR: #988, #1020, #1021
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: USDT(6000)},
	// DKG dealer consensus.
	// PR: #825
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: USDT(3000)},
	// Developer inference access / account API changes.
	// PR: #750
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: USDT(1000)},
	// OpenAI compatibility and API error handling.
	// PR: #614
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: USDT(500)},
	// v0.2.12 release management.
	{Address: "gonka18enyz7h6hh5zjveee5wnhkhrcexamfz0zdxxqe", Amount: USDT(2500)},
	// v0.2.12 release management.
	{Address: "gonka1ejkupq3cy6p8xd64ew2wlzveml86ckpzn9dl56", Amount: USDT(5000)},
	// Inference validation optimization.
	// Issue: #929
	{Address: "gonka1yhdhp4vwsvdsplv4acksntx0zxh8saueq6lj9m", Amount: USDT(9000)},
	// Acquire node gRPC.
	// PR: #945
	{Address: "gonka1vu28c7w5zxqe28lakrrfdrkvscft326rxur3dv", Amount: USDT(3000)},
	// Fund atomicity error safety.
	// PR: #789
	{Address: "gonka1s8szs7n43jxgz4a4xaxmzm5emh7fmjxhach7w8", Amount: USDT(2000)},
	// Align validator slashing with required collateral.
	// PR: #940
	{Address: "gonka1j3f2xkapx8cmczpjqcsrh7cc3peyj3ngkjv4p8", Amount: USDT(1500)},
	// Report of free inference vulnerability (fixed by devshards release).
	{Address: "gonka1c34w3r45f0uftjckt2yy4k22vnc3zqjnp0umyz", Amount: USDT(500)},
	// Chat completions fix (missed from v0.2.10).
	// Issue: #499
	{Address: "gonka139f7x4gur2yuyty64dkqxep8jk3d7ku8ayjaqg", Amount: USDT(200)},
	// Review of upgrade v0.2.11.
	{Address: "gonka12jaf7m4eysyqt32mrgarum6z96vt55tckvcleq", Amount: USDT(1000)},
}

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	_ distrkeeper.Keeper,
	blsKeeper blskeeper.Keeper,
	authzKeeper authzkeeper.Keeper,
	feegrantKeeper feegrantkeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, _ upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		k.LogInfo("starting upgrade", types.Upgrades, "version", UpgradeName)

		// Keep capability module version explicit to avoid re-running InitGenesis
		// on chains where capability state already exists but version map is missing.
		if _, ok := fromVM["capability"]; !ok {
			fromVM["capability"] = mm.Modules["capability"].(module.HasConsensusVersion).ConsensusVersion()
		}

		err := removeTopMiner(ctx, k)
		if err != nil {
			return nil, err
		}

		err = clearTrainingState(ctx, k)
		if err != nil {
			return nil, err
		}

		// Multi-model migration steps.
		err = clearLegacyPoCv2Data(ctx, k)
		if err != nil {
			return nil, err
		}

		err = migrateParams(ctx, k)
		if err != nil {
			return nil, err
		}

		err = updateGovernanceModels(ctx, k)
		if err != nil {
			return nil, err
		}

		err = backfillVotingPower(ctx, k)
		if err != nil {
			return nil, err
		}

		err = initNewPruningState(ctx, k)
		if err != nil {
			return nil, err
		}

		err = adjustParameters(ctx, k)
		if err != nil {
			return nil, err
		}

		err = adjustBLSParameters(ctx, blsKeeper)
		if err != nil {
			return nil, err
		}

		// Migrate any in-flight EpochBLSData entries from the pre-split
		// inline layout to per-entry sub-keys. This must run before the
		// new verifier/dealer/group-validation handlers can touch state,
		// since they now null out split fields before SetEpochBLSData and
		// would otherwise drop legacy inline data.
		if err := migrateEpochBLSDataToSubKeys(sdk.UnwrapSDKContext(ctx), blsKeeper); err != nil {
			k.LogError("Error migrating EpochBLSData to sub-keys for v0.2.12", types.Upgrades, "err", err)
			return nil, err
		}

		// Same migration for ThresholdSigningRequest.PartialSignatures.
		// Pre-split, partial sigs accumulated inline on the request;
		// post-split, AddPartialSignature writes sub-keys directly and
		// nulls out the slice before persisting the base. Legacy inline
		// entries must be moved to sub-keys here or they would be dropped.
		if err := migrateThresholdSigningRequestsToSubKeys(sdk.UnwrapSDKContext(ctx), blsKeeper); err != nil {
			k.LogError("Error migrating ThresholdSigningRequests to sub-keys for v0.2.12", types.Upgrades, "err", err)
			return nil, err
		}

		// Same split for BridgeTransaction.Validators. Pre-split, each
		// validator's confirmation appended to the inline slice; post-split,
		// the bridge-exchange handler writes per-validator sub-keys and
		// nulls out Validators before SetBridgeTransaction. Move any
		// legacy inline entries to the sub-key layout here so the hot
		// path doesn't drop them.
		if err := migrateBridgeTransactionValidatorsToSubKeys(ctx, k); err != nil {
			k.LogError("Error migrating BridgeTransaction validators to sub-keys for v0.2.12", types.Upgrades, "err", err)
			return nil, err
		}

		// Same split for GroupKeyValidationState.PartialSignatures.
		// Pre-split, partials accumulated inline on the validation state;
		// post-split, SubmitGroupKeyValidationSignature writes per-participant
		// sub-keys directly and SetGroupKeyValidationState zeroes the
		// inline slice. Move any legacy inline entries to sub-keys here so
		// the read path (GetGroupKeyValidationState) stays a pure read.
		if err := migrateGroupKeyValidationStatesToSubKeys(sdk.UnwrapSDKContext(ctx), blsKeeper); err != nil {
			k.LogError("Error migrating GroupKeyValidationStates to sub-keys for v0.2.12", types.Upgrades, "err", err)
			return nil, err
		}

		if err := setFeeParams(ctx, k); err != nil {
			return nil, err
		}

		if err := setDevshardApprovedVersions(ctx, k); err != nil {
			return nil, err
		}

		if err := distributeBountyRewards(ctx, k); err != nil {
			return nil, err
		}

		// Auto-create feegrant allowances for every cold→warm pair that has
		// existing ML ops authz grants. This is required because v0.2.12 turns
		// on consensus-level transaction fees: the DAPI signs every tx with
		// the warm key (which is unfunded), so the chain needs a feegrant
		// allowance from cold→warm to deduct fees from the funded cold account.
		// Without this migration, every existing host's DAPI would start
		// failing transactions immediately after the upgrade.
		if err := migrateFeegrantsForFees(ctx, authzKeeper, feegrantKeeper, k); err != nil {
			k.LogError("Error migrating feegrants for v0.2.12 fees", types.Upgrades, "err", err)
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

// migrateFeegrantsForFees iterates every existing authz grant. For each unique
// cold→warm pair that has an MsgStartInference grant (which uniquely identifies
// host ML ops grants), it creates a BasicAllowance from cold→warm so the warm
// key can pay tx fees from the cold account's balance via x/feegrant.
//
// Idempotent: if an allowance already exists for the pair, it is skipped.
//
// Scale and cost on mainnet:
//
//   - The upper bound on work is proportional to the number of authz grants
//     in state, not the number of participants. Each participant has on the
//     order of ~20 ML ops authz grants (one per msg type in
//     InferenceOperationKeyPerms), so at ~100 mainnet hosts we expect ~2,000
//     authz grant entries to iterate.
//   - We filter inside the callback to only the `MsgStartInference` grant
//     per pair, yielding ~100 feegrant allowances to create (one per host).
//   - Creating a BasicAllowance is a single KV store write plus an account
//     lookup — negligible compared to the rest of the upgrade handler.
//
// In practice this migration completes in well under a second on any
// reasonable mainnet-sized network. If this ever becomes a hot spot (e.g.
// the network grows to tens of thousands of hosts), convert it to a
// streaming two-pass approach instead of accumulating pairs in memory.
func migrateFeegrantsForFees(
	ctx context.Context,
	authzKeeper authzkeeper.Keeper,
	feegrantKeeper feegrantkeeper.Keeper,
	k keeper.Keeper,
) error {
	type grantPair struct {
		granter sdk.AccAddress
		grantee sdk.AccAddress
	}
	seen := make(map[string]bool)
	var pairs []grantPair

	startInferenceMsgType := sdk.MsgTypeURL(&types.MsgStartInference{})
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
		pairs = append(pairs, grantPair{granter: granterAddr, grantee: granteeAddr})
		return false
	})

	k.LogInfo("Found cold→warm pairs needing feegrant allowance", types.Upgrades, "count", len(pairs))

	expirationTime := sdk.UnwrapSDKContext(ctx).BlockTime().Add(MigratedFeeAllowanceExpiration)
	created := 0
	skipped := 0
	for _, pair := range pairs {
		// Skip if an allowance already exists (idempotent re-runs)
		existing, _ := feegrantKeeper.GetAllowance(ctx, pair.granter, pair.grantee)
		if existing != nil {
			skipped++
			continue
		}
		allowance := &feegrant.BasicAllowance{
			SpendLimit: MigratedFeeAllowance,
			Expiration: &expirationTime,
		}
		if err := feegrantKeeper.GrantAllowance(ctx, pair.granter, pair.grantee, allowance); err != nil {
			k.LogError("Failed to grant feegrant allowance during upgrade",
				types.Upgrades,
				"granter", pair.granter.String(),
				"grantee", pair.grantee.String(),
				"error", err,
			)
			// Continue processing other pairs — one failure should not abort the upgrade.
			continue
		}
		created++
	}
	k.LogInfo("Feegrant migration complete", types.Upgrades, "created", created, "skipped", skipped)
	return nil
}

func adjustParameters(ctx context.Context, k keeper.Keeper) error {
	// For start, a simple roundtrip for params to clear out now-removed values
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	params.XXX_DiscardUnknown()

	if params.ValidationParams == nil {
		params.ValidationParams = types.DefaultValidationParams()
	}
	params.ValidationParams.LogprobsMode = types.DefaultLogprobsMode

	if params.EpochParams == nil {
		params.EpochParams = types.DefaultEpochParams()
	}
	params.EpochParams.ConfirmationPocSafetyWindow = 500

	err = k.SetParams(ctx, params)
	if err != nil {
		return err
	}

	genesisParams, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		return errors.New("genesis only params not found")
	}
	genesisParams.XXX_DiscardUnknown()
	err = k.SetGenesisOnlyParams(ctx, &genesisParams)
	if err != nil {
		return err
	}
	return nil
}

func adjustBLSParameters(ctx context.Context, blsKeeper blskeeper.Keeper) error {
	params, err := blsKeeper.GetParams(ctx)
	if err != nil {
		return err
	}

	defaults := blstypes.DefaultParams()
	if params.ITotalSlots == 0 {
		params = defaults
	}
	if params.DisputePhaseDurationBlocks <= 0 {
		params.DisputePhaseDurationBlocks = defaults.DisputePhaseDurationBlocks
	}
	if params.MaxSigningAttempts == 0 {
		params.MaxSigningAttempts = defaults.MaxSigningAttempts
	}

	return blsKeeper.SetParams(ctx, params)
}

// Note on the collect-first pattern used by all four migrations below.
//
// Each migration rewrites the SAME keys it is iterating: SetEpochBLSData,
// StoreThresholdSigningRequest, SetGroupKeyValidationState, and
// SetBridgeTransaction all persist back to the prefix the iterator is
// scoped to (plus additional sub-key prefixes).
//
// Under Cosmos SDK's cache-kv store, write-during-iterate is in fact safe:
// the iterator snapshots store.sortedCache via a COW Copy() at open
// (cosmossdk.io/store/cachekv/store.go:192), so subsequent writes land in
// the live cache without affecting the iterator's snapshot. But that
// guarantee is an implementation detail of the cache-kv store — a Walk
// helper called against a different backing (raw IAVL, a different cache
// wrapper, a memdb-backed test harness) has no such guarantee, and quietly
// relying on it is a footgun for future callers and maintainers.
//
// So every migration here buffers all entries first, closes the iterator,
// then writes. Matches the DeleteGroupValidationPartialSignaturesForEpoch
// pattern already established in x/bls/keeper/group_validation.go and
// removes the cache-kv-specific invariant. All four migration datasets
// are bounded (low tens of MB at most on mainnet) so buffering is cheap.

// migrateEpochBLSDataToSubKeys migrates every existing EpochBLSData entry
// from the legacy "everything inline" layout to the v0.2.12 split layout
// where DealerParts, VerificationSubmissions, and DealerComplaints live
// under per-entry sub-keys.
//
// The fix that split these fields relies on the invariant that no inline
// entries linger in the base struct after upgrade — if a verifier tx lands
// post-upgrade and the base still has legacy inline entries, the handler
// nulls them out before SetEpochBLSData to avoid the O(N) re-sync cost,
// which would silently discard the legacy data. This migration runs once
// in the upgrade block (before any user txs) to eliminate that risk.
//
// Buffers first, then writes (see package comment above). SetEpochBLSData
// itself does the work: its inline sync loops write any populated entries
// to sub-keys and persist the base with the split fields zeroed.
// Re-running is a no-op because a migrated entry has no inline data left
// to sync.
func migrateEpochBLSDataToSubKeys(ctx sdk.Context, blsKeeper blskeeper.Keeper) error {
	var toMigrate []blstypes.EpochBLSData
	if err := blsKeeper.WalkEpochBLSData(ctx, func(ebd blstypes.EpochBLSData) error {
		hasInline := len(ebd.DealerParts) > 0 ||
			len(ebd.VerificationSubmissions) > 0 ||
			len(ebd.DealerComplaints) > 0
		if !hasInline {
			return nil
		}
		toMigrate = append(toMigrate, ebd)
		return nil
	}); err != nil {
		return err
	}
	for _, ebd := range toMigrate {
		if err := blsKeeper.SetEpochBLSData(ctx, ebd); err != nil {
			return fmt.Errorf("migrate EpochBLSData epoch=%d: %w", ebd.EpochId, err)
		}
	}
	return nil
}

// migrateThresholdSigningRequestsToSubKeys splits legacy inline
// ThresholdSigningRequest.PartialSignatures into per-submitter sub-keys.
// Same rationale as migrateEpochBLSDataToSubKeys: the post-split
// AddPartialSignature nulls out PartialSignatures before persisting the
// base request, so legacy inline entries must be moved to sub-keys before
// any post-upgrade tx can touch state.
//
// Buffers first, then writes (see package comment above). Idempotent.
func migrateThresholdSigningRequestsToSubKeys(ctx sdk.Context, blsKeeper blskeeper.Keeper) error {
	var toMigrate []blstypes.ThresholdSigningRequest
	if err := blsKeeper.WalkRawThresholdSigningRequests(ctx, func(req blstypes.ThresholdSigningRequest) error {
		if len(req.PartialSignatures) == 0 {
			return nil
		}
		toMigrate = append(toMigrate, req)
		return nil
	}); err != nil {
		return err
	}
	for i := range toMigrate {
		req := toMigrate[i]
		if err := blsKeeper.StoreThresholdSigningRequest(ctx, &req); err != nil {
			return fmt.Errorf("migrate ThresholdSigningRequest %x: %w", req.RequestId, err)
		}
	}
	return nil
}

// migrateGroupKeyValidationStatesToSubKeys splits legacy inline
// GroupKeyValidationState.PartialSignatures into per-participant sub-keys.
// SetGroupKeyValidationState handles the sync via syncInlinePartialsToSubKeys
// (resolving addr→index from the previous epoch's Participants) and
// persists the base with PartialSignatures zeroed.
//
// Buffers first, then writes (see package comment above). Idempotent.
func migrateGroupKeyValidationStatesToSubKeys(ctx sdk.Context, blsKeeper blskeeper.Keeper) error {
	var toMigrate []blstypes.GroupKeyValidationState
	if err := blsKeeper.WalkGroupKeyValidationStates(ctx, func(state blstypes.GroupKeyValidationState) error {
		if len(state.PartialSignatures) == 0 {
			return nil
		}
		toMigrate = append(toMigrate, state)
		return nil
	}); err != nil {
		return err
	}
	for i := range toMigrate {
		state := toMigrate[i]
		if err := blsKeeper.SetGroupKeyValidationState(ctx, &state); err != nil {
			return fmt.Errorf("migrate GroupKeyValidationState new_epoch=%d: %w", state.NewEpochId, err)
		}
	}
	return nil
}

// migrateBridgeTransactionValidatorsToSubKeys splits legacy inline
// BridgeTransaction.Validators into a per-validator KeySet. The
// post-split bridge-exchange handler nulls out Validators before calling
// SetBridgeTransaction to avoid re-syncing every validator on every
// confirmation; without this migration, that null-out would drop any
// legacy inline entries that hadn't been synced to the KeySet yet.
//
// Re-calling SetBridgeTransaction with the rehydrated tx drives
// SetBridgeTransaction's own sync loop, which writes inline entries to
// the KeySet and persists the base with Validators stripped.
//
// Buffers first, then writes (see package comment above). Idempotent.
func migrateBridgeTransactionValidatorsToSubKeys(ctx context.Context, k keeper.Keeper) error {
	iter, err := k.BridgeTransactionsMap.Iterate(ctx, nil)
	if err != nil {
		return fmt.Errorf("iterate bridge transactions for migration: %w", err)
	}
	var toMigrate []types.BridgeTransaction
	for ; iter.Valid(); iter.Next() {
		tx, err := iter.Value()
		if err != nil {
			iter.Close()
			return fmt.Errorf("decode bridge transaction for migration: %w", err)
		}
		if len(tx.Validators) == 0 {
			continue
		}
		toMigrate = append(toMigrate, tx)
	}
	iter.Close()
	for i := range toMigrate {
		tx := toMigrate[i]
		k.SetBridgeTransaction(ctx, &tx)
	}
	return nil
}

func removeTopMiner(ctx context.Context, k keeper.Keeper) error {
	err := k.TopMiners.Clear(ctx, nil)
	if err != nil {
		return err
	}
	tokenomicsData, found := k.GetTokenomicsData(ctx)
	if !found {
		return errors.New("tokenomics data not found")
	}
	tokenomicsData.XXX_DiscardUnknown()
	err = k.SetTokenomicsData(ctx, tokenomicsData)
	if err != nil {
		return err
	}
	return nil
}

func clearTrainingState(ctx context.Context, k keeper.Keeper) error {
	return k.ClearTrainingState(ctx)
}

// clearLegacyPoCv2Data removes all entries under the legacy PoC v2 prefixes
// (38, 39, 40). These collections changed key codec in v0.2.12 -- model_id was
// added to the key -- and were moved to new prefixes (58, 59, 60). The old
// entries cannot be decoded with the new codec, so we clear them with raw
// store iteration. Safe because this data is ephemeral per-epoch and the first
// post-upgrade epoch writes fresh records under the new prefixes.
func clearLegacyPoCv2Data(ctx context.Context, k keeper.Keeper) error {
	return k.ClearLegacyPoCv2Data(ctx)
}

func kimiWeightScaleFactor(base *types.Decimal) *types.Decimal {
	baseDec := decimal.NewFromInt(1)
	if base != nil {
		baseDec = base.ToDecimal()
	}
	scaled := baseDec.
		Mul(decimal.NewFromInt(6400)).
		Div(decimal.NewFromInt(1822))
	return types.DecimalFromDecimal(scaled)
}

func kimiPoCModelConfig(baseWeightScaleFactor *types.Decimal, penaltyStartEpoch uint64) *types.PoCModelConfig {
	return &types.PoCModelConfig{
		ModelId: kimiModelID,
		SeqLen:  1024,
		StatTest: &types.PoCStatTestParams{
			DistThreshold:   &types.Decimal{Value: 4, Exponent: -1},
			PMismatch:       &types.Decimal{Value: 1, Exponent: -1},
			PValueThreshold: &types.Decimal{Value: 5, Exponent: -2},
		},
		WeightScaleFactor: kimiWeightScaleFactor(baseWeightScaleFactor),
		PenaltyStartEpoch: penaltyStartEpoch,
	}
}

func kimiPenaltyStartEpoch(ctx context.Context, k keeper.Keeper) uint64 {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogInfo("no effective epoch for Kimi penalty start; using fallback", types.Upgrades,
			"model_id", kimiModelID, "penalty_start_epoch", 2)
		return 2
	}
	return epochIndex + 3
}

func ensureKimiPoCModelConfig(ctx context.Context, k keeper.Keeper, poc *types.PocParams) bool {
	if poc == nil {
		return false
	}
	for _, model := range poc.Models {
		if model != nil && model.ModelId == kimiModelID {
			return false
		}
	}
	poc.Models = append(poc.Models, kimiPoCModelConfig(poc.WeightScaleFactor, kimiPenaltyStartEpoch(ctx, k)))
	return true
}

// migrateParams populates PocParams.Models from the deprecated singular fields
// (ModelId, SeqLen, StatTest, WeightScaleFactor) and initializes
// DelegationParams with defaults. Idempotent: skips work if Models is already
// populated.
func migrateParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	poc := params.PocParams
	if poc != nil && len(poc.Models) == 0 {
		poc.Models = []*types.PoCModelConfig{
			{
				ModelId:           poc.ModelId,
				SeqLen:            poc.SeqLen,
				StatTest:          poc.StatTest,
				WeightScaleFactor: poc.WeightScaleFactor,
				PenaltyStartEpoch: 0,
			},
		}
		k.LogInfo("migrated PocParams singular fields into models[]", types.Upgrades,
			"model_id", poc.ModelId, "seq_len", poc.SeqLen)
	}
	if ensureKimiPoCModelConfig(ctx, k, poc) {
		k.LogInfo("added Kimi model to PocParams models[]", types.Upgrades,
			"model_id", kimiModelID, "seq_len", 1024)
	}

	if params.DelegationParams == nil {
		defaults := types.DefaultDelegationParams()
		params.DelegationParams = defaults
		k.LogInfo("initialized DelegationParams with defaults", types.Upgrades,
			"deploy_window", defaults.DeployWindow,
			"v_min", defaults.VMin)
	}
	params.DelegationParams.RefusalPenalty = types.DecimalFromFloat(0.1)
	params.DelegationParams.NoParticipationPenalty = types.DecimalFromFloat(0.15)
	params.DelegationParams.DelegationShare = types.DecimalFromFloat(0.05)
	params.DelegationParams.CapFactor = types.DecimalFromFloat(0.75)
	params.DelegationParams.DeployWindow = 500
	if poc != nil && params.DelegationParams.InitialModelId == "" {
		params.DelegationParams.InitialModelId = poc.ModelId
	}

	// MaxModelVotingPowerPercentage is a fraction, so 0.3 means 30%.
	params.DelegationParams.MaxModelVotingPowerPercentage = types.DecimalFromFloat(0.3)
	clearDeprecatedPocParams(poc)

	return k.SetParams(ctx, params)
}

func clearDeprecatedPocParams(poc *types.PocParams) {
	if poc == nil {
		return
	}
	poc.WeightScaleFactor = nil
	poc.ModelParams = nil
	poc.ModelId = ""
	poc.SeqLen = 0
	poc.StatTest = nil
}

func kimiGovernanceModel(authority string) *types.Model {
	return &types.Model{
		ProposedBy:             authority,
		Id:                     kimiModelID,
		UnitsOfComputePerToken: 10000,
		HfRepo:                 kimiModelID,
		HfCommit:               "5a49d036ab7472b7d5912ded487150ec1358c11d",
		ModelArgs: []string{
			"--max-model-len", "240000",
			"--tool-call-parser", "kimi_k2",
			"--reasoning-parser", "kimi_k2",
		},
		VRam:                720,
		ThroughputPerNonce:  1500,
		ValidationThreshold: &types.Decimal{Value: 920, Exponent: -3},
	}
}

func updateGovernanceModels(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.PocParams == nil {
		return errors.New("poc params not found")
	}

	approved := make(map[string]bool)
	for _, modelConfig := range params.PocParams.GetModelConfigs() {
		if modelConfig == nil || modelConfig.ModelId == "" {
			continue
		}
		approved[modelConfig.ModelId] = true
	}

	k.SetModel(ctx, kimiGovernanceModel(k.GetAuthority()))

	models, err := k.GetGovernanceModels(ctx)
	if err != nil {
		return err
	}
	for _, model := range models {
		if model == nil {
			continue
		}
		if !approved[model.Id] {
			k.DeleteGovernanceModel(ctx, model.Id)
			k.LogInfo("removed governance model not present in PocParams", types.Upgrades,
				"model_id", model.Id)
		}
	}
	k.LogInfo("updated governance models for PocParams", types.Upgrades,
		"approved_count", len(approved), "kimi_model_id", kimiModelID)
	return nil
}

// initNewPruningState seeds the four pruning-state fields introduced in
// v0.2.12 (PocValidationsV2, PocV2StoreCommits, MlnodeWeightDistributions,
// PocValidationSnapshots) to the current effective epoch index. Without this,
// the first post-upgrade Prune() call would walk every historical epoch from
// 1 to currentEpoch-threshold finding empty ranges and writing a PruningState
// update per epoch. Seeding to currentEpoch makes startEpoch > endEpoch, so
// the pruners wait for fresh data to accumulate under the new prefixes.
func initNewPruningState(ctx context.Context, k keeper.Keeper) error {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogInfo("initNewPruningState: no effective epoch, skipping", types.Upgrades)
		return nil
	}
	current := int64(epochIndex)

	state, err := k.PruningState.Get(ctx)
	if err != nil {
		return err
	}
	if state.PocValidationsV2PrunedEpoch < current {
		state.PocValidationsV2PrunedEpoch = current
	}
	if state.PocV2StoreCommitsPrunedEpoch < current {
		state.PocV2StoreCommitsPrunedEpoch = current
	}
	if state.MlnodeWeightDistributionsPrunedEpoch < current {
		state.MlnodeWeightDistributionsPrunedEpoch = current
	}
	if state.PocValidationSnapshotsPrunedEpoch < current {
		state.PocValidationSnapshotsPrunedEpoch = current
	}
	if err := k.PruningState.Set(ctx, state); err != nil {
		return err
	}
	k.LogInfo("initNewPruningState: seeded new pruning markers", types.Upgrades,
		"epoch", current)
	return nil
}

// backfillVotingPower populates AP.VotingPowers for the current epoch and
// ValidationWeight.voting_power for the current epoch's model subgroups.
// Pre-upgrade state is single-model with no delegation, so every participant
// is DIRECT and their voting_power equals their consensus weight.
//
// This is required because getEffectiveValidationBaseState reads voting_power
// from EpochGroupData subgroups; zero values would break validation acceptance
// for the first post-upgrade epoch.
func backfillVotingPower(ctx context.Context, k keeper.Keeper) error {
	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogInfo("backfillVotingPower: no effective epoch, skipping", types.Upgrades)
		return nil
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.PocParams == nil || len(params.PocParams.Models) == 0 {
		k.LogInfo("backfillVotingPower: no models configured, skipping", types.Upgrades)
		return nil
	}
	modelID := params.PocParams.Models[0].ModelId
	if modelID == "" {
		k.LogInfo("backfillVotingPower: primary model_id is empty, skipping", types.Upgrades)
		return nil
	}

	// Backfill ActiveParticipants.VotingPowers for the effective epoch.
	ap, apFound := k.GetActiveParticipants(ctx, epochIndex)
	if apFound {
		changed := false
		for _, p := range ap.Participants {
			if p == nil {
				continue
			}
			if len(p.VotingPowers) == 0 {
				p.VotingPowers = []*types.ModelVotingPower{
					{ModelId: modelID, VotingPower: p.Weight},
				}
				changed = true
			}
		}
		if changed {
			if err := k.SetActiveParticipants(ctx, ap); err != nil {
				return err
			}
			k.LogInfo("backfillVotingPower: updated ActiveParticipants", types.Upgrades,
				"epoch", epochIndex, "count", len(ap.Participants))
		}
	}

	// Backfill EpochGroupData.ValidationWeight.voting_power for the current
	// epoch's model subgroup. In single-model no-delegation, voting_power
	// equals the subgroup's consensus weight for each member.
	subgroupData, found := k.GetEpochGroupData(ctx, epochIndex, modelID)
	if !found {
		k.LogInfo("backfillVotingPower: no subgroup data for model, skipping subgroup backfill", types.Upgrades,
			"epoch", epochIndex, "model_id", modelID)
		return nil
	}
	changed := false
	for _, vw := range subgroupData.ValidationWeights {
		if vw == nil {
			continue
		}
		if vw.VotingPower == 0 && vw.Weight > 0 {
			vw.VotingPower = vw.Weight
			changed = true
		}
	}
	if changed {
		k.SetEpochGroupData(ctx, subgroupData)
		k.LogInfo("backfillVotingPower: updated EpochGroupData subgroup voting_power", types.Upgrades,
			"epoch", epochIndex, "model_id", modelID, "entries", len(subgroupData.ValidationWeights))
	}

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

	for _, v := range params.DevshardEscrowParams.ApprovedVersions {
		if v != nil && v.Name == DevshardV1Name {
			k.LogInfo("devshard approved version already present, skipping", types.Upgrades,
				"name", DevshardV1Name)
			return nil
		}
	}

	params.DevshardEscrowParams.ApprovedVersions = append(params.DevshardEscrowParams.ApprovedVersions,
		&types.DevshardApprovedVersion{
			Name:   DevshardV1Name,
			Binary: DevshardV1Binary,
			Sha256: DevshardV1Sha256,
		},
	)

	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set devshard approved versions during upgrade", types.Upgrades, "error", err)
		return err
	}
	k.LogInfo("registered initial devshard approved version", types.Upgrades,
		"name", DevshardV1Name, "binary", DevshardV1Binary, "sha256", DevshardV1Sha256)
	return nil
}

func setFeeParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	fp := types.DefaultFeeParams()
	// Note: temporary due to issue in gas estimations.
	fp.MinGasPriceNgonka = 0
	params.FeeParams = fp
	if err := k.SetParams(ctx, params); err != nil {
		k.LogError("failed to set fee params during upgrade", types.Upgrades, "error", err)
		return err
	}
	k.LogInfo("initialized fee params", types.Upgrades,
		"min_gas_price_ngonka", fp.MinGasPriceNgonka,
		"base_validation_gas", fp.BaseValidationGas,
		"gas_per_poc_count", fp.GasPerPocCount)
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

	available := k.BankView.SpendableCoin(ctx, communitySaleAddr, DefaultBountyIbcUsdtDenom).Amount.Int64()
	if available < totalRequired {
		k.Logger().Warn("insufficient community sale balance, skipping bounty distribution",
			"required", totalRequired, "available", available, "denom", DefaultBountyIbcUsdtDenom)
		return nil
	}

	k.Logger().Info("community sale balance sufficient for bounty distribution",
		"required", totalRequired, "available", available, "denom", DefaultBountyIbcUsdtDenom)

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
				"denom":     DefaultBountyIbcUsdtDenom,
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
				"address", bounty.Address, "amount", bounty.Amount, "denom", DefaultBountyIbcUsdtDenom, "error", err)
			continue
		}

		k.Logger().Info("bounty distributed from community sale contract",
			"address", bounty.Address, "amount", bounty.Amount, "denom", DefaultBountyIbcUsdtDenom)
	}

	return nil
}
