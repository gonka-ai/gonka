package types

import (
	"testing"

	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgRequestBridgeWithdrawal_ValidateBasic_DestinationAddress(t *testing.T) {
	base := MsgRequestBridgeWithdrawal{
		Creator:                  sample.AccAddress(),
		UserAddress:              sample.AccAddress(),
		Amount:                   "100",
		DestinationBridgeAddress: "0x1234567890123456789012345678901234567890",
	}

	tests := []struct {
		name               string
		destinationAddress string
		wantErr            bool
	}{
		{
			name:               "rejects short destination",
			destinationAddress: "0x12345",
			wantErr:            true,
		},
		{
			name:               "rejects invalid hex destination",
			destinationAddress: "0xGGGG567890123456789012345678901234567890",
			wantErr:            true,
		},
		{
			name:               "rejects destination without prefix",
			destinationAddress: "1234567890123456789012345678901234567890",
			wantErr:            true,
		},
		{
			name:               "accepts valid destination",
			destinationAddress: "0x3333333333333333333333333333333333333333",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := base
			msg.DestinationAddress = tt.destinationAddress

			err := msg.ValidateBasic()
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
