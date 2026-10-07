package state

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestRootDivergenceErrorUnwrapsToMismatch(t *testing.T) {
	err := &RootDivergenceError{DiffRoot: []byte{0xaa}, Computed: []byte{0xbb}}
	require.ErrorIs(t, err, types.ErrPostStateRootMismatch)
	require.True(t, IsPostStateRootMismatchError(err))
	require.Contains(t, err.Error(), "post_state_root does not match computed state root")
}

func TestDiffRootInputsNamesTheHeightSyncField(t *testing.T) {
	local := RootInputs{ForcedStart: 0, HeightSyncHash: "aa", ComputedRoot: "11"}
	host := RootInputs{ForcedStart: 9, HeightSyncHash: "bb", ComputedRoot: "22"}
	differ := DiffRootInputs(local, host)
	require.Contains(t, differ, "hs_forced_start local=0 host=9")
	require.Contains(t, differ, "height_sync_hash local=aa host=bb")
	require.Contains(t, differ, "computed_root local=11 host=22")
}

func TestParseHostRootInputs(t *testing.T) {
	body := `{"message":"validate diff nonce 1: post_state_root does not match computed state root: diff aa, computed bb","host_state":{"nonce":1,"latest_nonce":1,"hs_forced_start":4,"computed_root":"bb"}}`
	got, ok := ParseHostRootInputs(body)
	require.True(t, ok)
	require.Equal(t, uint64(1), got.Nonce)
	require.Equal(t, uint64(4), got.ForcedStart)
	require.Equal(t, "bb", got.ComputedRoot)

	_, ok = ParseHostRootInputs(`{"message":"other"}`)
	require.False(t, ok)
	_, ok = ParseHostRootInputs("not json")
	require.False(t, ok)
}

func TestAsRootDivergenceFindsWrappedError(t *testing.T) {
	inner := &RootDivergenceError{Inputs: RootInputs{Nonce: 3}}
	wrapped := errors.Join(errors.New("validate diff nonce 3"), inner)
	got := AsRootDivergence(wrapped)
	require.NotNil(t, got)
	require.Equal(t, uint64(3), got.Inputs.Nonce)
}
