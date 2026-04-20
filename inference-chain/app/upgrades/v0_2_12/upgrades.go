package v0_2_12

import (
	"context"
	"errors"
	"fmt"
	"time"

	sdkmath "cosmossdk.io/math"
	"cosmossdk.io/x/feegrant"
	feegrantkeeper "cosmossdk.io/x/feegrant/keeper"
	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/cosmos-sdk/x/authz"
	authzkeeper "github.com/cosmos/cosmos-sdk/x/authz/keeper"
	distrkeeper "github.com/cosmos/cosmos-sdk/x/distribution/keeper"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// MigratedFeeAllowance is the BasicAllowance limit auto-granted during the
// v0.2.12 upgrade for every existing cold→warm authz pair. Sized to comfortably
// cover many months of routine DAPI operation; hosts can refresh by re-running
// `inferenced tx inference grant-ml-ops-permissions` when depleted.
var MigratedFeeAllowance = sdk.NewCoins(sdk.NewCoin("ngonka", sdkmath.NewInt(100_000_000_000))) // 100 GNK

// MigratedFeeAllowanceExpiration is how long the auto-granted allowance lasts.
const MigratedFeeAllowanceExpiration = 365 * 24 * time.Hour

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper,
	_ distrkeeper.Keeper,
	blsKeeper blskeeper.Keeper,
	authzKeeper authzkeeper.Keeper,
	feegrantKeeper feegrantkeeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
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

	if params.DelegationParams == nil {
		defaults := types.DefaultDelegationParams()
		params.DelegationParams = defaults
		k.LogInfo("initialized DelegationParams with defaults", types.Upgrades,
			"deploy_window", defaults.DeployWindow,
			"v_min", defaults.VMin)
	}
	if poc != nil && params.DelegationParams.InitialModelId == "" {
		params.DelegationParams.InitialModelId = poc.ModelId
	}

	// Per-model voting-power concentration cap (field 9) is new in v0.2.12.
	// Set explicitly to 0 (disabled) so the on-chain params struct carries
	// the new field from day one. Governance can raise it later via
	// MsgUpdateParams once real network concentration is observable.
	params.DelegationParams.MaxModelVotingPowerPercentage = types.DecimalFromFloat(0)

	return k.SetParams(ctx, params)
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

func setFeeParams(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}

	fp := types.DefaultFeeParams()
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
