package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetEscrow_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/productscience/inference/inference/subnet_escrow/42", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"escrow": map[string]any{
				"id":          "42",
				"creator":     "inference1abc",
				"amount":      "5000000000",
				"slots":       []string{"valA", "valB", "valC"},
				"epoch_index": "10",
				"app_hash":    "deadbeef",
				"settled":     false,
			},
			"found": true,
		})
	}))
	defer srv.Close()

	b := NewRESTBridge(srv.URL)
	info, err := b.GetEscrow("42")
	require.NoError(t, err)

	assert.Equal(t, "42", info.EscrowID)
	assert.Equal(t, uint64(5_000_000_000), info.Amount)
	assert.Equal(t, "inference1abc", info.CreatorAddress)
	assert.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, info.AppHash)
	assert.Equal(t, []string{"valA", "valB", "valC"}, info.Slots)
}

func TestGetEscrow_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"escrow": map[string]any{},
			"found":  false,
		})
	}))
	defer srv.Close()

	b := NewRESTBridge(srv.URL)
	_, err := b.GetEscrow("999")
	assert.ErrorIs(t, err, ErrEscrowNotFound)
}

func TestGetHostInfo_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/productscience/inference/inference/participant/valA", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"participant": map[string]any{
				"index":         "valA",
				"address":       "inference1valA",
				"weight":        100,
				"inference_url": "http://ml.example.com:8080",
				"validator_key": "AQID",
			},
		})
	}))
	defer srv.Close()

	b := NewRESTBridge(srv.URL)
	info, err := b.GetHostInfo("valA")
	require.NoError(t, err)

	assert.Equal(t, "inference1valA", info.Address)
	assert.Equal(t, "http://ml.example.com:8080", info.URL)
}

func TestGetHostInfo_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	b := NewRESTBridge(srv.URL)
	_, err := b.GetHostInfo("missing")
	assert.ErrorIs(t, err, ErrParticipantNotFound)
}

func TestVerifyWarmKey_Authorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"grantees": []map[string]string{
				{"address": "inference1warm1", "pub_key": ""},
				{"address": "inference1warm2", "pub_key": ""},
			},
		})
	}))
	defer srv.Close()

	b := NewRESTBridge(srv.URL)
	ok, err := b.VerifyWarmKey("inference1warm2", "valA")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestVerifyWarmKey_NotAuthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"grantees": []map[string]string{
				{"address": "inference1other", "pub_key": ""},
			},
		})
	}))
	defer srv.Close()

	b := NewRESTBridge(srv.URL)
	ok, err := b.VerifyWarmKey("inference1warm2", "valA")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestVerifyWarmKey_EmptyGrantees(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"grantees": []map[string]string{},
		})
	}))
	defer srv.Close()

	b := NewRESTBridge(srv.URL)
	ok, err := b.VerifyWarmKey("inference1warm", "valA")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestVerifyWarmKey_CachesResult(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{
			"grantees": []map[string]string{
				{"address": "inference1warm1", "pub_key": ""},
			},
		})
	}))
	defer srv.Close()

	b := NewRESTBridge(srv.URL)

	ok1, err := b.VerifyWarmKey("inference1warm1", "valA")
	require.NoError(t, err)
	assert.True(t, ok1)

	ok2, err := b.VerifyWarmKey("inference1warm1", "valA")
	require.NoError(t, err)
	assert.True(t, ok2)

	assert.Equal(t, 1, calls, "second call should hit cache")
}

func TestStubMethods_ReturnNotImplemented(t *testing.T) {
	b := NewRESTBridge("http://unused")

	assert.ErrorIs(t, b.OnEscrowCreated(EscrowInfo{}), ErrNotImplemented)
	assert.ErrorIs(t, b.OnSettlementProposed("", nil, 0), ErrNotImplemented)
	assert.ErrorIs(t, b.OnSettlementFinalized(""), ErrNotImplemented)
	assert.ErrorIs(t, b.SubmitDisputeState("", nil, 0, nil), ErrNotImplemented)
}
