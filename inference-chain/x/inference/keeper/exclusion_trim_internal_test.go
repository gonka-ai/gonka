package keeper

import (
	"strings"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
)

func TestStoredExclusion_KeepsNonCanonicalAddress(t *testing.T) {
	upper := strings.ToUpper(testutil.Bech32Addr(3))
	acc, err := sdk.AccAddressFromBech32(upper)
	require.NoError(t, err)
	e := types.ExcludedParticipant{Address: upper, EpochIndex: 4, Reason: "r"}
	stored := storedExclusion(acc, e)
	require.Equal(t, e, stored)
	require.Equal(t, e, restoredExclusion(collections.Join(uint64(4), acc), stored))
}
