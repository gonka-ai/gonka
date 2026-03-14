package types_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/streamvesting/types"
)

func TestMsgBatchTransferWithVesting_ValidateBasic(t *testing.T) {
	sender := validAddr(1)
	recipient1 := validAddr(2)
	recipient2 := validAddr(3)

	validOutput := func(recipient string, denom string, amount int64) types.BatchVestingOutput {
		return types.BatchVestingOutput{
			Recipient: recipient,
			Amount:    sdk.NewCoins(sdk.NewCoin(denom, math.NewInt(amount))),
		}
	}

	tests := []struct {
		name      string
		msg       types.MsgBatchTransferWithVesting
		expectErr string
	}{
		{
			name: "valid single output",
			msg: types.MsgBatchTransferWithVesting{
				Sender:  sender,
				Outputs: []types.BatchVestingOutput{validOutput(recipient1, "gonka", 100)},
			},
		},
		{
			name: "valid multiple outputs",
			msg: types.MsgBatchTransferWithVesting{
				Sender: sender,
				Outputs: []types.BatchVestingOutput{
					validOutput(recipient1, "gonka", 50),
					validOutput(recipient2, "ngonka", types.MinTransferNgonka),
				},
			},
		},
		{
			name: "valid at max vesting epochs",
			msg: types.MsgBatchTransferWithVesting{
				Sender:        sender,
				Outputs:       []types.BatchVestingOutput{validOutput(recipient1, "gonka", 100)},
				VestingEpochs: types.MaxVestingEpochs,
			},
		},
		{
			name: "invalid sender address",
			msg: types.MsgBatchTransferWithVesting{
				Sender:  "bad",
				Outputs: []types.BatchVestingOutput{validOutput(recipient1, "gonka", 100)},
			},
			expectErr: "invalid sender address",
		},
		{
			name: "empty outputs",
			msg: types.MsgBatchTransferWithVesting{
				Sender:  sender,
				Outputs: nil,
			},
			expectErr: "outputs cannot be empty",
		},
		{
			name: "invalid recipient in second output",
			msg: types.MsgBatchTransferWithVesting{
				Sender: sender,
				Outputs: []types.BatchVestingOutput{
					validOutput(recipient1, "gonka", 100),
					{Recipient: "bad", Amount: sdk.NewCoins(sdk.NewCoin("gonka", math.NewInt(100)))},
				},
			},
			expectErr: "invalid recipient address at index 1",
		},
		{
			name: "zero amount in output",
			msg: types.MsgBatchTransferWithVesting{
				Sender: sender,
				Outputs: []types.BatchVestingOutput{
					{Recipient: recipient1, Amount: sdk.NewCoins()},
				},
			},
			expectErr: "amount cannot be zero",
		},
		{
			name: "dust gonka in first output",
			msg: types.MsgBatchTransferWithVesting{
				Sender:  sender,
				Outputs: []types.BatchVestingOutput{validOutput(recipient1, "gonka", 5)},
			},
			expectErr: "below minimum of 10 gonka",
		},
		{
			name: "dust ngonka in second output",
			msg: types.MsgBatchTransferWithVesting{
				Sender: sender,
				Outputs: []types.BatchVestingOutput{
					validOutput(recipient1, "gonka", 100),
					validOutput(recipient2, "ngonka", 1000),
				},
			},
			expectErr: "recipient at index 1",
		},
		{
			name: "unsupported denom in output",
			msg: types.MsgBatchTransferWithVesting{
				Sender:  sender,
				Outputs: []types.BatchVestingOutput{validOutput(recipient1, "stake", 1000)},
			},
			expectErr: "unsupported denomination",
		},
		{
			name: "exceeds max vesting epochs",
			msg: types.MsgBatchTransferWithVesting{
				Sender:        sender,
				Outputs:       []types.BatchVestingOutput{validOutput(recipient1, "gonka", 100)},
				VestingEpochs: types.MaxVestingEpochs + 1,
			},
			expectErr: "exceeds maximum allowed",
		},
		{
			name: "exceeds max recipients",
			msg: func() types.MsgBatchTransferWithVesting {
				outputs := make([]types.BatchVestingOutput, types.MaxBatchRecipients+1)
				for i := range outputs {
					outputs[i] = validOutput(validAddr(byte(10+i%200)), "gonka", 100)
				}
				return types.MsgBatchTransferWithVesting{Sender: sender, Outputs: outputs}
			}(),
			expectErr: "too many recipients",
		},
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
