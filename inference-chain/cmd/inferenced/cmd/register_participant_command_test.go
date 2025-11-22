package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockParticipantServer creates a mock participant registration server
func mockParticipantServer(t *testing.T, expectedAddress string, shouldSucceed bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/participants" {
			// Handle participant lookup for waiting
			if strings.HasPrefix(r.URL.Path, "/v1/participants/") && r.Method == "GET" {
				if shouldSucceed {
					response := InferenceParticipantResponse{
						Pubkey:  "test-pubkey",
						Balance: 1000,
					}
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(response)
				} else {
					http.NotFound(w, r)
				}
				return
			}
			http.NotFound(w, r)
			return
		}

		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		var body RegisterParticipantDto
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		// Validate expected data
		if body.Address != expectedAddress {
			http.Error(w, fmt.Sprintf("Expected address %s, got %s", expectedAddress, body.Address), http.StatusBadRequest)
			return
		}

		if !shouldSucceed {
			http.Error(w, "Registration failed", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
}

func TestFetchConsensusKeyFromNode_MissingEnvVar(t *testing.T) {
	// Ensure environment variable is not set
	os.Unsetenv("DAPI_CHAIN_NODE__URL")

	_, err := fetchConsensusKeyFromNode()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DAPI_CHAIN_NODE__URL environment variable is not set")
	assert.Contains(t, err.Error(), "Auto-fetch is only available when running from api/node containers")
}

func TestFetchConsensusKeyFromNode_ConnectionError(t *testing.T) {
	// Use invalid URL
	os.Setenv("DAPI_CHAIN_NODE__URL", "http://invalid-host:12345")
	defer os.Unsetenv("DAPI_CHAIN_NODE__URL")

	_, err := fetchConsensusKeyFromNode()
	require.Error(t, err)
	// The actual error format may vary, just check that it's a connection/RPC error
	assert.Contains(t, err.Error(), "node RPC endpoint not responding")
}

func TestRegisterNewParticipantCommand_ExplicitConsensusKey(t *testing.T) {
	// Valid test keys
	validAccountKey := "Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY"   // 33 bytes SECP256K1
	validConsensusKey := "Zo6ZhruBqokt3grulQcgFEfHjmsfNjl5wAtoII1tjzU=" // 32 bytes ED25519

	// Expected address derived from account key
	expectedAddress := "cosmos1rk52j24xj9ej87jas4zqpvjuhrgpnd7hpu77ue"

	mockServer := mockParticipantServer(t, expectedAddress, true)
	defer mockServer.Close()

	cmd := RegisterNewParticipantCommand()
	cmd.SetArgs([]string{
		"http://test-node:8080",
		validAccountKey,
		"--node-address", mockServer.URL,
		"--consensus-key", validConsensusKey,
	})

	// Capture output
	var output strings.Builder
	cmd.SetOut(&output)

	err := cmd.Execute()
	require.NoError(t, err)

	outputStr := output.String()
	assert.Contains(t, outputStr, "Using provided consensus key (validated)")
	assert.Contains(t, outputStr, "(provided)")
	assert.Contains(t, outputStr, "Participant registration successful")
}

func TestRegisterNewParticipantCommand_InvalidProvidedKey(t *testing.T) {
	validAccountKey := "Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY"
	invalidConsensusKey := "AggLJgjYij7iN/qmWohnV5mU7CdcYFGw9qd3NlsvZ28c" // 33 bytes, not 32

	cmd := RegisterNewParticipantCommand()
	cmd.SetArgs([]string{
		"http://test-node:8080",
		validAccountKey,
		"--node-address", "http://mock-server:8000",
		"--consensus-key", invalidConsensusKey,
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid consensus key provided")
	assert.Contains(t, err.Error(), "ED25519 validator key must be exactly 32 bytes")
}

func TestRegisterNewParticipantCommand_AutoFetchFails(t *testing.T) {
	validAccountKey := "Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY"

	// No DAPI_CHAIN_NODE__URL set
	os.Unsetenv("DAPI_CHAIN_NODE__URL")

	cmd := RegisterNewParticipantCommand()
	cmd.SetArgs([]string{
		"http://test-node:8080",
		validAccountKey,
		"--node-address", "http://mock-server:8000",
		// No --consensus-key flag provided
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to auto-fetch consensus key")
	assert.Contains(t, err.Error(), "DAPI_CHAIN_NODE__URL environment variable is not set")
}

func TestRegisterNewParticipantCommand_InvalidAccountKey(t *testing.T) {
	invalidAccountKey := "invalid-base64-key!"
	validConsensusKey := "Zo6ZhruBqokt3grulQcgFEfHjmsfNjl5wAtoII1tjzU="

	cmd := RegisterNewParticipantCommand()
	cmd.SetArgs([]string{
		"http://test-node:8080",
		invalidAccountKey,
		"--node-address", "http://mock-server:8000",
		"--consensus-key", validConsensusKey,
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to extract address from account public key")
}

func TestRegisterNewParticipantCommand_MissingNodeAddress(t *testing.T) {
	validAccountKey := "Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY"
	validConsensusKey := "Zo6ZhruBqokt3grulQcgFEfHjmsfNjl5wAtoII1tjzU="

	cmd := RegisterNewParticipantCommand()
	cmd.SetArgs([]string{
		"http://test-node:8080",
		validAccountKey,
		"--consensus-key", validConsensusKey,
		// Missing --node-address flag
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `required flag(s) "node-address" not set`)
}

func TestRegisterNewParticipantCommand_RegistrationServerError(t *testing.T) {
	validAccountKey := "Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY"
	validConsensusKey := "Zo6ZhruBqokt3grulQcgFEfHjmsfNjl5wAtoII1tjzU="
	expectedAddress := "cosmos1rk52j24xj9ej87jas4zqpvjuhrgpnd7hpu77ue"

	// Mock server that returns error
	mockServer := mockParticipantServer(t, expectedAddress, false)
	defer mockServer.Close()

	cmd := RegisterNewParticipantCommand()
	cmd.SetArgs([]string{
		"http://test-node:8080",
		validAccountKey,
		"--node-address", mockServer.URL,
		"--consensus-key", validConsensusKey,
	})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server returned status 500")
}

// Test helper function for address extraction
func TestExtractAddressFromPubKey(t *testing.T) {
	tests := []struct {
		name        string
		pubKeyB64   string
		expected    string
		expectError bool
	}{
		{
			name:        "valid SECP256K1 key",
			pubKeyB64:   "Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY",
			expected:    "cosmos1rk52j24xj9ej87jas4zqpvjuhrgpnd7hpu77ue",
			expectError: false,
		},
		{
			name:        "invalid base64",
			pubKeyB64:   "invalid-base64!",
			expectError: true,
		},
		{
			name:        "empty key",
			pubKeyB64:   "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			address, err := extractAddressFromPubKey(tt.pubKeyB64)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, address)
			}
		})
	}
}
