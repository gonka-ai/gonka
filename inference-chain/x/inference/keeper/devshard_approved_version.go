package keeper

import (
	"context"
	"sort"

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

// settlementApprovedVersions returns the allowlist entries the version check needs:
// just the settlement's own version when listed, the whole list otherwise.
func (k Keeper) settlementApprovedVersions(ctx context.Context, version string) ([]*types.DevshardApprovedVersion, error) {
	listed, err := k.DevshardApprovedVersionsMap.Has(ctx, version)
	if err != nil {
		return nil, err
	}
	if listed {
		return []*types.DevshardApprovedVersion{{Name: version}}, nil
	}
	stored, err := k.GetApprovedVersions(ctx)
	if err != nil {
		return nil, err
	}
	approved := make([]*types.DevshardApprovedVersion, len(stored))
	for i := range stored {
		approved[i] = &stored[i]
	}
	return approved, nil
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
