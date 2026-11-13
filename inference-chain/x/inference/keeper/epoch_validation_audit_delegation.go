package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/productscience/inference/x/inference/types"
)

// delegationCanonicalPlan is the rewrite set for string-keyed delegation
// state. Planning finishes before any of it is written. Two stored spellings
// of one account are cleared, not treated as a failed upgrade: the old
// handler lets a participant write both keys.
type delegationCanonicalPlan struct {
	delegations        []types.PoCDelegation
	delegationOldKeys  []string
	delegationDeletes  []delegationKeyRewrite
	refusals           []delegationKeyRewrite
	refusalDeletes     []delegationKeyRewrite
	intents            []delegationKeyRewrite
	intentDeletes      []delegationKeyRewrite
	snapshot           *types.DelegationSnapshot
	bootstrap          *types.BootstrapDelegationSnapshot
	delegationsChanged int
	delegationsCleared int
	refusalsChanged    int
	refusalsCleared    int
	intentsChanged     int
	intentsCleared     int
	snapshotsChanged   int
}

type delegationKeyRewrite struct {
	model     string
	old       string
	canonical string
}

func (k Keeper) planDelegationCanonicalState(ctx context.Context) (delegationCanonicalPlan, error) {
	var plan delegationCanonicalPlan
	if err := k.planPoCDelegations(ctx, &plan); err != nil {
		return delegationCanonicalPlan{}, err
	}
	if err := k.planPoCRefusals(ctx, &plan); err != nil {
		return delegationCanonicalPlan{}, err
	}
	if err := k.planPoCDirectIntents(ctx, &plan); err != nil {
		return delegationCanonicalPlan{}, err
	}
	if err := k.planDelegationSnapshots(ctx, &plan); err != nil {
		return delegationCanonicalPlan{}, err
	}
	return plan, nil
}

func (k Keeper) applyDelegationCanonicalState(ctx context.Context, plan delegationCanonicalPlan) error {
	for _, rewrite := range plan.delegationDeletes {
		if err := k.DeletePoCDelegation(ctx, rewrite.model, rewrite.old); err != nil {
			return err
		}
	}
	for _, rewrite := range plan.refusalDeletes {
		if err := k.DeletePoCRefusal(ctx, rewrite.model, rewrite.old); err != nil {
			return err
		}
	}
	for _, rewrite := range plan.intentDeletes {
		if err := k.DeletePoCDirectIntent(ctx, rewrite.model, rewrite.old); err != nil {
			return err
		}
	}
	for i, value := range plan.delegations {
		old := plan.delegationOldKeys[i]
		if old != value.Delegator {
			if err := k.DeletePoCDelegation(ctx, value.ModelId, old); err != nil {
				return err
			}
		}
		if err := k.SetPoCDelegation(ctx, value); err != nil {
			return err
		}
	}
	for _, rewrite := range plan.refusals {
		if rewrite.old != rewrite.canonical {
			if err := k.DeletePoCRefusal(ctx, rewrite.model, rewrite.old); err != nil {
				return err
			}
		}
		if err := k.SetPoCRefusal(ctx, rewrite.model, rewrite.canonical); err != nil {
			return err
		}
	}
	for _, rewrite := range plan.intents {
		if rewrite.old != rewrite.canonical {
			if err := k.DeletePoCDirectIntent(ctx, rewrite.model, rewrite.old); err != nil {
				return err
			}
		}
		if err := k.SetPoCDirectIntent(ctx, rewrite.model, rewrite.canonical); err != nil {
			return err
		}
	}
	if plan.snapshot != nil {
		if err := k.SetDelegationSnapshot(ctx, *plan.snapshot); err != nil {
			return err
		}
	}
	if plan.bootstrap != nil {
		if err := k.SetBootstrapDelegationSnapshot(ctx, *plan.bootstrap); err != nil {
			return err
		}
	}
	if plan.delegationsChanged+plan.refusalsChanged+plan.intentsChanged+plan.snapshotsChanged+
		plan.delegationsCleared+plan.refusalsCleared+plan.intentsCleared > 0 {
		k.LogInfo("canonical address audit: delegation state rewritten", types.Upgrades,
			"delegationsRewritten", plan.delegationsChanged,
			"delegationsCleared", plan.delegationsCleared,
			"refusalsRewritten", plan.refusalsChanged,
			"refusalsCleared", plan.refusalsCleared,
			"intentsRewritten", plan.intentsChanged,
			"intentsCleared", plan.intentsCleared,
			"snapshotsRewritten", plan.snapshotsChanged,
		)
	}
	return nil
}

func (k Keeper) planPoCDelegations(ctx context.Context, plan *delegationCanonicalPlan) error {
	iter, err := k.PoCDelegations.Iterate(ctx, nil)
	if err != nil {
		return err
	}
	defer iter.Close()

	type observedDelegation struct {
		oldKey  string
		value   types.PoCDelegation
		rewrite bool
	}
	grouped := make(map[string][]observedDelegation)
	var identities []string
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return err
		}
		value, err := iter.Value()
		if err != nil {
			return err
		}
		value = restoredPoCDelegation(key, value)
		if value.ModelId != key.K1() {
			return sdkerrors.Wrap(types.ErrIllegalState, "poc delegation model does not match store key")
		}
		delegator, err := canonicalDelegationAccount(key.K2(), "poc delegation delegator")
		if err != nil {
			return err
		}
		valueDelegator, err := canonicalDelegationAccount(value.Delegator, "poc delegation delegator")
		if err != nil {
			return err
		}
		if delegator != valueDelegator {
			return sdkerrors.Wrap(types.ErrIllegalState, "poc delegation delegator does not match store key")
		}
		delegate, err := canonicalDelegationAccount(value.DelegateTo, "poc delegation delegate")
		if err != nil {
			return err
		}
		identity := delegationIdentity(key.K1(), delegator)
		if _, seen := grouped[identity]; !seen {
			identities = append(identities, identity)
		}
		rewrite := key.K2() != delegator || value.Delegator != delegator || value.DelegateTo != delegate
		value.Delegator = delegator
		value.DelegateTo = delegate
		grouped[identity] = append(grouped[identity], observedDelegation{oldKey: key.K2(), value: value, rewrite: rewrite})
	}
	for _, identity := range identities {
		rows := grouped[identity]
		if len(rows) > 1 {
			for _, row := range rows {
				plan.delegationDeletes = append(plan.delegationDeletes, delegationKeyRewrite{model: row.value.ModelId, old: row.oldKey})
			}
			plan.delegationsCleared++
			continue
		}
		if !rows[0].rewrite {
			continue
		}
		plan.delegations = append(plan.delegations, rows[0].value)
		plan.delegationOldKeys = append(plan.delegationOldKeys, rows[0].oldKey)
		plan.delegationsChanged++
	}
	return nil
}

func (k Keeper) planPoCRefusals(ctx context.Context, plan *delegationCanonicalPlan) error {
	rewrites, deletes, cleared, err := k.planDelegationKeySet(ctx, k.PoCRefusals, "poc refusal")
	if err != nil {
		return err
	}
	plan.refusals = rewrites
	plan.refusalDeletes = deletes
	plan.refusalsChanged = len(rewrites)
	plan.refusalsCleared = cleared
	return nil
}

func (k Keeper) planPoCDirectIntents(ctx context.Context, plan *delegationCanonicalPlan) error {
	rewrites, deletes, cleared, err := k.planDelegationKeySet(ctx, k.PoCDirectIntents, "poc direct intent")
	if err != nil {
		return err
	}
	plan.intents = rewrites
	plan.intentDeletes = deletes
	plan.intentsChanged = len(rewrites)
	plan.intentsCleared = cleared
	return nil
}

func (k Keeper) planDelegationKeySet(ctx context.Context, set collections.KeySet[collections.Pair[string, string]], label string) ([]delegationKeyRewrite, []delegationKeyRewrite, int, error) {
	iter, err := set.Iterate(ctx, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	defer iter.Close()

	type keyHit struct {
		model, stored, canonical string
	}
	grouped := make(map[string][]keyHit)
	var identities []string
	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return nil, nil, 0, err
		}
		canonical, err := canonicalDelegationAccount(key.K2(), label)
		if err != nil {
			return nil, nil, 0, err
		}
		identity := delegationIdentity(key.K1(), canonical)
		if _, seen := grouped[identity]; !seen {
			identities = append(identities, identity)
		}
		grouped[identity] = append(grouped[identity], keyHit{model: key.K1(), stored: key.K2(), canonical: canonical})
	}
	var rewrites, deletes []delegationKeyRewrite
	cleared := 0
	for _, identity := range identities {
		hits := grouped[identity]
		if len(hits) > 1 {
			for _, hit := range hits {
				deletes = append(deletes, delegationKeyRewrite{model: hit.model, old: hit.stored})
			}
			cleared++
			continue
		}
		if hits[0].stored == hits[0].canonical {
			continue
		}
		rewrites = append(rewrites, delegationKeyRewrite{model: hits[0].model, old: hits[0].stored, canonical: hits[0].canonical})
	}
	return rewrites, deletes, cleared, nil
}

func (k Keeper) planDelegationSnapshots(ctx context.Context, plan *delegationCanonicalPlan) error {
	snapshot, found := k.GetDelegationSnapshot(ctx)
	if found {
		changed, err := canonicalizeDelegationRows(&snapshot.Delegations, &snapshot.Refusals, nil)
		if err != nil {
			return err
		}
		if changed {
			plan.snapshot = &snapshot
			plan.snapshotsChanged++
		}
	}
	bootstrap, found := k.GetBootstrapDelegationSnapshot(ctx)
	if found {
		changed, err := canonicalizeDelegationRows(&bootstrap.Delegations, nil, &bootstrap.Intents)
		if err != nil {
			return err
		}
		if changed {
			plan.bootstrap = &bootstrap
			plan.snapshotsChanged++
		}
	}
	return nil
}

func canonicalizeDelegationRows(delegations *[]*types.PoCDelegation, refusals *[]*types.PoCRefusal, intents *[]*types.PoCDirectIntent) (bool, error) {
	changed := false
	if delegations != nil {
		next, rowChanged, err := compactDelegationSnapshotRows(*delegations)
		if err != nil {
			return false, err
		}
		if rowChanged {
			*delegations = next
			changed = true
		}
	}
	if refusals != nil {
		next, rowChanged, err := compactRefusalSnapshotRows(*refusals)
		if err != nil {
			return false, err
		}
		if rowChanged {
			*refusals = next
			changed = true
		}
	}
	if intents != nil {
		next, rowChanged, err := compactIntentSnapshotRows(*intents)
		if err != nil {
			return false, err
		}
		if rowChanged {
			*intents = next
			changed = true
		}
	}
	return changed, nil
}

func compactDelegationSnapshotRows(rows []*types.PoCDelegation) ([]*types.PoCDelegation, bool, error) {
	type prepared struct {
		row       *types.PoCDelegation
		identity  string
		rewritten bool
	}
	counts := make(map[string]int, len(rows))
	items := make([]prepared, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			return nil, false, sdkerrors.Wrap(types.ErrIllegalState, "nil poc delegation")
		}
		delegator, err := canonicalDelegationAccount(row.Delegator, "delegation snapshot delegator")
		if err != nil {
			return nil, false, err
		}
		delegate, err := canonicalDelegationAccount(row.DelegateTo, "delegation snapshot delegate")
		if err != nil {
			return nil, false, err
		}
		identity := delegationIdentity(row.ModelId, delegator)
		counts[identity]++
		rewritten := row.Delegator != delegator || row.DelegateTo != delegate
		row.Delegator = delegator
		row.DelegateTo = delegate
		items = append(items, prepared{row: row, identity: identity, rewritten: rewritten})
	}
	return compactPrepared(items, counts, func(item prepared) string { return item.identity }, func(item prepared) bool { return item.rewritten }, func(item prepared) *types.PoCDelegation { return item.row })
}

func compactRefusalSnapshotRows(rows []*types.PoCRefusal) ([]*types.PoCRefusal, bool, error) {
	type prepared struct {
		row       *types.PoCRefusal
		identity  string
		rewritten bool
	}
	counts := make(map[string]int, len(rows))
	items := make([]prepared, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			return nil, false, sdkerrors.Wrap(types.ErrIllegalState, "nil poc refusal")
		}
		participant, err := canonicalDelegationAccount(row.Participant, "delegation snapshot refusal")
		if err != nil {
			return nil, false, err
		}
		identity := delegationIdentity(row.ModelId, participant)
		counts[identity]++
		rewritten := row.Participant != participant
		row.Participant = participant
		items = append(items, prepared{row: row, identity: identity, rewritten: rewritten})
	}
	return compactPrepared(items, counts, func(item prepared) string { return item.identity }, func(item prepared) bool { return item.rewritten }, func(item prepared) *types.PoCRefusal { return item.row })
}

func compactIntentSnapshotRows(rows []*types.PoCDirectIntent) ([]*types.PoCDirectIntent, bool, error) {
	type prepared struct {
		row       *types.PoCDirectIntent
		identity  string
		rewritten bool
	}
	counts := make(map[string]int, len(rows))
	items := make([]prepared, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			return nil, false, sdkerrors.Wrap(types.ErrIllegalState, "nil poc direct intent")
		}
		participant, err := canonicalDelegationAccount(row.Participant, "bootstrap delegation snapshot intent")
		if err != nil {
			return nil, false, err
		}
		identity := delegationIdentity(row.ModelId, participant)
		counts[identity]++
		rewritten := row.Participant != participant
		row.Participant = participant
		items = append(items, prepared{row: row, identity: identity, rewritten: rewritten})
	}
	return compactPrepared(items, counts, func(item prepared) string { return item.identity }, func(item prepared) bool { return item.rewritten }, func(item prepared) *types.PoCDirectIntent { return item.row })
}

func compactPrepared[T any, P any](items []P, counts map[string]int, identity func(P) string, rewritten func(P) bool, row func(P) T) ([]T, bool, error) {
	changed := false
	out := make([]T, 0, len(items))
	for _, item := range items {
		if counts[identity(item)] > 1 {
			changed = true
			continue
		}
		if rewritten(item) {
			changed = true
		}
		out = append(out, row(item))
	}
	return out, changed, nil
}

func canonicalDelegationAccount(bech32, label string) (string, error) {
	addr, err := sdk.AccAddressFromBech32(bech32)
	if err != nil {
		return "", sdkerrors.Wrapf(types.ErrIllegalState, "%s is not decodable", label)
	}
	return addr.String(), nil
}

func delegationIdentity(model, account string) string {
	return model + "\x00" + account
}
