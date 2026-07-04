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
		if r.URL.Path != "/productscience/inference/inference/devshard_escrow/42" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"escrow": map[string]any{
				"id":                  "42",
				"creator":             "inference1abc",
				"amount":              "5000000000",
				"slots":               []string{"valA", "valB", "valC"},
				"epoch_index":         "10",
				"app_hash":            "deadbeef",
				"settled":             false,
				"token_price":         "1",
				"create_devshard_fee": "10000",
				"fee_per_nonce":       "1000",
				"inference_seal_grace_nonces":  160,
				"inference_seal_grace_seconds": 3600,
				"auto_seal_every_n_nonces":     150,
				"validation_rate":              6000,
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
	assert.Equal(t, uint64(10_000), info.CreateDevshardFee)
	assert.Equal(t, uint64(1_000), info.FeePerNonce)
	assert.Equal(t, uint32(160), info.InferenceSealGraceNonces)
	assert.Equal(t, uint32(3600), info.InferenceSealGraceSeconds)
	assert.Equal(t, uint32(150), info.AutoSealEveryNNonces)
	assert.Equal(t, uint32(6000), info.ValidationRate)
}

func TestGetEscrow_GraceFieldsNumeric(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"escrow": map[string]any{
				"id": "42", "creator": "c", "amount": "1", "slots": []string{"a"},
				"epoch_index": "0", "app_hash": "aa", "settled": false, "token_price": "1",
				"inference_seal_grace_nonces":  2,
				"inference_seal_grace_seconds": 10,
			},
			"found": true,
		})
	}))
	defer srv.Close()

	info, err := NewRESTBridge(srv.URL).GetEscrow("42")
	require.NoError(t, err)
	assert.Equal(t, uint32(2), info.InferenceSealGraceNonces)
	assert.Equal(t, uint32(10), info.InferenceSealGraceSeconds)
}

func TestGetEscrow_FeesMissingKeysDecodeZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/productscience/inference/inference/devshard_escrow/42":
			json.NewEncoder(w).Encode(map[string]any{
				"escrow": map[string]any{
					"id": "42", "creator": "c", "amount": "1", "slots": []string{"a"},
					"epoch_index": "0", "app_hash": "aa", "settled": false, "token_price": "1",
				},
				"found": true,
			})
		case "/productscience/inference/inference/params":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	info, err := NewRESTBridge(srv.URL).GetEscrow("42")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), info.CreateDevshardFee)
	assert.Equal(t, uint64(0), info.FeePerNonce)
	assert.Equal(t, uint32(0), info.ValidationRate, "missing validation_rate decodes as zero (unset)")
}

func TestGetEscrow_DoesNotQueryParams(t *testing.T) {
	var paramsCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/productscience/inference/inference/devshard_escrow/42":
			json.NewEncoder(w).Encode(map[string]any{
				"escrow": map[string]any{
					"id": "42", "creator": "c", "amount": "1", "slots": []string{"a"},
					"epoch_index": "0", "app_hash": "aa", "settled": false, "token_price": "1",
				},
				"found": true,
			})
		case "/productscience/inference/inference/params":
			paramsCalls++
			t.Fatal("GetEscrow must not query chain params")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	_, err := NewRESTBridge(srv.URL).GetEscrow("42")
	require.NoError(t, err)
	require.Equal(t, 0, paramsCalls)
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

func TestGetValidationThreshold_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/productscience/inference/inference/epoch_group_data/257", r.URL.Path)
		assert.Equal(t, "Qwen/Qwen3", r.URL.Query().Get("model_id"))
		json.NewEncoder(w).Encode(map[string]any{
			"epoch_group_data": map[string]any{
				"model_snapshot": map[string]any{
					"validation_threshold": map[string]any{
						"value":    "958",
						"exponent": -3,
					},
				},
			},
		})
	}))
	defer srv.Close()

	b := NewRESTBridge(srv.URL)
	threshold, err := b.GetValidationThreshold(257, "Qwen/Qwen3")
	require.NoError(t, err)

	assert.Equal(t, int64(958), threshold.Value)
	assert.Equal(t, int32(-3), threshold.Exponent)
}

func TestGetValidationThreshold_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"epoch_group_data": map[string]any{},
		})
	}))
	defer srv.Close()

	b := NewRESTBridge(srv.URL)
	_, err := b.GetValidationThreshold(257, "missing")
	require.Error(t, err)
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

func TestGetSessionBindParams_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/productscience/inference/inference/params", r.URL.Path)
		json.NewEncoder(w).Encode(map[string]any{
			"params": map[string]any{
				"devshard_escrow_params": map[string]any{
					"refusal_timeout":       "60",
					"execution_timeout":     "1200",
					"validation_rate":       0,
					"vote_threshold_factor": 50,
				},
			},
		})
	}))
	defer srv.Close()

	live, err := NewRESTBridge(srv.URL).GetSessionBindParams()
	require.NoError(t, err)
	assert.Equal(t, uint32(0), live.ValidationRate)
	assert.Equal(t, int64(60), live.RefusalTimeout)
	assert.Equal(t, int64(1200), live.ExecutionTimeout)
	assert.Equal(t, uint32(50), live.VoteThresholdFactor)
}

func TestGetSessionBindParams_MissingDevshardParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"params": map[string]any{}})
	}))
	defer srv.Close()

	_, err := NewRESTBridge(srv.URL).GetSessionBindParams()
	require.Error(t, err)
}
