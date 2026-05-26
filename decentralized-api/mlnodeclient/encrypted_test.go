package mlnodeclient

import (
	"context"
	"decentralized-api/encclient"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetEncryptedIdentity_HappyPath(t *testing.T) {
	publicKey := make([]byte, 32)
	for i := range publicKey {
		publicKey[i] = byte(i)
	}
	quote := []byte("fake-tdx-quote-bytes-32+bytes-here-for-testing")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, encryptedIdentityPath, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(encryptedIdentityWire{
			Provider:        "intel-tdx",
			EnvelopeVersion: "v1",
			PublicKey:       base64.StdEncoding.EncodeToString(publicKey),
			Quote:           base64.StdEncoding.EncodeToString(quote),
			Measurement:     "deadbeef",
			Extra:           map[string]any{"firmware": "1.2.3"},
		})
	}))
	defer srv.Close()

	client := NewNodeClient(srv.URL, srv.URL)
	client.client.Timeout = 2 * time.Second
	identity, err := client.GetEncryptedIdentity(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "intel-tdx", identity.Provider)
	assert.Equal(t, "v1", identity.EnvelopeVersion)
	assert.Equal(t, publicKey, identity.PublicKey)
	assert.Equal(t, quote, identity.Quote)
	assert.Equal(t, "deadbeef", identity.Measurement)
	assert.Equal(t, "1.2.3", identity.Extra["firmware"])

	assert.Equal(t, encclient.RecipientKeyID(publicKey), identity.KeyID)
}

func TestGetEncryptedIdentity_RejectsForgedKeyIDHint(t *testing.T) {

	publicKey := make([]byte, 32)
	for i := range publicKey {
		publicKey[i] = byte(i)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(encryptedIdentityWire{
			Provider:        "intel-tdx-lite",
			EnvelopeVersion: "v1",
			PublicKey:       base64.StdEncoding.EncodeToString(publicKey),
			KeyID:           "deadbeefdeadbeef",
		})
	}))
	defer srv.Close()

	client := NewNodeClient(srv.URL, srv.URL)
	client.client.Timeout = 2 * time.Second
	_, err := client.GetEncryptedIdentity(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match sha256(public_key)")
}

func TestGetEncryptedIdentity_NilQuoteOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"provider":"intel-tdx-lite","envelope_version":"v1","public_key":"` +
			base64.StdEncoding.EncodeToString(make([]byte, 32)) + `"}`))
	}))
	defer srv.Close()

	client := NewNodeClient(srv.URL, srv.URL)
	client.client.Timeout = 2 * time.Second
	identity, err := client.GetEncryptedIdentity(context.Background())
	require.NoError(t, err)
	assert.Nil(t, identity.Quote)
}

func TestGetEncryptedIdentity_DefaultsEnvelopeVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"provider":"intel-tdx-lite","public_key":"` + base64.StdEncoding.EncodeToString(make([]byte, 32)) + `"}`))
	}))
	defer srv.Close()

	client := NewNodeClient(srv.URL, srv.URL)
	client.client.Timeout = 2 * time.Second
	identity, err := client.GetEncryptedIdentity(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "v1", identity.EnvelopeVersion)
}

func TestGetEncryptedIdentity_ReturnsDisabledOn503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "disabled", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := NewNodeClient(srv.URL, srv.URL)
	client.client.Timeout = 2 * time.Second
	_, err := client.GetEncryptedIdentity(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEncryptedIdentityDisabled))
}

func TestGetEncryptedIdentity_PropagatesOtherStatuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewNodeClient(srv.URL, srv.URL)
	client.client.Timeout = 2 * time.Second
	_, err := client.GetEncryptedIdentity(context.Background())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "status 500"))
	assert.False(t, errors.Is(err, ErrEncryptedIdentityDisabled))
}

func TestGetEncryptedIdentity_RejectsBadBase64(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"provider":"intel-tdx-lite","public_key":"!!!not-base64!!!"}`))
	}))
	defer srv.Close()

	client := NewNodeClient(srv.URL, srv.URL)
	client.client.Timeout = 2 * time.Second
	_, err := client.GetEncryptedIdentity(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode public_key base64")
}
