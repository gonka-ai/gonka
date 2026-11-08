package signing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type countingVerifier struct {
	inner Verifier
	calls int
}

func (c *countingVerifier) RecoverAddress(message []byte, sig []byte) (string, error) {
	c.calls++
	return c.inner.RecoverAddress(message, sig)
}

func TestCachingVerifierRecoversASignatureOnce(t *testing.T) {
	signer, err := GenerateKey()
	require.NoError(t, err)
	msg := []byte("finish")
	sig, err := signer.Sign(msg)
	require.NoError(t, err)
	inner := &countingVerifier{inner: NewSecp256k1Verifier()}
	v := NewCachingVerifier(inner, 8)

	for range 3 {
		addr, err := v.RecoverAddress(msg, sig)
		require.NoError(t, err)
		require.Equal(t, signer.Address(), addr)
	}
	require.Equal(t, 1, inner.calls)

	// The same signature over another message is another recovery.
	other, err := v.RecoverAddress([]byte("receipt"), sig)
	require.NoError(t, err)
	require.NotEqual(t, signer.Address(), other)
	require.Equal(t, 2, inner.calls)
}

func TestCachingVerifierDoesNotKeepAFailedRecovery(t *testing.T) {
	inner := &countingVerifier{inner: NewSecp256k1Verifier()}
	v := NewCachingVerifier(inner, 8)
	for range 2 {
		_, err := v.RecoverAddress([]byte("receipt"), []byte("receipt"))
		require.ErrorContains(t, err, "invalid signature length")
	}
	require.Equal(t, 2, inner.calls)
}

func TestCachingVerifierEvictsTheLeastRecentlyUsed(t *testing.T) {
	signer, err := GenerateKey()
	require.NoError(t, err)
	inner := &countingVerifier{inner: NewSecp256k1Verifier()}
	v := NewCachingVerifier(inner, 2)
	sign := func(m string) ([]byte, []byte) {
		sig, err := signer.Sign([]byte(m))
		require.NoError(t, err)
		return []byte(m), sig
	}
	m1, s1 := sign("one")
	m2, s2 := sign("two")
	m3, s3 := sign("three")
	check := func(m, s []byte) {
		addr, err := v.RecoverAddress(m, s)
		require.NoError(t, err)
		require.Equal(t, signer.Address(), addr)
	}

	check(m1, s1)
	check(m2, s2)
	check(m1, s1) // one is now the most recent
	check(m3, s3) // evicts two
	require.Equal(t, 3, inner.calls)
	check(m1, s1)
	require.Equal(t, 3, inner.calls)
	check(m2, s2)
	require.Equal(t, 4, inner.calls)
}
