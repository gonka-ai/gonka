package types_test

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestBridgeReissueParamsDefaultDisabledAndValidated(t *testing.T) {
	require.Nil(t, types.DefaultParams().BridgeReissueParams)
	valid := types.BridgeReissueTarget{ChainId: "ethereum", ContractAddress: "0x1111111111111111111111111111111111111111"}
	for _, tc := range []struct {
		name    string
		targets []types.BridgeReissueTarget
		invalid bool
	}{
		{"empty", nil, false}, {"enabled", []types.BridgeReissueTarget{valid}, false},
		{"duplicate", []types.BridgeReissueTarget{valid, valid}, true},
		{"empty chain", []types.BridgeReissueTarget{{ContractAddress: valid.ContractAddress}}, true},
		{"noncanonical chain", []types.BridgeReissueTarget{{ChainId: "Ethereum", ContractAddress: valid.ContractAddress}}, true},
		{"invalid hex", []types.BridgeReissueTarget{{ChainId: valid.ChainId, ContractAddress: "0x" + strings.Repeat("gg", 20)}}, true},
		{"zero address", []types.BridgeReissueTarget{{ChainId: valid.ChainId, ContractAddress: "0x" + strings.Repeat("00", 20)}}, true},
		{"short address", []types.BridgeReissueTarget{{ChainId: valid.ChainId, ContractAddress: "0x11"}}, true},
		{"too many", make([]types.BridgeReissueTarget, types.MaxBridgeReissueTargets+1), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := types.DefaultParams()
			params.BridgeReissueParams = &types.BridgeReissueParams{AllowedTargets: tc.targets}
			if tc.invalid {
				require.Error(t, params.Validate())
				return
			}
			require.NoError(t, params.Validate())
			encoded, err := params.Marshal()
			require.NoError(t, err)
			var restored types.Params
			require.NoError(t, restored.Unmarshal(encoded))
			require.Equal(t, params.BridgeReissueParams, restored.BridgeReissueParams)
		})
	}
}

func TestReissueBridgeSignatureMessageValidationAndFees(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	msg := &types.MsgReissueBridgeSignature{Creator: testutil.Creator, RequestId: "req_123_transactionbytes"}
	require.NoError(t, msg.ValidateBasic())
	require.Equal(t, types.FeeGroupBridge, types.FeeGroupOf(msg))
	require.False(t, types.IsNetworkDuty(msg), "requesting another signature is not a validator duty")
	require.Equal(t, []string{testutil.Creator}, msg.GetSignersStrings())
	msg.RequestId = ""
	require.Error(t, msg.ValidateBasic())
	msg.RequestId = "req_123"
	msg.Creator = "invalid"
	require.Error(t, msg.ValidateBasic())
}
