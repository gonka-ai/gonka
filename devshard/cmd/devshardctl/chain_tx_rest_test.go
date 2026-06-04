package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"devshard/signing"

	"github.com/stretchr/testify/require"
)

func TestRESTChainTxClient_CreateDevshardEscrow(t *testing.T) {
	signer, err := signing.GenerateKey()
	require.NoError(t, err)

	var broadcastSeen bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cosmos/auth/v1beta1/accounts/" + signer.Address():
			writeTestJSON(t, w, map[string]any{
				"account": map[string]any{
					"@type":          "/cosmos.auth.v1beta1.BaseAccount",
					"address":        signer.Address(),
					"account_number": "7",
					"sequence":       "11",
				},
			})
		case "/cosmos/tx/v1beta1/txs":
			require.Equal(t, http.MethodPost, r.Method)
			var req struct {
				TxBytes string `json:"tx_bytes"`
				Mode    string `json:"mode"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.Equal(t, "BROADCAST_MODE_SYNC", req.Mode)
			txBytes, err := base64.StdEncoding.DecodeString(req.TxBytes)
			require.NoError(t, err)
			require.NotEmpty(t, txBytes)
			assertUnorderedTx(t, txBytes)
			broadcastSeen = true
			writeTestJSON(t, w, map[string]any{
				"tx_response": map[string]any{
					"code":   0,
					"txhash": "ABC123",
				},
			})
		case "/cosmos/tx/v1beta1/txs/ABC123":
			require.True(t, broadcastSeen)
			writeTestJSON(t, w, map[string]any{
				"tx_response": map[string]any{
					"code":   0,
					"txhash": "ABC123",
					"events": []map[string]any{{
						"type": "devshard_escrow_created",
						"attributes": []map[string]string{{
							"key":   "escrow_id",
							"value": "42",
						}},
					}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewRESTChainTxClient(RESTChainTxConfig{
		BaseURL:      server.URL,
		ChainID:      "gonka-test",
		FeeAmount:    123,
		GasLimit:     456,
		PollInterval: time.Millisecond,
		PollTimeout:  time.Second,
	})
	require.NoError(t, err)

	result, err := client.CreateDevshardEscrow(t.Context(), signer, 1_000_000, "Qwen/Test")
	require.NoError(t, err)
	require.Equal(t, uint64(42), result.EscrowID)
	require.Equal(t, "ABC123", result.TxHash)
	require.Equal(t, signer.Address(), result.Creator)
}

func TestRESTChainTxClient_CreateDevshardEscrowUsesTxQueryFallback(t *testing.T) {
	signer, err := signing.GenerateKey()
	require.NoError(t, err)

	var broadcastSeen bool
	broadcastServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cosmos/auth/v1beta1/accounts/" + signer.Address():
			writeTestJSON(t, w, map[string]any{
				"account": map[string]any{
					"@type":          "/cosmos.auth.v1beta1.BaseAccount",
					"address":        signer.Address(),
					"account_number": "7",
					"sequence":       "11",
				},
			})
		case "/cosmos/tx/v1beta1/txs":
			require.Equal(t, http.MethodPost, r.Method)
			var req struct {
				TxBytes string `json:"tx_bytes"`
				Mode    string `json:"mode"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.Equal(t, "BROADCAST_MODE_SYNC", req.Mode)
			txBytes, err := base64.StdEncoding.DecodeString(req.TxBytes)
			require.NoError(t, err)
			assertUnorderedTx(t, txBytes)
			broadcastSeen = true
			writeTestJSON(t, w, map[string]any{
				"tx_response": map[string]any{
					"code":   0,
					"txhash": "FALLBACK123",
				},
			})
		case "/cosmos/tx/v1beta1/txs/FALLBACK123":
			http.Error(w, `{"code":2,"message":"transaction indexing is disabled"}`, http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer broadcastServer.Close()

	queryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/cosmos/tx/v1beta1/txs/FALLBACK123", r.URL.Path)
		require.True(t, broadcastSeen)
		writeTestJSON(t, w, map[string]any{
			"tx_response": map[string]any{
				"code":   0,
				"txhash": "FALLBACK123",
				"events": []map[string]any{{
					"type": "devshard_escrow_created",
					"attributes": []map[string]string{{
						"key":   "escrow_id",
						"value": "43",
					}},
				}},
			},
		})
	}))
	defer queryServer.Close()

	client, err := NewRESTChainTxClient(RESTChainTxConfig{
		BaseURL:      broadcastServer.URL,
		TxQueryURL:   queryServer.URL,
		ChainID:      "gonka-test",
		FeeAmount:    123,
		GasLimit:     456,
		PollInterval: time.Millisecond,
		PollTimeout:  time.Second,
	})
	require.NoError(t, err)

	result, err := client.CreateDevshardEscrow(t.Context(), signer, 1_000_000, "Qwen/Test")
	require.NoError(t, err)
	require.Equal(t, uint64(43), result.EscrowID)
	require.Equal(t, "FALLBACK123", result.TxHash)
	require.Equal(t, signer.Address(), result.Creator)
}

func TestRESTChainTxClient_SettleDevshardEscrow(t *testing.T) {
	signer, err := signing.GenerateKey()
	require.NoError(t, err)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cosmos/auth/v1beta1/accounts/" + signer.Address():
			writeTestJSON(t, w, map[string]any{
				"account": map[string]any{
					"@type":          "/cosmos.auth.v1beta1.BaseAccount",
					"address":        signer.Address(),
					"account_number": "8",
					"sequence":       "12",
				},
			})
		case "/cosmos/tx/v1beta1/txs":
			require.Equal(t, http.MethodPost, r.Method)
			var req struct {
				TxBytes string `json:"tx_bytes"`
				Mode    string `json:"mode"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.Equal(t, "BROADCAST_MODE_SYNC", req.Mode)
			txBytes, err := base64.StdEncoding.DecodeString(req.TxBytes)
			require.NoError(t, err)
			require.NotEmpty(t, txBytes)
			assertUnorderedTx(t, txBytes)
			writeTestJSON(t, w, map[string]any{
				"tx_response": map[string]any{
					"code":   0,
					"txhash": "DEF456",
				},
			})
		case "/cosmos/tx/v1beta1/txs/DEF456":
			t.Fatalf("settlement should not depend on tx indexing")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewRESTChainTxClient(RESTChainTxConfig{
		BaseURL:      server.URL,
		ChainID:      "gonka-test",
		FeeAmount:    123,
		GasLimit:     456,
		PollInterval: time.Millisecond,
		PollTimeout:  time.Second,
	})
	require.NoError(t, err)

	settlement := SettlementJSON{
		EscrowID:  "42",
		Version:   "v1",
		StateRoot: base64.StdEncoding.EncodeToString([]byte("state-root")),
		Nonce:     17,
		Fees:      99,
		RestHash:  base64.StdEncoding.EncodeToString([]byte("rest-hash")),
		HostStats: []HostStatsJSON{{
			SlotID:               1,
			Missed:               2,
			Invalid:              3,
			Cost:                 4,
			RequiredValidations:  5,
			CompletedValidations: 6,
		}},
		Signatures: []SlotSignatureJSON{{
			SlotID:    1,
			Signature: base64.StdEncoding.EncodeToString([]byte("signature")),
		}},
	}
	result, err := client.SettleDevshardEscrow(t.Context(), signer, settlement)
	require.NoError(t, err)
	require.Equal(t, uint64(42), result.EscrowID)
	require.Equal(t, "DEF456", result.TxHash)
	require.Equal(t, signer.Address(), result.Settler)
}

func TestFindUintFieldNestedAccount(t *testing.T) {
	payload := map[string]any{
		"account": map[string]any{
			"base_vesting_account": map[string]any{
				"base_account": map[string]any{
					"account_number": "9",
					"sequence":       "13",
				},
			},
		},
	}
	accountNumber, ok := findUintField(payload, "account_number")
	require.True(t, ok)
	require.Equal(t, uint64(9), accountNumber)
	sequence, ok := findUintField(payload, "sequence")
	require.True(t, ok)
	require.Equal(t, uint64(13), sequence)
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(value))
}

func assertUnorderedTx(t *testing.T, txBytes []byte) {
	t.Helper()
	txRaw := mustProtoFields(t, txBytes)
	body := mustFieldBytes(t, txRaw, 1)
	authInfo := mustFieldBytes(t, txRaw, 2)

	bodyFields := mustProtoFields(t, body)
	require.Equal(t, uint64(1), mustFieldVarint(t, bodyFields, 4), "tx body must set unordered=true")
	require.NotEmpty(t, mustFieldBytes(t, bodyFields, 5), "tx body must set timeout_timestamp")

	authFields := mustProtoFields(t, authInfo)
	signerInfo := mustFieldBytes(t, authFields, 1)
	signerFields := mustProtoFields(t, signerInfo)
	require.Equal(t, uint64(0), mustFieldVarint(t, signerFields, 3), "unordered tx signer sequence must be 0")
}

type protoField struct {
	number uint64
	wire   uint64
	varint uint64
	bytes  []byte
}

func mustProtoFields(t *testing.T, data []byte) []protoField {
	t.Helper()
	fields, err := parseProtoFields(data)
	require.NoError(t, err)
	return fields
}

func parseProtoFields(data []byte) ([]protoField, error) {
	var fields []protoField
	for len(data) > 0 {
		key, n, err := consumeVarint(data)
		if err != nil {
			return nil, err
		}
		data = data[n:]
		field := protoField{number: key >> 3, wire: key & 0x7}
		switch field.wire {
		case 0:
			value, n, err := consumeVarint(data)
			if err != nil {
				return nil, err
			}
			field.varint = value
			data = data[n:]
		case 2:
			length, n, err := consumeVarint(data)
			if err != nil {
				return nil, err
			}
			data = data[n:]
			if uint64(len(data)) < length {
				return nil, io.ErrUnexpectedEOF
			}
			field.bytes = data[:length]
			data = data[length:]
		default:
			return nil, fmt.Errorf("unsupported wire type %d", field.wire)
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func consumeVarint(data []byte) (uint64, int, error) {
	var value uint64
	for i, b := range data {
		if i == 10 {
			return 0, 0, fmt.Errorf("varint too long")
		}
		value |= uint64(b&0x7f) << (7 * i)
		if b < 0x80 {
			return value, i + 1, nil
		}
	}
	return 0, 0, io.ErrUnexpectedEOF
}

func mustFieldBytes(t *testing.T, fields []protoField, number uint64) []byte {
	t.Helper()
	for _, field := range fields {
		if field.number == number {
			require.Equal(t, uint64(2), field.wire)
			return field.bytes
		}
	}
	t.Fatalf("field %d not found", number)
	return nil
}

func mustFieldVarint(t *testing.T, fields []protoField, number uint64) uint64 {
	t.Helper()
	for _, field := range fields {
		if field.number == number {
			require.Equal(t, uint64(0), field.wire)
			return field.varint
		}
	}
	t.Fatalf("field %d not found", number)
	return 0
}
