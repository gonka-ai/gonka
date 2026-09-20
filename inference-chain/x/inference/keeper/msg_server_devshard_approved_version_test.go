package keeper_test

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func validApprovedVersion(name string) types.DevshardApprovedVersion {
	return types.DevshardApprovedVersion{
		Name:   name,
		Binary: "https://example.com/" + name + ".zip",
		Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func TestPutDevshardApprovedVersion(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	_, err := ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: testutil.Creator,
		Version:   validApprovedVersion("v1"),
	})
	require.ErrorIs(t, err, types.ErrInvalidSigner)

	_, err = ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   validApprovedVersion("v1"),
	})
	require.NoError(t, err)

	got, found := k.GetApprovedVersion(wctx, "v1")
	require.True(t, found)
	require.Equal(t, "v1", got.Name)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, got.PassCount)
	pol, found, err := k.GetVersionPolicy(wctx, "v1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, pol.PassCount)

	updated := validApprovedVersion("v1")
	updated.Binary = "https://example.com/v1-new.zip"
	updated.Sha256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	_, err = ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   updated,
	})
	require.NoError(t, err)
	got, found = k.GetApprovedVersion(wctx, "v1")
	require.True(t, found)
	require.Equal(t, updated.Sha256, got.Sha256)

	versions, err := k.GetApprovedVersions(wctx)
	require.NoError(t, err)
	require.Len(t, versions, 1)
}

func TestDeleteDevshardApprovedVersion(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	_, err := ms.DeleteDevshardApprovedVersion(wctx, &types.MsgDeleteDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Name:      "missing",
	})
	require.ErrorIs(t, err, types.ErrApprovedVersionNotFound)

	_, err = ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   validApprovedVersion("v2"),
	})
	require.NoError(t, err)

	_, err = ms.DeleteDevshardApprovedVersion(wctx, &types.MsgDeleteDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Name:      "v2",
	})
	require.NoError(t, err)
	_, found := k.GetApprovedVersion(wctx, "v2")
	require.False(t, found)
	pol, found, err := k.GetVersionPolicy(wctx, "v2")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, pol.PassCount)
}

func TestPutDevshardApprovedVersion_RejectsOversizeAndCap(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	tooLong := validApprovedVersion(strings.Repeat("n", types.MaxDevshardApprovedVersionNameLen+1))
	require.Error(t, tooLong.Validate())

	longBinary := validApprovedVersion("v-url")
	longBinary.Binary = strings.Repeat("u", types.MaxDevshardApprovedVersionBinaryLen+1)
	require.Error(t, longBinary.Validate())

	for i := 0; i < types.MaxDevshardApprovedVersions; i++ {
		v := validApprovedVersion("slot-" + string(rune('a'+i)))
		_, err := ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
			Authority: k.GetAuthority(),
			Version:   v,
		})
		require.NoError(t, err)
	}
	_, err := ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   validApprovedVersion("overflow"),
	})
	require.ErrorIs(t, err, types.ErrApprovedVersionsLimit)

	override := validApprovedVersion("slot-a")
	override.Sha256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err = ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   override,
	})
	require.NoError(t, err)
}

func TestUpdateParams_RejectsDeprecatedApprovedVersions(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)
	params := types.DefaultParams()
	params.DevshardEscrowParams.ApprovedVersions = []*types.DevshardApprovedVersion{
		{Name: "v1", Binary: "https://x", Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}
	_, err := ms.UpdateParams(wctx, &types.MsgUpdateParams{
		Authority: k.GetAuthority(),
		Params:    params,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "deprecated")
}

func TestDevshardApprovedVersionsQuery(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)
	_, err := ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   validApprovedVersion("v2"),
	})
	require.NoError(t, err)
	_, err = ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   validApprovedVersion("v1"),
	})
	require.NoError(t, err)

	resp, err := k.DevshardApprovedVersions(wctx, &types.QueryDevshardApprovedVersionsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Versions, 2)
	require.Equal(t, "v1", resp.Versions[0].Name)
	require.Equal(t, "v2", resp.Versions[1].Name)
}

func TestPutDevshardApprovedVersion_PassCountKeepAndOverwrite(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// Omitted pass_count on a new name records SAMPLED.
	_, err := ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   validApprovedVersion("v-policy"),
	})
	require.NoError(t, err)
	got, found := k.GetApprovedVersion(wctx, "v-policy")
	require.True(t, found)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, got.PassCount)

	// Explicit DERIVED overwrites the stored policy.
	derived := validApprovedVersion("v-policy")
	derived.PassCount = types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED
	_, err = ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   derived,
	})
	require.NoError(t, err)
	got, found = k.GetApprovedVersion(wctx, "v-policy")
	require.True(t, found)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, got.PassCount)
	pol, found, err := k.GetVersionPolicy(wctx, "v-policy")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, pol.PassCount)

	// Omitted pass_count on an update keeps the stored DERIVED value.
	keep := validApprovedVersion("v-policy")
	keep.Binary = "https://example.com/v-policy-new.zip"
	keep.Sha256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	_, err = ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   keep,
	})
	require.NoError(t, err)
	got, found = k.GetApprovedVersion(wctx, "v-policy")
	require.True(t, found)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, got.PassCount)
	require.Equal(t, keep.Sha256, got.Sha256)

	typo := validApprovedVersion("v-policy")
	typo.PassCount = types.DevshardPassCount(99)
	_, err = ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   typo,
	})
	require.NoError(t, err)
	got, found = k.GetApprovedVersion(wctx, "v-policy")
	require.True(t, found)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, got.PassCount, "mistype on an existing name keeps the stored policy")

	// Policy survives delete; re-approval with omitted pass_count keeps DERIVED.
	_, err = ms.DeleteDevshardApprovedVersion(wctx, &types.MsgDeleteDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Name:      "v-policy",
	})
	require.NoError(t, err)
	_, err = ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   validApprovedVersion("v-policy"),
	})
	require.NoError(t, err)
	got, found = k.GetApprovedVersion(wctx, "v-policy")
	require.True(t, found)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, got.PassCount)

	mistyped := validApprovedVersion("v-new-typo")
	mistyped.PassCount = types.DevshardPassCount(99)
	_, err = ms.PutDevshardApprovedVersion(wctx, &types.MsgPutDevshardApprovedVersion{
		Authority: k.GetAuthority(),
		Version:   mistyped,
	})
	require.NoError(t, err)
	got, found = k.GetApprovedVersion(wctx, "v-new-typo")
	require.True(t, found)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, got.PassCount, "mistyped pass_count on a new name is SAMPLED")
}

func TestPassCountFor_MissingIsDerived(t *testing.T) {
	k, _, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	_, found, err := k.GetVersionPolicy(wctx, "never-seen")
	require.NoError(t, err)
	require.False(t, found)
	got, err := k.PassCountFor(wctx, "never-seen")
	require.NoError(t, err)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED, got)
}
