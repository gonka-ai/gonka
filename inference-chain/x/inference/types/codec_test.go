package types

import (
	"testing"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
)

func TestRemovedMessageTypeURLsDoNotUnpack(t *testing.T) {
	registry := codectypes.NewInterfaceRegistry()
	RegisterInterfaces(registry)

	for _, typeURL := range []string{
		"/inference.inference.MsgStartInference",
		"/inference.inference.MsgFinishInference",
		"/inference.inference.MsgValidation",
		"/inference.inference.MsgInvalidateInference",
		"/inference.inference.MsgRevalidateInference",
	} {
		t.Run(typeURL, func(t *testing.T) {
			var msg sdk.Msg
			err := registry.UnpackAny(&codectypes.Any{TypeUrl: typeURL}, &msg)
			require.Error(t, err)
		})
	}

	claimAny, err := codectypes.NewAnyWithValue(&MsgClaimRewards{})
	require.NoError(t, err)
	var claim sdk.Msg
	require.NoError(t, registry.UnpackAny(claimAny, &claim))
	require.IsType(t, &MsgClaimRewards{}, claim)
}
