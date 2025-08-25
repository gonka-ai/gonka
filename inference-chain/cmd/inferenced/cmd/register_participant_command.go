package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/spf13/cobra"

	"github.com/productscience/inference/x/inference/utils"
)

type RegisterParticipantDto struct {
	Address      string `json:"address"`
	Url          string `json:"url"`
	ValidatorKey string `json:"validator_key"`
	PubKey       string `json:"pub_key"`
	WorkerKey    string `json:"worker_key"`
}

type InferenceParticipantResponse struct {
	Pubkey  string `json:"pubkey"`
	Balance int64  `json:"balance"`
}

// extractAddressFromPubKey derives a cosmos address from a base64-encoded public key
func extractAddressFromPubKey(pubKeyBase64 string) (string, error) {
	if strings.TrimSpace(pubKeyBase64) == "" {
		return "", fmt.Errorf("public key cannot be empty")
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKeyBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode public key: %w", err)
	}

	if len(pubKeyBytes) == 0 {
		return "", fmt.Errorf("decoded public key is empty")
	}

	pubKey := &secp256k1.PubKey{Key: pubKeyBytes}
	return sdk.AccAddress(pubKey.Address()).String(), nil
}

// fetchConsensusKeyFromNode retrieves the validator consensus public key from the chain node's RPC status endpoint
// Uses DAPI_CHAIN_NODE__URL environment variable (automatically set in api/node containers)
// Returns the key as a base64-encoded string, consistent with the expected format
func fetchConsensusKeyFromNode() (string, error) {
	// Get chain node URL from environment variable (automatically set in api/node containers)
	chainNodeUrl := os.Getenv("DAPI_CHAIN_NODE__URL")
	if chainNodeUrl == "" {
		return "", fmt.Errorf("DAPI_CHAIN_NODE__URL environment variable is not set. Auto-fetch is only available when running from api/node containers. Please provide consensus key manually using --consensus-key flag")
	}

	// Create RPC client with connection timeout
	client, err := rpcclient.New(chainNodeUrl, "/websocket")
	if err != nil {
		return "", fmt.Errorf("failed to connect to chain node at %s. Please verify the node is running and accessible: %w", chainNodeUrl, err)
	}

	// Create context with timeout for the RPC call
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get validator status from the node
	result, err := client.Status(ctx)
	if err != nil {
		return "", fmt.Errorf("node RPC endpoint not responding. Please check if the node is properly configured: %w", err)
	}

	// Check if validator info is available
	if result.ValidatorInfo.PubKey == nil {
		return "", fmt.Errorf("node does not have validator information. Please provide consensus key manually using --consensus-key flag")
	}

	// Extract the validator public key
	validatorKey := result.ValidatorInfo.PubKey

	// Get the raw bytes directly from the PubKey
	keyBytes := validatorKey.Bytes()
	if len(keyBytes) == 0 {
		return "", fmt.Errorf("received invalid response from node status endpoint: validator key is empty")
	}

	// Encode as base64 string
	consensusKeyBase64 := base64.StdEncoding.EncodeToString(keyBytes)

	return consensusKeyBase64, nil
}

func RegisterNewParticipantCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register-new-participant <node-url> <account-public-key>",
		Short: "Register a new participant with the seed node",
		Long: `Register a new participant with the seed node.

The account address will be automatically derived from the account public key.
All communication happens via HTTP API calls to the seed node.

The consensus key can be provided explicitly using the --consensus-key flag, or it will be
automatically fetched from the chain node when running from within api containers.

Arguments:
  node-url                   Your node's public URL (e.g., http://my-node:8080)
  account-public-key         Base64-encoded account public key (from keyring output)

Flags:
  --node-address             Seed node address for participant registration (required)
  --consensus-key            Base64-encoded validator consensus public key (optional)
                            If not provided, will be auto-fetched from chain node

Environment Variables:
  DAPI_CHAIN_NODE__URL       Chain node RPC URL (automatically set in api containers)

Troubleshooting:
  - Auto-fetch fails: Ensure DAPI_CHAIN_NODE__URL is set and chain node is running
  - Connection errors: Verify node accessibility and proper configuration
  - Invalid key errors: Use --consensus-key flag to provide key manually
  - Missing validator info: Node may not be configured as validator, use manual key
  
Examples:
  # Auto-fetch consensus key from 'api' container (requires 'node' container running and DAPI_CHAIN_NODE__URL set):
  # DAPI_CHAIN_NODE__URL is automatically set when containers are created
  inferenced register-new-participant \
    http://my-node:8080 \
    "Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY" \
    --node-address http://node2.gonka.ai:8000

  # Provide explicit consensus key (for manual/external usage):
  inferenced register-new-participant \
    http://my-node:8080 \
    "Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY" \
    --consensus-key "x+OH2yt/GC/zK/fR5ImKnlfrmE6nZO/11FKXOpWRmAA=" \
    --node-address http://node2.gonka.ai:8000`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeAddress, err := cmd.Flags().GetString(NodeAddress)
			if err != nil {
				return err
			}
			if strings.TrimSpace(nodeAddress) == "" {
				return errors.New("node address is required (use --node-address flag)")
			}

			// Get consensus key from flag (optional)
			consensusKey, err := cmd.Flags().GetString("consensus-key")
			if err != nil {
				return err
			}

			nodeUrl := args[0]
			accountPubKey := args[1]

			accountAddress, err := extractAddressFromPubKey(accountPubKey)
			if err != nil {
				return fmt.Errorf("failed to extract address from account public key: %w", err)
			}

			// Task 4.3: Implement conditional consensus key logic
			var validatorConsensusKey string
			var keySource string

			if strings.TrimSpace(consensusKey) != "" {
				// Use provided consensus key (explicit mode)
				validatorConsensusKey = strings.TrimSpace(consensusKey)
				keySource = "provided"

				// Validate the provided consensus key
				_, err := utils.SafeCreateED25519ValidatorKey(validatorConsensusKey)
				if err != nil {
					return fmt.Errorf("invalid consensus key provided: %w", err)
				}

				cmd.Printf("Using provided consensus key (validated)\n")
			} else {
				// Auto-fetch consensus key from chain node (api/node containers only)
				keySource = "auto-fetched"
				cmd.Printf("No consensus key provided, attempting to auto-fetch from chain node...\n")

				fetchedKey, err := fetchConsensusKeyFromNode()
				if err != nil {
					return fmt.Errorf("failed to auto-fetch consensus key: %w", err)
				}

				// Validate the fetched consensus key
				_, err = utils.SafeCreateED25519ValidatorKey(fetchedKey)
				if err != nil {
					return fmt.Errorf("auto-fetched consensus key is invalid: %w. Please provide a valid consensus key manually using --consensus-key flag", err)
				}

				validatorConsensusKey = fetchedKey
				cmd.Printf("Successfully auto-fetched and validated consensus key from chain node\n")
			}

			requestBody := RegisterParticipantDto{
				Address:      accountAddress,
				Url:          nodeUrl,
				ValidatorKey: validatorConsensusKey,
				PubKey:       accountPubKey,
				WorkerKey:    "",
			}

			cmd.Printf("Registering new participant:\n")
			cmd.Printf("  Node URL: %s\n", nodeUrl)
			cmd.Printf("  Account Address: %s\n", accountAddress)
			cmd.Printf("  Account Public Key: %s\n", accountPubKey)
			cmd.Printf("  Validator Consensus Key: %s (%s)\n", validatorConsensusKey, keySource)
			cmd.Printf("  Seed Node Address: %s\n", nodeAddress)

			return sendRegisterNewParticipantRequest(cmd, nodeAddress, &requestBody)
		},
	}

	cmd.Flags().String(NodeAddress, "", "Seed node address to send the request to. Example: http://node2.gonka.ai:8000")
	cmd.MarkFlagRequired(NodeAddress)

	cmd.Flags().String("consensus-key", "", "Base64-encoded validator consensus public key (optional). If not provided, will be auto-fetched from node status endpoint")

	return cmd
}

func sendRegisterNewParticipantRequest(cmd *cobra.Command, nodeAddress string, body *RegisterParticipantDto) error {
	jsonData, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := strings.TrimRight(nodeAddress, "/") + "/v1/participants"
	cmd.Printf("Sending registration request to %s\n", url)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	cmd.Printf("Response status code: %d\n", resp.StatusCode)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("server returned status %d and failed to read response body", resp.StatusCode)
		}
		return fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	cmd.Printf("Participant registration successful.\n")
	cmd.Printf("Waiting for participant to be available (timeout: 30 seconds)...\n")

	participantURL := fmt.Sprintf("%s/v1/participants/%s", strings.TrimRight(nodeAddress, "/"), body.Address)
	if err := waitForParticipantAvailable(cmd, participantURL, 30*time.Second); err != nil {
		cmd.Printf("Warning: %v\n", err)
		cmd.Printf("You can manually check your participant at %s\n", participantURL)
	} else {
		cmd.Printf("Participant is now available at %s\n", participantURL)
	}

	return nil
}

// waitForParticipantAvailable polls the participant endpoint until it's available or timeout is reached
func waitForParticipantAvailable(cmd *cobra.Command, participantURL string, timeout time.Duration) error {
	httpClient := &http.Client{
		Timeout: 5 * time.Second, // 5 second timeout per request
	}

	ticker := time.NewTicker(2 * time.Second) // Check every 2 seconds
	defer ticker.Stop()

	timeoutCh := time.After(timeout)

	for {
		select {
		case <-timeoutCh:
			return fmt.Errorf("timeout after %v waiting for participant to be available", timeout)

		case <-ticker.C:
			cmd.Printf(".")

			resp, err := httpClient.Get(participantURL)
			if err != nil {
				continue
			}

			if resp.StatusCode == http.StatusOK {
				bodyBytes, err := io.ReadAll(resp.Body)
				resp.Body.Close()

				if err != nil {
					continue
				}

				var participant InferenceParticipantResponse
				if err := json.Unmarshal(bodyBytes, &participant); err != nil {
					continue
				}

				if participant.Pubkey != "" {
					cmd.Printf("\n")
					cmd.Printf("Found participant with pubkey: %s (balance: %d)\n", participant.Pubkey, participant.Balance)
					return nil
				}
			} else {
				resp.Body.Close()
			}

		}
	}
}
