package types_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestResolvePassCount(t *testing.T) {
	sampled := types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED
	derived := types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED

	got, err := types.ResolvePassCount(nil, types.DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED)
	require.NoError(t, err)
	require.Equal(t, derived, got, "new name with omitted pass_count is DERIVED")

	got, err = types.ResolvePassCount(&sampled, types.DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED)
	require.NoError(t, err)
	require.Equal(t, sampled, got, "omitted keeps the stored policy")

	got, err = types.ResolvePassCount(&derived, types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED)
	require.NoError(t, err)
	require.Equal(t, sampled, got, "explicit JSON overwrites the stored policy")

	got, err = types.ResolvePassCount(&sampled, types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED)
	require.NoError(t, err)
	require.Equal(t, derived, got)

	got, err = types.ResolvePassCount(nil, types.DevshardPassCount(99))
	require.NoError(t, err)
	require.Equal(t, derived, got, "unknown requested value is omitted → DERIVED for a new name")

	got, err = types.ResolvePassCount(&sampled, types.DevshardPassCount(99))
	require.NoError(t, err)
	require.Equal(t, sampled, got, "unknown requested value keeps the stored policy")

	got, err = types.ResolvePassCount(&derived, types.DevshardPassCount(99))
	require.NoError(t, err)
	require.Equal(t, derived, got, "mistype on an existing DERIVED name keeps DERIVED")

	got, err = types.ResolvePassCount(&derived, types.DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED)
	require.NoError(t, err)
	require.Equal(t, derived, got, "omitted on an existing DERIVED name keeps DERIVED")
}

func TestDevshardPassCountDerived(t *testing.T) {
	require.False(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED.Derived())
	require.False(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED.Derived())
	require.True(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED.Derived())
}

func TestDevshardVersionPolicyRejectsUnspecified(t *testing.T) {
	p := types.DevshardVersionPolicy{Name: "v1"}
	require.ErrorContains(t, p.Validate(), "invalid stored pass_count")
	p.PassCount = types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED
	require.NoError(t, p.Validate())
}
