package types_test

// With fee groups enabled, a message type that has no compiled group must not
// be a zero-fee path: it pays the highest enabled group price.

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	blstypes "github.com/productscience/inference/x/bls/types"
	"github.com/productscience/inference/x/inference/types"
)

func TestEnabledPayingPrice_UngroupedPaysMaxEnabledPrice(t *testing.T) {
	fp := types.DefaultFeeParams()
	epoch := fp.GroupByName(types.FeeGroupEpoch)
	require.NotNil(t, epoch)
	epoch.MinGasPrice = 10
	fp.Groups = append(fp.Groups, &types.FeeGroup{Name: types.FeeGroupCosmos, MinGasPrice: 7})
	fp.EnabledFeeGroups = []string{types.FeeGroupEpoch, types.FeeGroupCosmos}
	require.NoError(t, fp.Validate())

	for _, m := range []sdk.Msg{
		&blstypes.MsgRequestThresholdSignature{},
		&types.MsgUpdateParams{},
		&types.MsgSetClaimRecipients{},
		&types.MsgSubmitUnitOfComputePriceProposal{},
	} {
		require.Equal(t, "", types.FeeGroupOf(m))
		require.Equal(t, uint64(10), fp.EnabledPayingPrice([]sdk.Msg{m}, types.IsNetworkDuty), sdk.MsgTypeURL(m))
	}
	// Duties stay exempt, grouped types keep their own price, nothing enabled = free as before.
	require.Equal(t, uint64(0), fp.EnabledPayingPrice([]sdk.Msg{&types.MsgSubmitSeed{}}, types.IsNetworkDuty))
	require.Equal(t, uint64(7), fp.EnabledPayingPrice([]sdk.Msg{&banktypes.MsgSend{}}, types.IsNetworkDuty))
	fp.EnabledFeeGroups = nil
	require.Equal(t, uint64(0), fp.EnabledPayingPrice([]sdk.Msg{&blstypes.MsgRequestThresholdSignature{}}, types.IsNetworkDuty))
}
