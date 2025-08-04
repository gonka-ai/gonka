package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/spf13/cobra"
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
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKeyBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode public key: %w", err)
	}

	pubKey := &secp256k1.PubKey{Key: pubKeyBytes}
	return sdk.AccAddress(pubKey.Address()).String(), nil
}

func RegisterNewParticipantCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register-new-participant <node-url> <account-public-key> <consensus-key>",
		Short: "Register a new participant with the seed node",
		Long: `Register a new participant with the seed node.

The account address will be automatically derived from the account public key.
All communication happens via HTTP API calls to the seed node.

Arguments:
  node-url                   Your node's public URL (e.g., http://my-node:8080)
  account-public-key         Base64-encoded account public key (from keyring output)
  consensus-key    Base64-encoded validator consensus public key (from node status)

Example:
  inferenced register-new-participant \
    http://my-node:8080 \
    "Au+a3CpMj6nqFV6d0tUlVajCTkOP3cxKnps+1/lMv5zY" \
    "x+OH2yt/GC/zK/fR5ImKnlfrmE6nZO/11FKXOpWRmAA=" \
    --node-address http://195.242.13.239:8000`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeAddress, err := cmd.Flags().GetString(NodeAddress)
			if err != nil {
				return err
			}
			if strings.TrimSpace(nodeAddress) == "" {
				return errors.New("node address is required (use --node-address flag)")
			}

			nodeUrl := args[0]
			accountPubKey := args[1]
			validatorConsensusKey := args[2]

			accountAddress, err := extractAddressFromPubKey(accountPubKey)
			if err != nil {
				return fmt.Errorf("failed to extract address from account public key: %w", err)
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
			cmd.Printf("  Validator Consensus Key: %s\n", validatorConsensusKey)
			cmd.Printf("  Seed Node Address: %s\n", nodeAddress)

			return sendRegisterNewParticipantRequest(cmd, nodeAddress, &requestBody)
		},
	}

	cmd.Flags().String(NodeAddress, "", "Seed node address to send the request to. Example: http://195.242.13.239:8000")
	cmd.MarkFlagRequired(NodeAddress)

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
