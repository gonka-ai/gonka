package app_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
	"github.com/stretchr/testify/require"

	blstypes "github.com/productscience/inference/x/bls/types"
)

// BLS duties: registration is free, so CheckTx also requires the actor to be
// in the DKG participant list its handler reads.

const (
	// blsFallbackEpoch has BLS data while blsFallbackEpoch-1 has none, the
	// case where group key validation falls back to the new epoch's list.
	blsFallbackEpoch = uint64(501)
)

var blsRequestID = bytes.Repeat([]byte{7}, 32)

// setupBLSDuty: epoch dutyEpochIndex has DKG participant `participant`,
// dutyEpochIndex-1 has `previous`, blsFallbackEpoch has `previous` with no
// data for the epoch before it, and a signing request uses dutyEpochIndex.
// `registered` is registered but in no DKG.
func setupBLSDuty(t *testing.T) dutyCheckTxFixture {
	t.Helper()
	f := setupDutyCheckTx(t)
	ctx := f.testApp.NewUncachedContext(false, cmtproto.Header{
		Height:  f.testApp.LastBlockHeight(),
		ChainID: TallyTestChainID,
		Time:    time.Now().UTC(),
	})
	bk := f.testApp.BlsKeeper
	epoch := func(id uint64, members ...sdk.AccAddress) {
		data := blstypes.EpochBLSData{EpochId: id, DkgPhase: blstypes.DKGPhase_DKG_PHASE_COMPLETED}
		for i, m := range members {
			data.Participants = append(data.Participants, blstypes.BLSParticipantInfo{
				Address:        m.String(),
				SlotStartIndex: uint32(i),
				SlotEndIndex:   uint32(i),
			})
		}
		require.NoError(t, bk.SetEpochBLSData(ctx, data))
	}
	epoch(dutyEpochIndex, f.participant)
	epoch(dutyEpochIndex-1, f.previous)
	epoch(blsFallbackEpoch, f.previous)
	require.NoError(t, bk.StoreThresholdSigningRequest(ctx, &blstypes.ThresholdSigningRequest{
		RequestId:           blsRequestID,
		CurrentEpochId:      dutyEpochIndex,
		Status:              blstypes.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COLLECTING_SIGNATURES,
		DeadlineBlockHeight: 1_000_000,
	}))
	expiry := time.Now().UTC().Add(365 * 24 * time.Hour)
	// The DAPI's warm key holds grants for every BLS duty of its member.
	for _, granter := range []sdk.AccAddress{f.participant, f.previous} {
		for _, msg := range []sdk.Msg{
			&blstypes.MsgSubmitDealerPart{},
			&blstypes.MsgSubmitVerificationVector{},
			&blstypes.MsgRespondDealerComplaints{},
			&blstypes.MsgSubmitPartialSignature{},
			&blstypes.MsgSubmitGroupKeyValidationSignature{},
		} {
			require.NoError(t, f.testApp.AuthzKeeper.SaveGrant(
				ctx, f.grantee, granter,
				authztypes.NewGenericAuthorization(sdk.MsgTypeURL(msg)),
				&expiry,
			))
		}
	}
	_, err := f.testApp.FinalizeBlock(&abci.RequestFinalizeBlock{
		Height: f.testApp.LastBlockHeight() + 1,
		Time:   time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = f.testApp.Commit()
	require.NoError(t, err)
	return f
}

func verificationVectorFor(addr sdk.AccAddress, epochID uint64) *blstypes.MsgSubmitVerificationVector {
	return &blstypes.MsgSubmitVerificationVector{Creator: addr.String(), EpochId: epochID, DealerValidity: []bool{true}}
}

func dealerPartFor(addr sdk.AccAddress, epochID uint64) *blstypes.MsgSubmitDealerPart {
	return &blstypes.MsgSubmitDealerPart{
		Creator:     addr.String(),
		EpochId:     epochID,
		Commitments: [][]byte{bytes.Repeat([]byte{1}, 96)},
		EncryptedSharesForParticipants: []blstypes.EncryptedSharesForParticipant{{
			EncryptedShares: [][]byte{bytes.Repeat([]byte{1}, blstypes.MinEncryptedShareCiphertextLen)},
		}},
	}
}

func dealerComplaintsFor(addr sdk.AccAddress, epochID uint64) *blstypes.MsgRespondDealerComplaints {
	return &blstypes.MsgRespondDealerComplaints{
		Creator: addr.String(),
		EpochId: epochID,
		Responses: []blstypes.DealerComplaintResponse{{
			ResponseShareBytes:      bytes.Repeat([]byte{1}, 32),
			ResponseOpeningMaterial: bytes.Repeat([]byte{1}, 32),
		}},
	}
}

func partialSignatureFor(addr sdk.AccAddress, requestID []byte) *blstypes.MsgSubmitPartialSignature {
	return &blstypes.MsgSubmitPartialSignature{
		Creator:          addr.String(),
		RequestId:        requestID,
		SlotIndices:      []uint32{0},
		PartialSignature: bytes.Repeat([]byte{1}, 48),
	}
}

func groupKeyValidationFor(addr sdk.AccAddress, newEpochID uint64) *blstypes.MsgSubmitGroupKeyValidationSignature {
	return &blstypes.MsgSubmitGroupKeyValidationSignature{
		Creator:          addr.String(),
		NewEpochId:       newEpochID,
		SlotIndices:      []uint32{0},
		PartialSignature: bytes.Repeat([]byte{1}, 48),
	}
}

func requireAdmitted(t *testing.T, resp *abci.ResponseCheckTx, what string) {
	t.Helper()
	require.Equal(t, uint32(0), resp.Code, "%s must be admitted; log=%q", what, resp.Log)
}

func requireRejectedWith(t *testing.T, resp *abci.ResponseCheckTx, want error, what string) {
	t.Helper()
	require.NotEqual(t, uint32(0), resp.Code, "%s must be rejected at CheckTx; log=%q", what, resp.Log)
	require.True(t, strings.Contains(resp.Log, want.Error()), "%s: want %q in log %q", what, want.Error(), resp.Log)
}

func TestNetworkDuty_CheckTx_BLS_VerificationVector(t *testing.T) {
	f := setupBLSDuty(t)

	resp := runDutyCheckTx(t, f, []sdk.Msg{verificationVectorFor(f.registered, dutyEpochIndex)}, f.registered, f.registeredPriv)
	requireRejectedWith(t, resp, blstypes.ErrNotDKGParticipant, "registered non-member")

	resp = runDutyCheckTx(t, f, []sdk.Msg{verificationVectorFor(f.participant, dutyEpochIndex+5)}, f.participant, f.partPriv)
	requireRejectedWith(t, resp, blstypes.ErrEpochBLSDataNotFound, "epoch without DKG data")

	resp = runDutyCheckTx(t, f, []sdk.Msg{verificationVectorFor(f.participant, dutyEpochIndex)}, f.participant, f.partPriv)
	requireAdmitted(t, resp, "DKG member")
}

// The DAPI sends BLS duties wrapped in MsgExec, signed by a granted warm key,
// with Creator = the DKG member. Each message type is admitted for a member
// and rejected for a non-member on that path too.
func TestNetworkDuty_CheckTx_BLS_WarmKey(t *testing.T) {
	f := setupBLSDuty(t)
	cases := []struct {
		name   string
		member sdk.Msg
		other  sdk.Msg
	}{
		{"dealer part", dealerPartFor(f.participant, dutyEpochIndex), dealerPartFor(f.previous, dutyEpochIndex)},
		{"verification vector", verificationVectorFor(f.participant, dutyEpochIndex), verificationVectorFor(f.previous, dutyEpochIndex)},
		{"dealer complaints", dealerComplaintsFor(f.participant, dutyEpochIndex), dealerComplaintsFor(f.previous, dutyEpochIndex)},
		{"partial signature", partialSignatureFor(f.participant, blsRequestID), partialSignatureFor(f.previous, blsRequestID)},
		{"group key validation", groupKeyValidationFor(f.previous, dutyEpochIndex), groupKeyValidationFor(f.participant, dutyEpochIndex)},
		{"group key validation fallback", groupKeyValidationFor(f.previous, blsFallbackEpoch), groupKeyValidationFor(f.participant, blsFallbackEpoch)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execMsg := authztypes.NewMsgExec(f.grantee, []sdk.Msg{tc.member})
			resp := runDutyCheckTx(t, f, []sdk.Msg{&execMsg}, f.grantee, f.granteePriv)
			requireAdmitted(t, resp, "warm key acting for a DKG member")

			execMsg = authztypes.NewMsgExec(f.grantee, []sdk.Msg{tc.other})
			resp = runDutyCheckTx(t, f, []sdk.Msg{&execMsg}, f.grantee, f.granteePriv)
			requireRejectedWith(t, resp, blstypes.ErrNotDKGParticipant, "warm key acting for a non-member")
		})
	}
}

func TestNetworkDuty_CheckTx_BLS_PartialSignature(t *testing.T) {
	f := setupBLSDuty(t)

	resp := runDutyCheckTx(t, f, []sdk.Msg{partialSignatureFor(f.registered, blsRequestID)}, f.registered, f.registeredPriv)
	requireRejectedWith(t, resp, blstypes.ErrNotDKGParticipant, "registered non-member on a live request")

	resp = runDutyCheckTx(t, f, []sdk.Msg{partialSignatureFor(f.participant, bytes.Repeat([]byte{9}, 32))}, f.participant, f.partPriv)
	requireRejectedWith(t, resp, blstypes.ErrSigningRequestNotFound, "unknown request id")

	resp = runDutyCheckTx(t, f, []sdk.Msg{partialSignatureFor(f.participant, blsRequestID)}, f.participant, f.partPriv)
	requireAdmitted(t, resp, "member of the request's epoch")
}

func TestNetworkDuty_CheckTx_BLS_GroupKeyValidation(t *testing.T) {
	f := setupBLSDuty(t)

	// The handler checks the previous epoch's list, not the new one.
	resp := runDutyCheckTx(t, f, []sdk.Msg{groupKeyValidationFor(f.previous, dutyEpochIndex)}, f.previous, f.previousPriv)
	requireAdmitted(t, resp, "member of NewEpochId-1")
	resp = runDutyCheckTx(t, f, []sdk.Msg{groupKeyValidationFor(f.participant, dutyEpochIndex)}, f.participant, f.partPriv)
	requireRejectedWith(t, resp, blstypes.ErrNotDKGParticipant, "member of NewEpochId only")

	// No BLS data for NewEpochId-1: the handler falls back to NewEpochId.
	resp = runDutyCheckTx(t, f, []sdk.Msg{groupKeyValidationFor(f.previous, blsFallbackEpoch)}, f.previous, f.previousPriv)
	requireAdmitted(t, resp, "member of NewEpochId via the fallback")
	resp = runDutyCheckTx(t, f, []sdk.Msg{groupKeyValidationFor(f.registered, blsFallbackEpoch)}, f.registered, f.registeredPriv)
	requireRejectedWith(t, resp, blstypes.ErrNotDKGParticipant, "registered non-member via the fallback")
}
