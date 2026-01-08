package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestMsgAddParticipantsToAllowList(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ms := keeper.NewMsgServerImpl(k)
	wctx := sdk.UnwrapSDKContext(ctx)

	addr1 := "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2"
	addr2 := "gonka1v5ggga7lslfg2e57m9anxud40v2s4t9dw8yj68"

	// unauthorized authority should fail
	_, err := ms.AddParticipantsToAllowList(wctx, &types.MsgAddParticipantsToAllowList{
		Authority: "invalid",
		Addresses: []string{addr1},
	})
	require.Error(t, err)

	// valid authority should add addresses
	_, err = ms.AddParticipantsToAllowList(wctx, &types.MsgAddParticipantsToAllowList{
		Authority: k.GetAuthority(),
		Addresses: []string{addr1, addr2},
	})
	require.NoError(t, err)

	acc1, _ := sdk.AccAddressFromBech32(addr1)
	acc2, _ := sdk.AccAddressFromBech32(addr2)

	ok, err := k.ParticipantAllowListSet.Has(wctx, acc1)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = k.ParticipantAllowListSet.Has(wctx, acc2)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestMsgRemoveParticipantsFromAllowList(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	ms := keeper.NewMsgServerImpl(k)
	wctx := sdk.UnwrapSDKContext(ctx)

	addr := "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2"
	acc, _ := sdk.AccAddressFromBech32(addr)

	// add first
	require.NoError(t, k.ParticipantAllowListSet.Set(wctx, acc))

	// unauthorized authority should fail
	_, err := ms.RemoveParticipantsFromAllowList(wctx, &types.MsgRemoveParticipantsFromAllowList{
		Authority: "invalid",
		Addresses: []string{addr},
	})
	require.Error(t, err)

	// valid authority should remove
	_, err = ms.RemoveParticipantsFromAllowList(wctx, &types.MsgRemoveParticipantsFromAllowList{
		Authority: k.GetAuthority(),
		Addresses: []string{addr},
	})
	require.NoError(t, err)

	ok, err := k.ParticipantAllowListSet.Has(wctx, acc)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestQueryParticipantAllowList(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	addr1 := "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2"
	addr2 := "gonka1v5ggga7lslfg2e57m9anxud40v2s4t9dw8yj68"

	acc1, _ := sdk.AccAddressFromBech32(addr1)
	acc2, _ := sdk.AccAddressFromBech32(addr2)

	require.NoError(t, k.ParticipantAllowListSet.Set(wctx, acc1))
	require.NoError(t, k.ParticipantAllowListSet.Set(wctx, acc2))

	resp, err := k.ParticipantAllowList(wctx, &types.QueryParticipantAllowListRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Addresses, 2)
}

func TestIsParticipantAllowed_AllowlistDisabled(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	// default: allowlist disabled
	params := k.GetParams(wctx)
	require.False(t, params.ParticipantAccessParams.UseParticipantAllowlist)

	// any address should be allowed when disabled
	addr := "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2"
	require.True(t, k.IsParticipantAllowed(wctx, 100, addr))
}

func TestIsParticipantAllowed_AllowlistEnabled(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	addr := "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2"
	acc, _ := sdk.AccAddressFromBech32(addr)

	// enable allowlist
	params := k.GetParams(wctx)
	params.ParticipantAccessParams.UseParticipantAllowlist = true
	require.NoError(t, k.SetParams(wctx, params))

	// not in allowlist -> not allowed
	require.False(t, k.IsParticipantAllowed(wctx, 100, addr))

	// add to allowlist
	require.NoError(t, k.ParticipantAllowListSet.Set(wctx, acc))

	// now allowed
	require.True(t, k.IsParticipantAllowed(wctx, 100, addr))
}

func TestIsParticipantAllowed_UntilBlockHeight(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	addr := "gonka1hgt9lxxxwpsnc3yn2nheqqy9a8vlcjwvgzpve2"

	// enable allowlist with cutoff at height 200
	params := k.GetParams(wctx)
	params.ParticipantAccessParams.UseParticipantAllowlist = true
	params.ParticipantAccessParams.ParticipantAllowlistUntilBlockHeight = 200
	require.NoError(t, k.SetParams(wctx, params))

	// at height 100 (< 200): allowlist active, not in list -> not allowed
	require.False(t, k.IsParticipantAllowed(wctx, 100, addr))

	// at height 200 (>= 200): allowlist disabled -> allowed
	require.True(t, k.IsParticipantAllowed(wctx, 200, addr))

	// at height 300 (> 200): allowlist disabled -> allowed
	require.True(t, k.IsParticipantAllowed(wctx, 300, addr))
}

