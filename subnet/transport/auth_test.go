package transport

import (
	"testing"

	"github.com/stretchr/testify/require"

	"subnet/internal/testutil"
	"subnet/signing"
)

func TestSignVerify_RoundTrip(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()

	body := []byte(`{"hello":"world"}`)
	ts := int64(1000000)

	sig, err := SignRequest(signer, "escrow-1", body, ts)
	require.NoError(t, err)

	addr, err := VerifyRequest(verifier, "escrow-1", body, sig, ts, ts)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), addr)
}

func TestVerify_TamperedBody(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()

	body := []byte(`{"hello":"world"}`)
	ts := int64(1000000)

	sig, err := SignRequest(signer, "escrow-1", body, ts)
	require.NoError(t, err)

	// Tamper with the body.
	tampered := []byte(`{"hello":"tampered"}`)
	addr, err := VerifyRequest(verifier, "escrow-1", tampered, sig, ts, ts)
	require.NoError(t, err)
	// Should recover a different address.
	require.NotEqual(t, signer.Address(), addr)
}

func TestVerify_TimestampOutsideWindow(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()

	body := []byte(`{"test":true}`)
	ts := int64(1000000)

	sig, err := SignRequest(signer, "escrow-1", body, ts)
	require.NoError(t, err)

	// Now is 31s ahead -> drift = 31 > 30 -> error.
	_, err = VerifyRequest(verifier, "escrow-1", body, sig, ts, ts+31)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timestamp drift")

	// Now is 31s behind -> drift = 31 > 30 -> error.
	_, err = VerifyRequest(verifier, "escrow-1", body, sig, ts, ts-31)
	require.Error(t, err)
	require.Contains(t, err.Error(), "timestamp drift")

	// Within window (30s) -> ok.
	addr, err := VerifyRequest(verifier, "escrow-1", body, sig, ts, ts+30)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), addr)
}

func TestVerify_DifferentEscrowID(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()

	body := []byte(`{"test":true}`)
	ts := int64(1000000)

	sig, err := SignRequest(signer, "escrow-1", body, ts)
	require.NoError(t, err)

	// Verify with different escrow ID -> different recovered address.
	addr, err := VerifyRequest(verifier, "escrow-2", body, sig, ts, ts)
	require.NoError(t, err)
	require.NotEqual(t, signer.Address(), addr)
}
