package heightsync

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
)

func testAnchorSection() *HeightSyncSection {
	return &HeightSyncSection{
		ProofType:             AnchorProofType,
		MainnetHeight:         42,
		MainnetBlockHashHex:   "aabbcc",
		TimestampUnixMs:       1_700_000_000_000,
		Direction:             "response",
		OriginatorSenderID:    "",
		OriginatorTimestampMs: 1_700_000_000_000,
	}
}

func TestSignOrigin_RoundTrip(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	sec := testAnchorSection()
	sec.OriginatorSenderID = signer.Address()

	blob, sig, err := SignOrigin(signer, sec)
	require.NoError(t, err)
	require.NotEmpty(t, blob)
	require.NotEmpty(t, sig)

	sec.SenderSignature = sig
	verifier := signing.NewSecp256k1Verifier()
	require.NoError(t, VerifyOrigin(verifier, sec, sig))
	require.NoError(t, VerifyOriginDetached(verifier, sec, blob, sig))
}

func TestVerifyOrigin_RejectsTamperedHash(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	sec := testAnchorSection()
	sec.OriginatorSenderID = signer.Address()
	_, sig, err := SignOrigin(signer, sec)
	require.NoError(t, err)

	sec.MainnetBlockHashHex = "deadbeef"
	sec.SenderSignature = sig
	require.Error(t, VerifyOrigin(signing.NewSecp256k1Verifier(), sec, sig))
}

func TestVerifyOrigin_RejectsWrongOriginator(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	other := testutil.MustGenerateKey(t)
	sec := testAnchorSection()
	sec.OriginatorSenderID = signer.Address()
	_, sig, err := SignOrigin(signer, sec)
	require.NoError(t, err)

	sec.OriginatorSenderID = other.Address()
	sec.SenderSignature = sig
	require.Error(t, VerifyOrigin(signing.NewSecp256k1Verifier(), sec, sig))
}

func TestCanonicalOriginBytes_DomainSeparated(t *testing.T) {
	sec := testAnchorSection()
	sec.OriginatorSenderID = "gonka1abc"

	b1, err := CanonicalOriginBytes(sec)
	require.NoError(t, err)
	require.Greater(t, len(b1), len(OriginSignDomain))
	require.Equal(t, OriginSignDomain, string(b1[:len(OriginSignDomain)]))

	sec2 := *sec
	sec2.MainnetHeight = 43
	b2, err := CanonicalOriginBytes(&sec2)
	require.NoError(t, err)
	require.NotEqual(t, b1, b2)
}
