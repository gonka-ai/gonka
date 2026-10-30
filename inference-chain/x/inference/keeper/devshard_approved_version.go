package keeper

import (
	"context"
	"errors"
	"sort"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetApprovedVersion(ctx context.Context, v types.DevshardApprovedVersion) error {
	return k.DevshardApprovedVersionsMap.Set(ctx, v.Name, v)
}

func (k Keeper) DeleteApprovedVersion(ctx context.Context, name string) error {
	return k.DevshardApprovedVersionsMap.Remove(ctx, name)
}

func (k Keeper) GetApprovedVersion(ctx context.Context, name string) (types.DevshardApprovedVersion, bool) {
	v, err := k.DevshardApprovedVersionsMap.Get(ctx, name)
	if err != nil {
		return types.DevshardApprovedVersion{}, false
	}
	return v, true
}

func (k Keeper) HasApprovedVersion(ctx context.Context, name string) bool {
	ok, err := k.DevshardApprovedVersionsMap.Has(ctx, name)
	return err == nil && ok
}

func (k Keeper) ApprovedVersionCount(ctx context.Context) (int, error) {
	versions, err := k.GetApprovedVersions(ctx)
	if err != nil {
		return 0, err
	}
	return len(versions), nil
}

func (k Keeper) GetApprovedVersions(ctx context.Context) ([]types.DevshardApprovedVersion, error) {
	iter, err := k.DevshardApprovedVersionsMap.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	vals, err := iter.Values()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(vals, func(i, j int) bool {
		return vals[i].Name < vals[j].Name
	})
	return vals, nil
}

func (k Keeper) SetVersionPolicy(ctx context.Context, p types.DevshardVersionPolicy) error {
	return k.DevshardVersionPoliciesMap.Set(ctx, p.Name, p)
}

func (k Keeper) GetVersionPolicy(ctx context.Context, name string) (types.DevshardVersionPolicy, bool, error) {
	p, err := k.DevshardVersionPoliciesMap.Get(ctx, name)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.DevshardVersionPolicy{}, false, nil
		}
		return types.DevshardVersionPolicy{}, false, err
	}
	return p, true, nil
}

func (k Keeper) GetVersionPolicies(ctx context.Context) ([]types.DevshardVersionPolicy, error) {
	iter, err := k.DevshardVersionPoliciesMap.Iterate(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	vals, err := iter.Values()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(vals, func(i, j int) bool {
		return vals[i].Name < vals[j].Name
	})
	return vals, nil
}

// PassCountFor returns the recorded policy for version. Unknown names, including
// the empty allowlist used in tests/dev, score as DERIVED so preexisting
// settlements keep assigned-missed-invalid. Store or decode failures
// are returned; they must not be treated as a missing policy.
func (k Keeper) PassCountFor(ctx context.Context, version string) (types.DevshardPassCount, error) {
	p, ok, err := k.GetVersionPolicy(ctx, version)
	if err != nil {
		return 0, err
	}
	if ok {
		return p.PassCount, nil
	}
	return types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, nil
}

// RecordVersionPassCount applies ResolvePassCount and writes the result.
// Omitted pass_count keeps a stored policy. An approved name with no policy
// row yet is a pre-pass_count binary: omitted keeps the scoring mode already
// on that row, or DERIVED when the row has none. A new name becomes SAMPLED.
// An explicit SAMPLED or DERIVED overwrites. Returns the value to stamp on
// the approved-version row so it never disagrees with the policy store.
func (k Keeper) RecordVersionPassCount(ctx context.Context, name string, requested types.DevshardPassCount) (types.DevshardPassCount, error) {
	p, ok, err := k.GetVersionPolicy(ctx, name)
	if err != nil {
		return 0, err
	}
	var existing *types.DevshardPassCount
	if ok {
		c := p.PassCount
		existing = &c
	} else {
		approved, err := k.DevshardApprovedVersionsMap.Get(ctx, name)
		if err != nil && !errors.Is(err, collections.ErrNotFound) {
			return 0, err
		}
		if err == nil {
			// No policy row. Consult the approved-version row so a binary/sha
			// roll that omits pass_count cannot record SAMPLED for a legacy name.
			c := passCountForUnpoliciedApproved(approved.PassCount)
			existing = &c
		}
	}
	resolved, err := types.ResolvePassCount(existing, requested)
	if err != nil {
		return 0, err
	}
	// Skip the write only when the policy row already stores this value.
	// A synthesized DERIVED from an unpolicied approved name must still be recorded.
	if ok && p.PassCount == resolved {
		return resolved, nil
	}
	if err := k.SetVersionPolicy(ctx, types.DevshardVersionPolicy{Name: name, PassCount: resolved}); err != nil {
		return 0, err
	}
	return resolved, nil
}

// passCountForUnpoliciedApproved is the scoring mode of an approved name that
// has no policy row. Legacy rows store UNSPECIFIED; that is DERIVED. An
// explicit SAMPLED or DERIVED already on the row is kept.
func passCountForUnpoliciedApproved(c types.DevshardPassCount) types.DevshardPassCount {
	switch c {
	case types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED,
		types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED:
		return c
	default:
		return types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED
	}
}
