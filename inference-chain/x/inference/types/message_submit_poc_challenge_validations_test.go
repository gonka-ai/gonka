package types

import (
	"strings"
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/testutil/sample"
	"github.com/stretchr/testify/require"
)

func challengeVoteEntries(target string, count int) []*PoCValidationEntryV2 {
	entries := make([]*PoCValidationEntryV2, 0, count)
	for i := 0; i < count; i++ {
		entries = append(entries, &PoCValidationEntryV2{
			ParticipantAddress: target,
			ModelId:            "model-" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strings.Repeat("y", i/26),
			ValidatedWeight:    1,
		})
	}
	return entries
}

func TestMsgSubmitPoCChallengeValidations_ValidateBasic(t *testing.T) {
	creator := sample.AccAddress()
	target := sample.AccAddress()

	tests := []struct {
		name string
		msg  MsgSubmitPoCChallengeValidations
		err  error
	}{
		{
			name: "invalid creator",
			msg: MsgSubmitPoCChallengeValidations{
				Creator:     "invalid_address",
				Validations: challengeVoteEntries(target, 1),
			},
			err: sdkerrors.ErrInvalidAddress,
		},
		{
			name: "non-canonical creator",
			msg: MsgSubmitPoCChallengeValidations{
				Creator:     strings.ToUpper(creator),
				Validations: challengeVoteEntries(target, 1),
			},
			err: sdkerrors.ErrInvalidAddress,
		},
		{
			name: "no entries",
			msg: MsgSubmitPoCChallengeValidations{
				Creator: creator,
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "one entry",
			msg: MsgSubmitPoCChallengeValidations{
				Creator:     creator,
				Validations: challengeVoteEntries(target, 1),
			},
		},
		{
			name: "entries at cap",
			msg: MsgSubmitPoCChallengeValidations{
				Creator:     creator,
				Validations: challengeVoteEntries(target, MaxPoCChallengeValidationsPerMsg),
			},
		},
		{
			name: "entries over cap",
			msg: MsgSubmitPoCChallengeValidations{
				Creator:     creator,
				Validations: challengeVoteEntries(target, MaxPoCChallengeValidationsPerMsg+1),
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "nil entry",
			msg: MsgSubmitPoCChallengeValidations{
				Creator:     creator,
				Validations: []*PoCValidationEntryV2{nil},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "empty model id",
			msg: MsgSubmitPoCChallengeValidations{
				Creator:     creator,
				Validations: []*PoCValidationEntryV2{{ParticipantAddress: target, ModelId: ""}},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "model id too long",
			msg: MsgSubmitPoCChallengeValidations{
				Creator: creator,
				Validations: []*PoCValidationEntryV2{{
					ParticipantAddress: target,
					ModelId:            strings.Repeat("m", MaxPoCChallengeModelIdLength+1),
				}},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "model id at max length",
			msg: MsgSubmitPoCChallengeValidations{
				Creator: creator,
				Validations: []*PoCValidationEntryV2{{
					ParticipantAddress: target,
					ModelId:            strings.Repeat("m", MaxPoCChallengeModelIdLength),
				}},
			},
		},
		{
			name: "invalid participant address",
			msg: MsgSubmitPoCChallengeValidations{
				Creator:     creator,
				Validations: []*PoCValidationEntryV2{{ParticipantAddress: "not-an-address", ModelId: "m1"}},
			},
			err: sdkerrors.ErrInvalidAddress,
		},
		{
			name: "non-canonical participant address",
			msg: MsgSubmitPoCChallengeValidations{
				Creator:     creator,
				Validations: []*PoCValidationEntryV2{{ParticipantAddress: strings.ToUpper(target), ModelId: "m1"}},
			},
			err: sdkerrors.ErrInvalidAddress,
		},
		{
			name: "duplicate participant and model",
			msg: MsgSubmitPoCChallengeValidations{
				Creator: creator,
				Validations: []*PoCValidationEntryV2{
					{ParticipantAddress: target, ModelId: "m1", ValidatedWeight: 1},
					{ParticipantAddress: target, ModelId: "m1", ValidatedWeight: 0},
				},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "same participant different models",
			msg: MsgSubmitPoCChallengeValidations{
				Creator: creator,
				Validations: []*PoCValidationEntryV2{
					{ParticipantAddress: target, ModelId: "m1", ValidatedWeight: 1},
					{ParticipantAddress: target, ModelId: "m2", ValidatedWeight: 0},
				},
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
