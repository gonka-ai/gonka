package transport

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/transport/rpcpb"
)

func TestSignEnvelope_CompatibleWithSignRequest(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	body := []byte(`{"hello":"world"}`)
	ts := int64(1_000_000)

	sig, err := SignRequest(signer, "escrow-1", body, ts)
	require.NoError(t, err)

	addr, err := VerifyEnvelope(verifier, &rpcpb.SignedEnvelope{
		Payload:   body,
		Signature: sig,
		Timestamp: ts,
		EscrowId:  "escrow-1",
	}, ts)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), addr)

	env, err := SignEnvelope(signer, "escrow-1", body, ts)
	require.NoError(t, err)
	addr, err = VerifyRequest(verifier, env.EscrowId, env.Payload, env.Signature, env.Timestamp, ts)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), addr)
}

func TestVerifyEnvelope_Tamper(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	body := []byte(`{"hello":"world"}`)
	ts := int64(1_000_000)
	env, err := SignEnvelope(signer, "escrow-1", body, ts)
	require.NoError(t, err)

	assertNotOriginalSigner := func(t *testing.T, tampered *rpcpb.SignedEnvelope) {
		t.Helper()
		addr, err := VerifyEnvelope(verifier, tampered, ts)
		if err != nil {
			return
		}
		require.NotEqual(t, signer.Address(), addr)
	}
	t.Run("payload", func(t *testing.T) {
		tampered := protoCloneEnvelope(env)
		tampered.Payload = []byte(`{"hello":"tampered"}`)
		assertNotOriginalSigner(t, tampered)
	})
	t.Run("escrow_id", func(t *testing.T) {
		tampered := protoCloneEnvelope(env)
		tampered.EscrowId = "escrow-2"
		assertNotOriginalSigner(t, tampered)
	})
	t.Run("timestamp", func(t *testing.T) {
		tampered := protoCloneEnvelope(env)
		tampered.Timestamp = ts + 1
		assertNotOriginalSigner(t, tampered)
	})
}

func TestSignAttach_RoundTripAndBinding(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	attachNonce := []byte("attach-nonce-0123456789abcdef")
	const host = "host-a"
	const ts int64 = 1_000_000
	sig, err := SignAttach(signer, host, ts, signer.Address(), attachNonce, "v5", nil)
	require.NoError(t, err)
	req := &rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     attachNonce,
		ProtocolVersion: "v5",
		HostAddress:     host,
		Timestamp:       ts,
		Signature:       sig,
	}
	addr, err := VerifyAttach(verifier, req, ts)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), addr)

	req.ProtocolVersion = "v6"
	addr, err = VerifyAttach(verifier, req, ts)
	require.NoError(t, err)
	require.NotEqual(t, signer.Address(), addr)
}

func TestSignAttach_HostAndTimestampBinding(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	nonce := []byte("attach-nonce-host-ts-01234567")
	const ts int64 = 1_000_000
	sig, err := SignAttach(signer, "host-a", ts, signer.Address(), nonce, "", nil)
	require.NoError(t, err)

	addr, err := VerifyAttach(verifier, &rpcpb.AttachRequest{
		PeerAddress: signer.Address(),
		AttachNonce: nonce,
		HostAddress: "host-b",
		Timestamp:   ts,
		Signature:   sig,
	}, ts)
	require.NoError(t, err)
	require.NotEqual(t, signer.Address(), addr)

	_, err = VerifyAttach(verifier, &rpcpb.AttachRequest{
		PeerAddress: signer.Address(),
		AttachNonce: nonce,
		HostAddress: "host-a",
		Timestamp:   ts,
		Signature:   sig,
	}, ts+31)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timestamp drift")
}

func TestSignAttach_LengthPrefixStopsShift(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	nonce := []byte("abcd")
	sig, err := SignAttach(signer, "host-a", 1_000_000, signer.Address(), nonce, "XY", nil)
	require.NoError(t, err)
	addr, err := VerifyAttach(verifier, &rpcpb.AttachRequest{
		PeerAddress:     signer.Address(),
		AttachNonce:     []byte("abcdX"),
		ProtocolVersion: "Y",
		HostAddress:     "host-a",
		Timestamp:       1_000_000,
		Signature:       sig,
	}, 1_000_000)
	require.NoError(t, err)
	require.NotEqual(t, signer.Address(), addr)
}

func TestVerifyEnvelope_TimestampDrift(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	body := []byte(`{"test":true}`)
	ts := int64(1_000_000)
	env, err := SignEnvelope(signer, "escrow-1", body, ts)
	require.NoError(t, err)

	addr, err := VerifyEnvelope(verifier, env, ts+29)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), addr)

	addr, err = VerifyEnvelope(verifier, env, ts+30)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), addr)

	_, err = VerifyEnvelope(verifier, env, ts+31)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timestamp drift")

	_, err = VerifyEnvelope(verifier, env, ts-31)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timestamp drift")
}

func protoCloneEnvelope(env *rpcpb.SignedEnvelope) *rpcpb.SignedEnvelope {
	return proto.Clone(env).(*rpcpb.SignedEnvelope)
}
