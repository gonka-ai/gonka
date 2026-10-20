package v0_2_17

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

var trainingWarmKeyMsgTypeURLs = []string{
	sdk.MsgTypeURL(&types.MsgRefreshTrainingNodeOptIn{}),
	sdk.MsgTypeURL(&types.MsgAutokickTrainshardNode{}),
}

var retiredTrainshardAutokickRequestPrefix = collections.NewPrefix(141)

func backfillTrainingParamDefaults(ctx context.Context, k keeper.Keeper) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.TrainingParams == nil {
		params.TrainingParams = types.DefaultTrainingParams()
	}

	changed := params.TrainingParams.OptInTtlBlocks == 0 || params.TrainingParams.ReleaseBufferBlocks == 0 ||
		params.TrainingParams.NodeWeightToleranceBps == 0 || params.TrainingParams.MinWeightRateParticipants == 0
	if params.TrainingParams.OptInTtlBlocks == 0 {
		params.TrainingParams.OptInTtlBlocks = types.DefaultTrainingOptInTtlBlocks
	}
	if params.TrainingParams.ReleaseBufferBlocks == 0 {
		params.TrainingParams.ReleaseBufferBlocks = types.DefaultTrainingReleaseBufferBlocks
	}
	if params.TrainingParams.NodeWeightToleranceBps == 0 {
		params.TrainingParams.NodeWeightToleranceBps = types.DefaultTrainingNodeWeightTolerance
	}
	if params.TrainingParams.MinWeightRateParticipants == 0 {
		params.TrainingParams.MinWeightRateParticipants = types.DefaultTrainingMinRateParticipants
	}
	// a zero cap stops training dead, so it can never be what the chain meant
	if params.TrainingParams.MaxActiveShards == 0 {
		params.TrainingParams.MaxActiveShards = types.DefaultTrainingMaxActiveShards
	}
	if params.TrainingParams.MaxActiveShardsPerCreator == 0 {
		params.TrainingParams.MaxActiveShardsPerCreator = types.DefaultTrainingMaxActivePerCreator
	}
	if params.TrainingParams.MaxTotalReservedNodes == 0 {
		params.TrainingParams.MaxTotalReservedNodes = types.DefaultTrainingMaxTotalReserved
	}
	if params.TrainingParams.MaxNodesPerShard == 0 {
		params.TrainingParams.MaxNodesPerShard = types.DefaultTrainingMaxNodesPerShard
	}
	if params.TrainingParams.MaxExpirationsPerBlock == 0 {
		params.TrainingParams.MaxExpirationsPerBlock = types.DefaultTrainingMaxExpirationsPerBlk
	}

	// retention is tuned for short epochs, so raise it to the minimum this chain's
	// epoch length requires before training can be enabled. The opt-in ttl stays
	// short: the daemon refreshes it every few minutes, and a longer one only keeps
	// a host whose daemon is gone eligible for assembly
	if epoch := params.EpochParams; epoch != nil && epoch.EpochLength > 0 {
		minRetention := 2*epoch.EpochLength + params.TrainingParams.ReleaseBufferBlocks
		if params.TrainingParams.SettledShardRetentionBlocks < minRetention {
			params.TrainingParams.SettledShardRetentionBlocks = minRetention
		}
	}

	if err := k.SetParams(ctx, params); err != nil {
		return err
	}
	k.LogInfo("backfilled training param defaults", types.Upgrades,
		"filled_new_fields", changed,
		"opt_in_ttl_blocks", params.TrainingParams.OptInTtlBlocks,
		"release_buffer_blocks", params.TrainingParams.ReleaseBufferBlocks,
		"node_weight_tolerance_bps", params.TrainingParams.NodeWeightToleranceBps,
		"min_weight_rate_participants", params.TrainingParams.MinWeightRateParticipants,
		"settled_shard_retention_blocks", params.TrainingParams.SettledShardRetentionBlocks,
	)
	return nil
}

func backfillTrainshardNodeStatuses(ctx context.Context, k keeper.Keeper) error {
	var updated []types.Trainshard
	if err := k.Trainshards.Walk(ctx, nil, func(_ uint64, shard types.Trainshard) (bool, error) {
		changed := false
		for _, node := range shard.Nodes {
			if node.Status != types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_UNSPECIFIED {
				continue
			}
			if shard.Status == types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE {
				node.Status = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE
			} else {
				node.Status = types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE
				node.ReleasedAtHeight = shard.ClosedAtHeight
				node.ReservedUntilHeight = shard.ClosedAtHeight
			}
			changed = true
		}
		if changed {
			updated = append(updated, shard)
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("walk trainshards for node status backfill: %w", err)
	}

	for _, shard := range updated {
		if err := k.Trainshards.Set(ctx, shard.TrainshardId, shard); err != nil {
			return fmt.Errorf("set trainshard %d during node status backfill: %w", shard.TrainshardId, err)
		}
	}
	k.LogInfo("backfilled trainshard node statuses", types.Upgrades, "updated", len(updated))
	return nil
}

func backfillTrainshardWeightEpochs(ctx context.Context, k keeper.Keeper) error {
	var missing []types.Trainshard
	if err := k.Trainshards.Walk(ctx, nil, func(id uint64, shard types.Trainshard) (bool, error) {
		has, err := k.TrainshardWeightEpoch.Has(ctx, id)
		if err != nil {
			return true, err
		}
		if !has {
			missing = append(missing, shard)
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("walk trainshards for weight epoch backfill: %w", err)
	}
	if len(missing) == 0 {
		return nil
	}

	effective, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return fmt.Errorf("backfill trainshard weight epochs: %w", types.ErrEffectiveEpochNotFound)
	}
	updated := 0
	for _, shard := range missing {
		epochIndex, found := effectiveEpochAt(ctx, k, effective, shard.CreatedAtHeight)
		if !found {
			k.LogError("trainshard weight epoch unresolved, the shard carries nothing", types.Upgrades,
				"trainshard_id", shard.TrainshardId, "created_at_height", shard.CreatedAtHeight)
			continue
		}
		if err := k.TrainshardWeightEpoch.Set(ctx, shard.TrainshardId, epochIndex); err != nil {
			return fmt.Errorf("set weight epoch of trainshard %d: %w", shard.TrainshardId, err)
		}
		updated++
	}
	k.LogInfo("backfilled trainshard weight epochs", types.Upgrades, "updated", updated, "unresolved", len(missing)-updated)
	return nil
}

func effectiveEpochAt(ctx context.Context, k keeper.Keeper, effective uint64, height int64) (uint64, bool) {
	for epochIndex := effective; ; epochIndex-- {
		if data, found := k.GetEpochGroupData(ctx, epochIndex, ""); found && data.EffectiveBlockHeight < height {
			return epochIndex, true
		}
		if epochIndex == 0 {
			return 0, false
		}
	}
}

func repairTrainshardReservations(ctx context.Context, k keeper.Keeper) error {
	var reservations []collections.KeyValue[collections.Pair[string, string], uint64]
	if err := k.TrainshardReservations.Walk(ctx, nil, func(key collections.Pair[string, string], id uint64) (bool, error) {
		reservations = append(reservations, collections.KeyValue[collections.Pair[string, string], uint64]{Key: key, Value: id})
		return false, nil
	}); err != nil {
		return fmt.Errorf("walk trainshard reservations: %w", err)
	}

	type nodeRef struct{ participant, nodeId string }
	returning := make(map[nodeRef]bool)
	if err := k.TrainshardReleaseIndex.Walk(ctx, nil, func(key collections.Triple[int64, string, string]) (bool, error) {
		returning[nodeRef{key.K2(), key.K3()}] = true
		return false, nil
	}); err != nil {
		return fmt.Errorf("walk trainshard release index: %w", err)
	}

	shards := make(map[uint64]*types.Trainshard)
	dropped, indexed := 0, 0
	for _, reservation := range reservations {
		key, id := reservation.Key, reservation.Value
		shard, cached := shards[id]
		if !cached {
			loaded, err := k.Trainshards.Get(ctx, id)
			switch {
			case err == nil:
				shard = &loaded
			case !errors.Is(err, collections.ErrNotFound):
				return fmt.Errorf("read trainshard %d: %w", id, err)
			}
			shards[id] = shard
		}

		held, active := false, false
		var until int64
		if shard != nil {
			for _, n := range shard.Nodes {
				if n == nil || n.Participant != key.K1() || n.NodeId != key.K2() {
					continue
				}
				held = true
				active = active || n.Status == types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE
				until = max(until, n.ReservedUntilHeight)
			}
		}
		if !held {
			if err := k.TrainshardReservations.Remove(ctx, key); err != nil {
				return fmt.Errorf("remove reservation of node %s of %s: %w", key.K2(), key.K1(), err)
			}
			dropped++
			continue
		}
		if returning[nodeRef{key.K1(), key.K2()}] || (active && shard.Status == types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE) {
			continue
		}
		if err := k.TrainshardReleaseIndex.Set(ctx, collections.Join3(until, key.K1(), key.K2())); err != nil {
			return fmt.Errorf("index return of node %s of %s: %w", key.K2(), key.K1(), err)
		}
		indexed++
	}
	k.LogInfo("repaired trainshard reservations", types.Upgrades,
		"dropped_dangling", dropped, "returns_indexed", indexed)
	return nil
}

func clearTrainshardAutokickRequests(ctx context.Context, k keeper.Keeper) error {
	removed, err := k.ClearStorePrefix(ctx, retiredTrainshardAutokickRequestPrefix)
	if err != nil {
		return fmt.Errorf("clear trainshard autokick requests: %w", err)
	}
	k.LogInfo("cleared trainshard autokick requests", types.Upgrades, "removed", removed)
	return nil
}

func clearStaleTrainingOptIns(ctx context.Context, k keeper.Keeper) error {
	var stale []collections.Pair[string, string]
	if err := k.TrainingNodeOptIns.Walk(ctx, nil, func(key collections.Pair[string, string], expiresAt int64) (bool, error) {
		if expiresAt == 0 || types.ValidateTrainingNodeId(key.K2()) != nil {
			stale = append(stale, key)
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("walk training opt-ins: %w", err)
	}

	var unaddressable []collections.Pair[string, string]
	if err := k.TrainingNodeEndpoints.Walk(ctx, nil, func(key collections.Pair[string, string], _ string) (bool, error) {
		if types.ValidateTrainingNodeId(key.K2()) != nil {
			unaddressable = append(unaddressable, key)
		}
		return false, nil
	}); err != nil {
		return fmt.Errorf("walk training endpoints: %w", err)
	}

	for _, key := range stale {
		if err := k.TrainingNodeOptIns.Remove(ctx, key); err != nil {
			return fmt.Errorf("remove stale training opt-in %s/%s: %w", key.K1(), key.K2(), err)
		}
	}
	for _, key := range unaddressable {
		if err := k.TrainingNodeEndpoints.Remove(ctx, key); err != nil {
			return fmt.Errorf("remove training endpoint %s/%s: %w", key.K1(), key.K2(), err)
		}
	}
	k.LogInfo("cleared stale training opt-ins", types.Upgrades,
		"removed_opt_ins", len(stale), "removed_endpoints", len(unaddressable))
	return nil
}

func grantTrainingWarmKeyAuthz(ctx context.Context, authzKeeper AuthzMigrationKeeper, k keeper.Keeper) error {
	type grantPair struct {
		granter    sdk.AccAddress
		grantee    sdk.AccAddress
		expiration *time.Time
	}

	now := sdk.UnwrapSDKContext(ctx).BlockTime()
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
		// v0.2.15 copied every legacy marker to the live one, so a pair holding only the legacy
		// grant is a key its host de-warmed by revoking the live marker
		if authorization.Msg != types.WarmKeyGrantMarkerTypeURL {
			return false
		}
		if grant.Expiration != nil && !grant.Expiration.After(now) {
			return false
		}
		key := granter.String() + "->" + grantee.String()
		if !seen[key] {
			seen[key] = true
			pairs = append(pairs, grantPair{granter: granter, grantee: grantee, expiration: grant.Expiration})
		}
		return false
	})

	created, skipped := 0, 0
	for _, pair := range pairs {
		for _, msgType := range trainingWarmKeyMsgTypeURLs {
			existing, _ := authzKeeper.GetAuthorization(ctx, pair.grantee, pair.granter, msgType)
			if existing != nil {
				skipped++
				continue
			}
			authorization := authz.NewGenericAuthorization(msgType)
			if err := authzKeeper.SaveGrant(ctx, pair.grantee, pair.granter, authorization, pair.expiration); err != nil {
				return fmt.Errorf("grant %s from %s to %s: %w", msgType, pair.granter, pair.grantee, err)
			}
			created++
		}
	}
	k.LogInfo("backfilled training warm key grants", types.Upgrades,
		"pairs", len(pairs), "created", created, "skipped", skipped)
	return nil
}
