package types_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/streamvesting/types"
)

func init() {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
}

func validAddr(seed byte) string {
	b := make([]byte, 20)
	for i := range b {
		b[i] = seed
	}
	return sdk.AccAddress(b).String()
}

func TestMsgTransferWithVesting_ValidateBasic(t *testing.T) {
	sender := validAddr(1)
	recipient := validAddr(2)

	tests := []struct {
		name      string
		msg       types.MsgTransferWithVesting
		expectErr string
	}{
		{
			name: "valid gonka at minimum",
			msg: types.MsgTransferWithVesting{
				Sender:    sender,
				Recipient: recipient,
				Amount:    sdk.NewCoins(sdk.NewCoin("gonka", math.NewInt(types.MinTransferGonka))),
			},
		},
		{
			name: "valid ngonka at minimum",
			msg: types.MsgTransferWithVesting{
				Sender:    sender,
				Recipient: recipient,
				Amount:    sdk.NewCoins(sdk.NewCoin("ngonka", math.NewInt(types.MinTransferNgonka))),
			},
		},
		{
			name: "valid gonka above minimum",
			msg: types.MsgTransferWithVesting{
				Sender:    sender,
				Recipient: recipient,
				Amount:    sdk.NewCoins(sdk.NewCoin("gonka", math.NewInt(1000))),
			},
		},
		{
			name: "valid multi-denom",
			msg: types.MsgTransferWithVesting{
				Sender:    sender,
				Recipient: recipient,
				Amount: sdk.NewCoins(
					sdk.NewCoin("gonka", math.NewInt(50)),
					sdk.NewCoin("ngonka", math.NewInt(types.MinTransferNgonka)),
				),
			},
		},
		{
			name: "valid at max vesting epochs",
			msg: types.MsgTransferWithVesting{
				Sender:        sender,
				Recipient:     recipient,
				Amount:        sdk.NewCoins(sdk.NewCoin("gonka", math.NewInt(100))),
				VestingEpochs: types.MaxVestingEpochs,
			},
		},
		{
			name: "invalid sender address",
			msg: types.MsgTransferWithVesting{
				Sender:    "bad",
				Recipient: recipient,
				Amount:    sdk.NewCoins(sdk.NewCoin("gonka", math.NewInt(100))),
			},
			expectErr: "invalid sender address",
		},
		{
			name: "invalid recipient address",
			msg: types.MsgTransferWithVesting{
				Sender:    sender,
				Recipient: "bad",
				Amount:    sdk.NewCoins(sdk.NewCoin("gonka", math.NewInt(100))),
			},
			expectErr: "invalid recipient address",
		},
		{
			name: "zero amount",
			msg: types.MsgTransferWithVesting{
				Sender:    sender,
				Recipient: recipient,
				Amount:    sdk.NewCoins(),
			},
			expectErr: "amount cannot be zero",
		},
		{
			name: "dust gonka below minimum",
			msg: types.MsgTransferWithVesting{
				Sender:    sender,
				Recipient: recipient,
				Amount:    sdk.NewCoins(sdk.NewCoin("gonka", math.NewInt(9))),
			},
			expectErr: "below minimum of 10 gonka",
		},
		{
			name: "dust ngonka below minimum",
			msg: types.MsgTransferWithVesting{
				Sender:    sender,
				Recipient: recipient,
				Amount:    sdk.NewCoins(sdk.NewCoin("ngonka", math.NewInt(types.MinTransferNgonka-1))),
			},
			expectErr: "below minimum",
		},
		{
			name: "unsupported denom",
			msg: types.MsgTransferWithVesting{
				Sender:    sender,
				Recipient: recipient,
				Amount:    sdk.NewCoins(sdk.NewCoin("stake", math.NewInt(1000))),
			},
			expectErr: "unsupported denomination",
		},
		{
			name: "unsupported denom ugonka",
			msg: types.MsgTransferWithVesting{
				Sender:    sender,
				Recipient: recipient,
				Amount:    sdk.NewCoins(sdk.NewCoin("ugonka", math.NewInt(1000))),
			},
			expectErr: "unsupported denomination",
		},
		{
			name: "exceeds max vesting epochs",
			msg: types.MsgTransferWithVesting{
				Sender:        sender,
				Recipient:     recipient,
				Amount:        sdk.NewCoins(sdk.NewCoin("gonka", math.NewInt(100))),
				VestingEpochs: types.MaxVestingEpochs + 1,
			},
			expectErr: "exceeds maximum allowed",
		},
		// NOTE: "exceeds max coin denominations" is unreachable with the current
		// denom restriction (only gonka/ngonka → max 2). The MaxCoinsInAmount
		// check remains as defense-in-depth if the allowed denom set grows.
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.msg.ValidateBasic()
			if tc.expectErr == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectErr)
			}
		})
	}
}
