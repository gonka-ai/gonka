package heightsync

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func testAck() *types.MsgHeightAck {
	return &types.MsgHeightAck{
		TurnSeq:           3,
		RefNonce:          10,
		SlotId:            1,
		ObservedHeight:    42,
		ObservedBlockHash: []byte{0xaa, 0xbb},
		SyncState:         types.SyncState_SYNCED,
		PeerSeen:          []byte{0x0f},
	}
}

func TestSignAck_RoundTrip(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	ack := testAck()
	require.NoError(t, SignAck(signer, ack))
	require.NotEmpty(t, ack.HostSig)

	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, VerifyAck(verifier, ack, signer.Address()))
}

func TestVerifyAck_RejectsTamperedHeight(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	ack := testAck()
	require.NoError(t, SignAck(signer, ack))
	ack.ObservedHeight = 43
	require.Error(t, VerifyAck(signing.NewSecp256k1Verifier(), ack, signer.Address()))
}

func TestVerifyAck_RejectsWrongSlotKey(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	other := testutil.MustGenerateKey(t)
	ack := testAck()
	require.NoError(t, SignAck(signer, ack))
	require.Error(t, VerifyAck(signing.NewSecp256k1Verifier(), ack, other.Address()))
}

func TestCanonicalAckBytes_DomainSeparated(t *testing.T) {
	ack := testAck()
	b1, err := CanonicalAckBytes(ack)
	require.NoError(t, err)
	require.True(t, len(b1) > len(DomainHeightAck))
	require.Equal(t, DomainHeightAck, string(b1[:len(DomainHeightAck)]))

	ack.HostSig = []byte{1, 2, 3}
	b2, err := CanonicalAckBytes(ack)
	require.NoError(t, err)
	require.Equal(t, b1, b2, "field 8 is excluded from the signing input")

	ack.ObservedHeight = 43
	b3, err := CanonicalAckBytes(ack)
	require.NoError(t, err)
	require.NotEqual(t, b1, b3)
}
