package mlnodeclient

import (
	"encoding/base64"
	"errors"
	"net/http"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/require"
)

func TestSignRequest_AddsSignatureHeader(t *testing.T) {
	privKey := secp256k1.GenPrivKey()
	SignFn = func(msg []byte) ([]byte, error) {
		return privKey.Sign(msg)
	}
	defer func() { SignFn = nil }()

	req, err := http.NewRequest(http.MethodPost, "http://test.local", nil)
	require.NoError(t, err)

	body := []byte(`{"test": "data"}`)
	err = signRequest(req, body)
	require.NoError(t, err)

	sig := req.Header.Get("X-Signature")
	require.NotEmpty(t, sig, "X-Signature header should be set")

	sigBytes, err := base64.StdEncoding.DecodeString(sig)
	require.NoError(t, err)
	require.NotEmpty(t, sigBytes)
}

func TestSignRequest_NoSignFn(t *testing.T) {
	SignFn = nil

	req, err := http.NewRequest(http.MethodPost, "http://test.local", nil)
	require.NoError(t, err)

	err = signRequest(req, []byte(`{"test": "data"}`))
	require.NoError(t, err)

	sig := req.Header.Get("X-Signature")
	require.Empty(t, sig, "X-Signature should not be set when SignFn is nil")
}

func TestSignRequest_VerifiableSignature(t *testing.T) {
	privKey := secp256k1.GenPrivKey()
	pubKey := privKey.PubKey()
	SignFn = func(msg []byte) ([]byte, error) {
		return privKey.Sign(msg)
	}
	defer func() { SignFn = nil }()

	body := []byte(`{"model": "test", "action": "init"}`)
	req, err := http.NewRequest(http.MethodPost, "http://test.local/api/v1/test", nil)
	require.NoError(t, err)

	err = signRequest(req, body)
	require.NoError(t, err)

	sigB64 := req.Header.Get("X-Signature")
	sigBytes, err := base64.StdEncoding.DecodeString(sigB64)
	require.NoError(t, err)

	valid := pubKey.VerifySignature(body, sigBytes)
	require.True(t, valid)
}

func TestSignRequest_EmptyBody(t *testing.T) {
	privKey := secp256k1.GenPrivKey()
	SignFn = func(msg []byte) ([]byte, error) {
		return privKey.Sign(msg)
	}
	defer func() { SignFn = nil }()

	req, err := http.NewRequest(http.MethodGet, "http://test.local", nil)
	require.NoError(t, err)

	err = signRequest(req, []byte{})
	require.NoError(t, err)

	sig := req.Header.Get("X-Signature")
	require.NotEmpty(t, sig, "X-Signature should be set even for empty body")
}

func TestSignRequest_DifferentBodyDifferentSignature(t *testing.T) {
	privKey := secp256k1.GenPrivKey()
	SignFn = func(msg []byte) ([]byte, error) {
		return privKey.Sign(msg)
	}
	defer func() { SignFn = nil }()

	req1, _ := http.NewRequest(http.MethodPost, "http://test.local", nil)
	req2, _ := http.NewRequest(http.MethodPost, "http://test.local", nil)

	_ = signRequest(req1, []byte(`{"body": "one"}`))
	_ = signRequest(req2, []byte(`{"body": "two"}`))

	sig1 := req1.Header.Get("X-Signature")
	sig2 := req2.Header.Get("X-Signature")

	require.NotEqual(t, sig1, sig2, "Different bodies should produce different signatures")
}

func TestSignRequest_ReturnsErrorOnSignFailure(t *testing.T) {
	SignFn = func(msg []byte) ([]byte, error) {
		return nil, errors.New("signing failed")
	}
	defer func() { SignFn = nil }()

	req, _ := http.NewRequest(http.MethodPost, "http://test.local", nil)
	err := signRequest(req, []byte(`{"test": "data"}`))

	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to sign request")
}

func TestMarshalBody_NilPayload(t *testing.T) {
	body, err := marshalBody(nil)
	require.NoError(t, err)
	require.Equal(t, []byte{}, body, "nil payload should produce empty body")
}

func TestMarshalBody_WithPayload(t *testing.T) {
	payload := map[string]string{"key": "value"}
	body, err := marshalBody(payload)
	require.NoError(t, err)
	require.Equal(t, `{"key":"value"}`, string(body))
}

func TestMarshalBody_InvalidPayload(t *testing.T) {
	ch := make(chan int)
	_, err := marshalBody(ch)
	require.Error(t, err)
}

func TestCosmosSDK_InternalHashing(t *testing.T) {
	privKey := secp256k1.GenPrivKey()
	pubKey := privKey.PubKey()

	body := []byte(`{"test": "data"}`)
	sig, err := privKey.Sign(body)
	require.NoError(t, err)

	require.True(t, pubKey.VerifySignature(body, sig))
	require.False(t, pubKey.VerifySignature([]byte(`{"test": "other"}`), sig))
}

func TestSignature_EmptyBodyVerifiable(t *testing.T) {
	privKey := secp256k1.GenPrivKey()
	pubKey := privKey.PubKey()
	SignFn = func(msg []byte) ([]byte, error) {
		return privKey.Sign(msg)
	}
	defer func() { SignFn = nil }()

	req, _ := http.NewRequest(http.MethodGet, "http://test.local/api/v1/status", nil)
	err := signRequest(req, []byte{})
	require.NoError(t, err)

	sigB64 := req.Header.Get("X-Signature")
	sigBytes, _ := base64.StdEncoding.DecodeString(sigB64)

	require.True(t, pubKey.VerifySignature([]byte{}, sigBytes))
}
