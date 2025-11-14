package types

import (
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func TestMsgRegisterModel_ValidateBasic(t *testing.T) {
	tests := []struct {
		name string
		msg  MsgRegisterModel
		err  error
	}{
		{
			name: "invalid authority address",
			msg: MsgRegisterModel{
				Authority:              "invalid_address",
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-1",
				UnitsOfComputePerToken: 100,
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidAddress,
		},
		{
			name: "invalid proposed_by address",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             "invalid_address",
				Id:                     "model-1",
				UnitsOfComputePerToken: 100,
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidAddress,
		},
		{
			name: "empty id",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "",
				UnitsOfComputePerToken: 100,
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "id with invalid characters",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model@123",
				UnitsOfComputePerToken: 100,
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "id too long",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     string(make([]byte, MaxModelLen+1)) + "a",
				UnitsOfComputePerToken: 100,
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "zero units_of_compute_per_token",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-1",
				UnitsOfComputePerToken: 0,
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "invalid hf_commit length",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-1",
				UnitsOfComputePerToken: 100,
				HfCommit:               "abc123",
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "invalid hf_commit format",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-1",
				UnitsOfComputePerToken: 100,
				HfCommit:               "g" + string(make([]byte, HfCommitLength-1)),
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "empty model_arg",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-1",
				UnitsOfComputePerToken: 100,
				ModelArgs:              []string{"arg1", ""},
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "too many model_args",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-1",
				UnitsOfComputePerToken: 100,
				ModelArgs:              make([]string, MaxModelArgsCount+1),
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "zero v_ram",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-1",
				UnitsOfComputePerToken: 100,
				VRam:                   0,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "zero throughput_per_nonce",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-1",
				UnitsOfComputePerToken: 100,
				VRam:                   8192,
				ThroughputPerNonce:      0,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "validation_threshold below 0",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-1",
				UnitsOfComputePerToken: 100,
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: -10, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "validation_threshold above 1",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-1",
				UnitsOfComputePerToken: 100,
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 150, Exponent: -2},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "valid message - minimal",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-1",
				UnitsOfComputePerToken: 100,
				VRam:                   8192,
				ThroughputPerNonce:      1000,
				ValidationThreshold:    &Decimal{Value: 85, Exponent: -2},
			},
		},
		{
			name: "valid message - full",
			msg: MsgRegisterModel{
				Authority:              sample.AccAddress(),
				ProposedBy:             sample.AccAddress(),
				Id:                     "model-123_abc",
				UnitsOfComputePerToken: 100,
				HfRepo:                 "https://huggingface.co/user/model",
				HfCommit:               "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
				ModelArgs:              []string{"--arg1", "value1", "--arg2"},
				VRam:                   16384,
				ThroughputPerNonce:      2000,
				ValidationThreshold:    &Decimal{Value: 90, Exponent: -2},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.ValidateBasic()
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
				return
			}
			require.NoError(t, err)
		})
	}
}
