package types

import (
	"strings"
	"testing"

	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil/sample"
)

func validValidationEntry(subject string) *TeeValidationEntry {
	return &TeeValidationEntry{
		SubjectAddress:  subject,
		NodeId:          "node-1",
		MeasurementSeen: "deadbeef",
		Verdict:         TeeVerdict_TEE_VERDICT_OK,
		Reason:          "",
	}
}

func TestMsgSubmitTeeValidations_ValidateBasic(t *testing.T) {
	creator := sample.AccAddress()
	subject := sample.AccAddress()
	otherSubject := sample.AccAddress()

	tests := []struct {
		name string
		msg  MsgSubmitTeeValidations
		err  error
	}{
		{
			name: "valid single OK vote",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations:              []*TeeValidationEntry{validValidationEntry(subject)},
			},
		},
		{
			name: "valid REJECT with reason",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{{
					SubjectAddress: subject,
					NodeId:         "node-1",
					Verdict:        TeeVerdict_TEE_VERDICT_REJECT,
					Reason:         "crl-revoked",
				}},
			},
		},
		{
			name: "invalid creator",
			msg: MsgSubmitTeeValidations{
				Creator:                  "not-bech32",
				PocStageStartBlockHeight: 100,
				Validations:              []*TeeValidationEntry{validValidationEntry(subject)},
			},
			err: sdkerrors.ErrInvalidAddress,
		},
		{
			name: "zero stage height",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 0,
				Validations:              []*TeeValidationEntry{validValidationEntry(subject)},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "empty validations",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations:              nil,
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "nil entry",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations:              []*TeeValidationEntry{nil},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "invalid subject address",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{{
					SubjectAddress: "not-bech32",
					NodeId:         "node-1",
					Verdict:        TeeVerdict_TEE_VERDICT_OK,
				}},
			},
			err: sdkerrors.ErrInvalidAddress,
		},
		{
			name: "empty node id",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{{
					SubjectAddress: subject,
					NodeId:         "",
					Verdict:        TeeVerdict_TEE_VERDICT_OK,
				}},
			},
			err: ErrTeeAttestationNodeIdEmpty,
		},
		{
			name: "duplicate (subject, node)",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{
					validValidationEntry(subject),
					validValidationEntry(subject),
				},
			},
			err: ErrTeeValidationAlreadyExists,
		},
		{
			name: "two subjects same node id is OK",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{
					validValidationEntry(subject),
					validValidationEntry(otherSubject),
				},
			},
		},
		{
			name: "unspecified verdict rejected",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{{
					SubjectAddress: subject,
					NodeId:         "node-1",
					Verdict:        TeeVerdict_TEE_VERDICT_UNSPECIFIED,
				}},
			},
			err: ErrTeeValidationVerdictUnspecified,
		},
		{
			name: "OK vote without measurement_seen rejected",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{{
					SubjectAddress: subject,
					NodeId:         "node-1",
					Verdict:        TeeVerdict_TEE_VERDICT_OK,
				}},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "non-hex measurement rejected",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{{
					SubjectAddress:  subject,
					NodeId:          "node-1",
					MeasurementSeen: "zzzz",
					Verdict:         TeeVerdict_TEE_VERDICT_OK,
				}},
			},
			err: ErrTeeAttestationMeasurementNotAllowed,
		},
		{
			name: "0x-prefixed uppercase measurement accepted (canonicalized)",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{{
					SubjectAddress:  subject,
					NodeId:          "node-1",
					MeasurementSeen: "0xDEADBEEF",
					Verdict:         TeeVerdict_TEE_VERDICT_OK,
				}},
			},
		},
		{
			name: "reason too long",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{{
					SubjectAddress: subject,
					NodeId:         "node-1",
					Verdict:        TeeVerdict_TEE_VERDICT_REJECT,
					Reason:         strings.Repeat("x", MaxTeeValidationReasonBytes+1),
				}},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "reason must be valid utf8",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{{
					SubjectAddress: subject,
					NodeId:         "node-1",
					Verdict:        TeeVerdict_TEE_VERDICT_REJECT,
					Reason:         string([]byte{0xff, 0xfe}),
				}},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "reason with non printable chars rejected",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{{
					SubjectAddress: subject,
					NodeId:         "node-1",
					Verdict:        TeeVerdict_TEE_VERDICT_REJECT,
					Reason:         "line1\nline2",
				}},
			},
			err: sdkerrors.ErrInvalidRequest,
		},
		{
			name: "node id too long",
			msg: MsgSubmitTeeValidations{
				Creator:                  creator,
				PocStageStartBlockHeight: 100,
				Validations: []*TeeValidationEntry{{
					SubjectAddress: subject,
					NodeId:         strings.Repeat("n", MaxTeeNodeIDLength+1),
					Verdict:        TeeVerdict_TEE_VERDICT_OK,
				}},
			},
			err: sdkerrors.ErrInvalidRequest,
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

func TestMsgSubmitTeeAttestation_QuoteSizeCap(t *testing.T) {
	creator := sample.AccAddress()
	pubkey := make([]byte, 32)

	tooLarge := make([]byte, MaxTeeQuoteBytes+1)
	msg := MsgSubmitTeeAttestation{
		Creator:                  creator,
		PocStageStartBlockHeight: 100,
		Attestations: []*TeeAttestationEntry{{
			NodeId:    "node-1",
			PublicKey: pubkey,
			Provider:  "intel-tdx-lite",
			Quote:     tooLarge,
		}},
	}
	require.ErrorIs(t, msg.ValidateBasic(), ErrTeeAttestationQuoteTooLarge)

	msg.Attestations[0].Quote = make([]byte, MaxTeeQuoteBytes)
	require.NoError(t, msg.ValidateBasic())
}
