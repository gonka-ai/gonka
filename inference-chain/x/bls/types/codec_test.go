package types_test

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

func TestRemovedThresholdSignatureRequestRejectedByTxDecoder(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	types.RegisterInterfaces(registry)
	authz.RegisterInterfaces(registry)
	decode := authtx.DefaultTxDecoder(codec.NewProtoCodec(registry))
	request := &types.MsgRequestThresholdSignature{
		Creator: sdk.AccAddress(make([]byte, 20)).String(), CurrentEpochId: 1,
		ChainId: make([]byte, 32), RequestId: make([]byte, 32), Data: [][]byte{make([]byte, 32)},
	}
	require.NoError(t, request.ValidateBasic())
	removed, err := codectypes.NewAnyWithValue(request)
	require.NoError(t, err)
	active, err := codectypes.NewAnyWithValue(&types.MsgSubmitPartialSignature{})
	require.NoError(t, err)
	wrap := func(inner *codectypes.Any) *codectypes.Any {
		msg, err := codectypes.NewAnyWithValue(&authz.MsgExec{Grantee: request.Creator, Msgs: []*codectypes.Any{inner}})
		require.NoError(t, err)
		return msg
	}
	for _, tc := range []struct {
		name     string
		msg      *codectypes.Any
		rejected bool
	}{
		{"removed direct", removed, true},
		{"removed authz", wrap(removed), true},
		{"removed nested authz", wrap(wrap(removed)), true},
		{"active direct", active, false},
		{"active authz", wrap(active), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := (&txtypes.TxBody{Messages: []*codectypes.Any{tc.msg}}).Marshal()
			require.NoError(t, err)
			authInfo, err := (&txtypes.AuthInfo{}).Marshal()
			require.NoError(t, err)
			raw, err := (&txtypes.TxRaw{BodyBytes: body, AuthInfoBytes: authInfo}).Marshal()
			require.NoError(t, err)
			_, err = decode(raw)
			if tc.rejected {
				require.ErrorContains(t, err, removed.TypeUrl)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
