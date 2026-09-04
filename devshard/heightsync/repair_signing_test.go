package heightsync

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func testRepairRequest() *RepairRequest {
	return &RepairRequest{
		TurnStart:         3,
		RefNonce:          10,
		RequesterSlot:     1,
		ObservedHeight:    500,
		ObservedBlockHash: []byte{0xaa, 0xbb},
	}
}

func TestSignRepairRequest_RoundTrip(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	req := testRepairRequest()
	require.NoError(t, SignRepairRequest(signer, req))
	require.NotEmpty(t, req.RequesterSig)

	v := signing.NewSecp256k1Verifier()
	require.NoError(t, VerifyRepairRequest(v, req, signer.Address()))
}

func TestSignRepairResponse_RoundTrip(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	ackSigner := testutil.MustGenerateKey(t)
	ack := &types.MsgHeightAck{
		RefNonce:          10,
		SlotId:            2,
		ObservedHeight:    510,
		ObservedBlockHash: []byte{0xcc},
		SyncState:         types.SyncState_SYNCED,
		PeerSeen:          []byte{0x07},
	}
	require.NoError(t, SignAck(ackSigner, ack))

	resp := &RepairResponse{
		Outcome:           RepairOutcomeHeight,
		ObservedHeight:    510,
		ObservedBlockHash: []byte{0xcc},
		SyncState:         types.SyncState_SYNCED,
		Ack:               ack,
	}
	require.NoError(t, SignRepairResponse(signer, resp))
	require.NotEmpty(t, resp.ResponderSig)

	v := signing.NewSecp256k1Verifier()
	require.NoError(t, VerifyRepairResponse(v, resp, signer.Address()))
}

func TestRepair_RejectsCrossDomain(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	req := testRepairRequest()
	require.NoError(t, SignRepairRequest(signer, req))

	ackBlob, err := CanonicalAckBytes(&types.MsgHeightAck{
		RefNonce: req.RefNonce, SlotId: req.RequesterSlot,
		ObservedHeight: req.ObservedHeight, ObservedBlockHash: req.ObservedBlockHash,
	})
	require.NoError(t, err)
	cross, err := signer.Sign(ackBlob)
	require.NoError(t, err)
	req.RequesterSig = cross

	err = VerifyRepairRequest(signing.NewSecp256k1Verifier(), req, signer.Address())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrRepairVerify)
}

func TestCanonicalRepairBytes_DomainSeparated(t *testing.T) {
	req := testRepairRequest()
	b1, err := CanonicalRepairRequestBytes(req)
	require.NoError(t, err)
	require.Equal(t, DomainRepair, string(b1[:len(DomainRepair)]))

	req.ObservedHeight = 501
	b2, err := CanonicalRepairRequestBytes(req)
	require.NoError(t, err)
	require.NotEqual(t, b1, b2)

	resp := &RepairResponse{Outcome: RepairOutcomeHeight, ObservedHeight: 500}
	rb, err := CanonicalRepairResponseBytes(resp)
	require.NoError(t, err)
	require.Equal(t, DomainRepair, string(rb[:len(DomainRepair)]))
	require.NotEqual(t, b1, rb)
}

func TestVerifyRepairResponse_RejectsUnreachableOnWire(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	resp := &RepairResponse{Outcome: RepairOutcomeUnreachable, ObservedHeight: 0}
	require.NoError(t, SignRepairResponse(signer, resp))
	err := VerifyRepairResponse(signing.NewSecp256k1Verifier(), resp, signer.Address())
	require.ErrorIs(t, err, ErrRepairOutcome)
}
