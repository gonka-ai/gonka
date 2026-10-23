package keeper

import (
	"context"
	"errors"
	"fmt"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

// RepairEpochValidationState canonicalizes retained account spellings, then
// rewrites or deletes malformed weight distributions and epoch-group node
// lists. Account validation covers participants, active sets, PoC delegation
// records, and the retained delegation snapshots. It finishes before any of
// those steps write. A second call changes nothing.
func (k Keeper) RepairEpochValidationState(ctx context.Context) error {
	if err := k.auditCanonicalAccounts(ctx); err != nil {
		return err
	}
	return k.auditStoredWeightState(ctx)
}

type participantRewrite struct {
	key   sdk.AccAddress
	value types.Participant
}

type activeParticipantRewrite struct {
	suffix []byte
	record types.ActiveParticipants
}

func (k Keeper) auditCanonicalAccounts(ctx context.Context) error {
	participantRewrites, participantsScanned, err := k.planParticipantCanonicalRewrites(ctx)
	if err != nil {
		k.logCanonicalAddressAudit(participantsScanned, 0, 0, 0, 1)
		return err
	}
	activeRewrites, activeScanned, err := k.planActiveParticipantCanonicalRewrites(ctx)
	if err != nil {
		k.logCanonicalAddressAudit(participantsScanned, 0, activeScanned, 0, 1)
		return err
	}
	delegationPlan, err := k.planDelegationCanonicalState(ctx)
	if err != nil {
		k.logCanonicalAddressAudit(participantsScanned, 0, activeScanned, 0, 1)
		return err
	}

	for _, rewrite := range participantRewrites {
		if err := k.Participants.Set(ctx, rewrite.key, rewrite.value); err != nil {
			return err
		}
	}
	if len(activeRewrites) > 0 {
		storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
		store := prefix.NewStore(storeAdapter, []byte(types.ActiveParticipantsKeyPrefix))
		for _, rewrite := range activeRewrites {
			bz, err := k.cdc.Marshal(&rewrite.record)
			if err != nil {
				return err
			}
			store.Set(rewrite.suffix, bz)
		}
	}
	if err := k.applyDelegationCanonicalState(ctx, delegationPlan); err != nil {
		return err
	}

	k.logCanonicalAddressAudit(participantsScanned, len(participantRewrites), activeScanned, len(activeRewrites), 0)
	return nil
}

func (k Keeper) logCanonicalAddressAudit(participantsScanned, participantsRewritten, activeScanned, activeRewritten, invalidOrDuplicate int) {
	k.LogInfo("canonical address audit", types.Upgrades,
		"participantsScanned", participantsScanned,
		"participantsRewritten", participantsRewritten,
		"activeRecordsScanned", activeScanned,
		"activeRecordsRewritten", activeRewritten,
		"invalidOrDuplicate", invalidOrDuplicate,
	)
}

func (k Keeper) planParticipantCanonicalRewrites(ctx context.Context) ([]participantRewrite, int, error) {
	iter, err := k.Participants.Iterate(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer iter.Close()

	var rewrites []participantRewrite
	scanned := 0
	for ; iter.Valid(); iter.Next() {
		scanned++
		key, err := iter.Key()
		if err != nil {
			return nil, scanned, err
		}
		value, err := iter.Value()
		if err != nil {
			return nil, scanned, err
		}
		if err := accountMatchesStoreKey(value.Index, key); err != nil {
			return nil, scanned, sdkerrors.Wrap(types.ErrIllegalState, "participant index does not match store key")
		}
		if err := accountMatchesStoreKey(value.Address, key); err != nil {
			return nil, scanned, sdkerrors.Wrap(types.ErrIllegalState, "participant address does not match store key")
		}
		canonical := key.String()
		if value.Index == canonical && value.Address == canonical {
			continue
		}
		value.Index = canonical
		value.Address = canonical
		keyCopy := append(sdk.AccAddress(nil), key...)
		rewrites = append(rewrites, participantRewrite{key: keyCopy, value: value})
	}
	return rewrites, scanned, nil
}

func accountMatchesStoreKey(bech32 string, key sdk.AccAddress) error {
	addr, err := sdk.AccAddressFromBech32(bech32)
	if err != nil {
		return err
	}
	if !addr.Equals(key) {
		return fmt.Errorf("decoded account does not match store key")
	}
	return nil
}

func (k Keeper) planActiveParticipantCanonicalRewrites(ctx context.Context) ([]activeParticipantRewrite, int, error) {
	storeAdapter := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store := prefix.NewStore(storeAdapter, []byte(types.ActiveParticipantsKeyPrefix))
	iter := store.Iterator(nil, nil)
	defer iter.Close()

	var rewrites []activeParticipantRewrite
	scanned := 0
	for ; iter.Valid(); iter.Next() {
		scanned++
		var record types.ActiveParticipants
		if err := k.cdc.Unmarshal(iter.Value(), &record); err != nil {
			return nil, scanned, err
		}
		changed, err := canonicalizeActiveParticipantIndexes(&record)
		if err != nil {
			return nil, scanned, err
		}
		if !changed {
			continue
		}
		rewrites = append(rewrites, activeParticipantRewrite{
			suffix: append([]byte(nil), iter.Key()...),
			record: record,
		})
	}
	return rewrites, scanned, nil
}

func canonicalizeActiveParticipantIndexes(record *types.ActiveParticipants) (bool, error) {
	if record == nil {
		return false, nil
	}
	seen := make(map[string]struct{}, len(record.Participants))
	changed := false
	for _, participant := range record.Participants {
		if participant == nil {
			continue
		}
		addr, err := sdk.AccAddressFromBech32(participant.Index)
		if err != nil {
			return false, sdkerrors.Wrap(types.ErrIllegalState, "active participant index is not an account address")
		}
		canonical := addr.String()
		if _, dup := seen[canonical]; dup {
			return false, sdkerrors.Wrap(types.ErrIllegalState, "active participants contain duplicate account identities")
		}
		seen[canonical] = struct{}{}
		if participant.Index != canonical {
			participant.Index = canonical
			changed = true
		}
	}
	return changed, nil
}

type weightAuditStats struct {
	distributionsScanned                  int
	distributionsRewritten                int
	distributionsDeleted                  int
	zeroWeightRowsRemoved                 int
	distributionsDeletedMalformedPositive int
	epochGroupsRewritten                  int
	mlNodesRemoved                        int
	bucketsCleared                        int
}

func (k Keeper) auditStoredWeightState(ctx context.Context) error {
	stats := weightAuditStats{}
	if err := k.auditMLNodeWeightDistributions(ctx, &stats); err != nil {
		return err
	}
	if err := k.auditEpochGroupMLNodes(ctx, &stats); err != nil {
		return err
	}
	k.LogInfo("epoch validation weight audit", types.Upgrades,
		"distributionsScanned", stats.distributionsScanned,
		"distributionsRewritten", stats.distributionsRewritten,
		"distributionsDeleted", stats.distributionsDeleted,
		"zeroWeightRowsRemoved", stats.zeroWeightRowsRemoved,
		"distributionsDeletedMalformedPositive", stats.distributionsDeletedMalformedPositive,
		"epochGroupsRewritten", stats.epochGroupsRewritten,
		"mlNodesRemoved", stats.mlNodesRemoved,
		"bucketsCleared", stats.bucketsCleared,
	)
	return nil
}

func (k Keeper) auditMLNodeWeightDistributions(ctx context.Context, stats *weightAuditStats) error {
	iter, err := k.MLNodeWeightDistributions.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return err
		}
		value, err := iter.Value()
		if err != nil {
			return err
		}
		stats.distributionsScanned++
		retained, malformedPositive, zerosRemoved := classifyStoredWeightRows(value.Weights)
		commit, err := k.PoCV2StoreCommits.Get(ctx, key)
		missingCommit := errors.Is(err, collections.ErrNotFound)
		if err != nil && !missingCommit {
			return err
		}
		sum, overflow := sumStoredWeights(retained)
		deleteDistribution := malformedPositive || missingCommit || overflow || sum != uint64(commit.Count)
		if deleteDistribution {
			if err := k.MLNodeWeightDistributions.Remove(ctx, key); err != nil {
				return err
			}
			stats.distributionsDeleted++
			stats.zeroWeightRowsRemoved += zerosRemoved
			if malformedPositive {
				stats.distributionsDeletedMalformedPositive++
			}
			k.LogInfo("epoch validation audit: distribution deleted", types.Upgrades,
				"stage", key.K1(),
				"participant", key.K2().String(),
				"model", key.K3(),
				"zeroWeightRowsRemoved", zerosRemoved,
				"malformedPositive", malformedPositive,
			)
			continue
		}
		if zerosRemoved == 0 {
			continue
		}
		value.Weights = retained
		if err := k.MLNodeWeightDistributions.Set(ctx, key, value); err != nil {
			return err
		}
		stats.distributionsRewritten++
		stats.zeroWeightRowsRemoved += zerosRemoved
		k.LogInfo("epoch validation audit: distribution rewritten", types.Upgrades,
			"stage", key.K1(),
			"participant", key.K2().String(),
			"model", key.K3(),
			"zeroWeightRowsRemoved", zerosRemoved,
		)
	}
	return nil
}

// classifyStoredWeightRows keeps positive rows with a non-empty unique node id
// no longer than MaxMLNodeWeightNodeIDBytes. Nil and zero-weight rows are
// removable. A positive row with an empty, oversized, or repeated node id
// makes the whole distribution undeletable-by-rewrite: the caller deletes it
// instead of merging weights.
func classifyStoredWeightRows(weights []*types.MLNodeWeight) (retained []*types.MLNodeWeight, malformedPositive bool, zerosRemoved int) {
	seen := make(map[string]struct{}, len(weights))
	retained = make([]*types.MLNodeWeight, 0, len(weights))
	for _, weight := range weights {
		if weight == nil || weight.Weight == 0 {
			zerosRemoved++
			continue
		}
		if weight.NodeId == "" || len(weight.NodeId) > types.MaxMLNodeWeightNodeIDBytes {
			return nil, true, zerosRemoved
		}
		if _, dup := seen[weight.NodeId]; dup {
			return nil, true, zerosRemoved
		}
		seen[weight.NodeId] = struct{}{}
		retained = append(retained, &types.MLNodeWeight{NodeId: weight.NodeId, Weight: weight.Weight})
	}
	return retained, false, zerosRemoved
}

func sumStoredWeights(weights []*types.MLNodeWeight) (uint64, bool) {
	var sum uint64
	for _, weight := range weights {
		if weight == nil {
			continue
		}
		next := sum + uint64(weight.Weight)
		if next < sum {
			return 0, true
		}
		sum = next
	}
	return sum, false
}

func (k Keeper) auditEpochGroupMLNodes(ctx context.Context, stats *weightAuditStats) error {
	iter, err := k.EpochGroupDataMap.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return err
		}
		group, err := iter.Value()
		if err != nil {
			return err
		}
		groupChanged := false
		for _, vw := range group.ValidationWeights {
			if vw == nil {
				continue
			}
			cleaned, cleared, removed, changed := sanitizeEpochMLNodes(vw.MlNodes)
			if !changed {
				continue
			}
			groupChanged = true
			stats.mlNodesRemoved += removed
			if cleared {
				stats.bucketsCleared++
				vw.MlNodes = nil
			} else {
				vw.MlNodes = cleaned
			}
			k.LogInfo("epoch validation audit: epoch group ml_nodes changed", types.Upgrades,
				"epoch", group.EpochIndex,
				"participant", vw.MemberAddress,
				"model", group.ModelId,
				"mlNodesRemoved", removed,
				"bucketCleared", cleared,
			)
		}
		if !groupChanged {
			continue
		}
		if err := k.EpochGroupDataMap.Set(ctx, key, group); err != nil {
			return err
		}
		stats.epochGroupsRewritten++
	}
	return nil
}

// sanitizeEpochMLNodes drops nil and zero-weight entries. An empty node id, a
// node id longer than MaxMLNodeWeightNodeIDBytes, or a duplicate positive id
// clears the whole bucket. ValidationWeight.Weight is left to the caller.
func sanitizeEpochMLNodes(nodes []*types.MLNodeInfo) (cleaned []*types.MLNodeInfo, cleared bool, removed int, changed bool) {
	seen := make(map[string]struct{}, len(nodes))
	cleaned = make([]*types.MLNodeInfo, 0, len(nodes))
	for _, node := range nodes {
		if node == nil || node.PocWeight <= 0 {
			removed++
			changed = true
			continue
		}
		if node.NodeId == "" || len(node.NodeId) > types.MaxMLNodeWeightNodeIDBytes {
			return nil, true, removed, true
		}
		if _, dup := seen[node.NodeId]; dup {
			return nil, true, removed, true
		}
		seen[node.NodeId] = struct{}{}
		cleaned = append(cleaned, node)
	}
	if !changed {
		return nodes, false, 0, false
	}
	return cleaned, false, removed, true
}
