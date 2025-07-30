package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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

func RegisterNewParticipantCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register-new-participant [operator-address] [node-url] [operator-public-key] [validator-consensus-key]",
		Short: "Register a new participant with the seed node",
		Long: `Register a new participant with the seed node by sending a request to the specified seed node address.

Example:
  inferenced register-new-participant cosmos1abc... http://my-node:8080 Ahex... valcons1xyz... --node-address http://genesis-node:8080`,
		Args: cobra.ExactArgs(4),
		RunE: func(cmd *cobra.Command, args []string) error {
			nodeAddress, err := cmd.Flags().GetString(NodeAddress)
			if err != nil {
				return err
			}
			if strings.TrimSpace(nodeAddress) == "" {
				return errors.New("node address is required (use --node-address flag)")
			}

			operatorAddress := args[0]
			nodeUrl := args[1]
			operatorPubKey := args[2]
			validatorConsensusKey := args[3]

			// For now, WorkerKey is left empty as mentioned in the todo - "Fully Ignore Worker Key for now"
			requestBody := RegisterParticipantDto{
				Address:      operatorAddress,
				Url:          nodeUrl,
				ValidatorKey: validatorConsensusKey,
				PubKey:       operatorPubKey,
				WorkerKey:    "", // Ignored for now as per todo
			}

			cmd.Printf("Registering new participant:\n")
			cmd.Printf("  Operator Address: %s\n", operatorAddress)
			cmd.Printf("  Node URL: %s\n", nodeUrl)
			cmd.Printf("  Operator Public Key: %s\n", operatorPubKey)
			cmd.Printf("  Validator Consensus Key: %s\n", validatorConsensusKey)
			cmd.Printf("  Seed Node Address: %s\n", nodeAddress)

			return sendRegisterNewParticipantRequest(cmd, nodeAddress, &requestBody)
		},
	}

	cmd.Flags().String(NodeAddress, "", "Seed node address to send the request to. Example: http://genesis-node:8080")
	cmd.MarkFlagRequired(NodeAddress)

	return cmd
}

func sendRegisterNewParticipantRequest(cmd *cobra.Command, nodeAddress string, body *RegisterParticipantDto) error {
	// Encode the payload to JSON
	jsonData, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := nodeAddress + "/v1/participants"
	cmd.Printf("Sending registration request to %s\n", url)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set the appropriate headers
	req.Header.Set("Content-Type", "application/json")

	// Send the request
	httpClient := &http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	cmd.Printf("Response status code: %d\n", resp.StatusCode)

	// Check the response status code
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("server returned status %d and failed to read response body", resp.StatusCode)
		}
		return fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	cmd.Printf("✅ Participant registration successful!\n")
	cmd.Printf("Waiting for participant to be available (timeout: 30 seconds)...\n")

	participantURL := fmt.Sprintf("%s/v1/participants/%s", nodeAddress, body.Address)
	if err := waitForParticipantAvailable(cmd, participantURL, 30*time.Second); err != nil {
		cmd.Printf("⚠️  Warning: %v\n", err)
		cmd.Printf("You can manually check your participant at %s\n", participantURL)
	} else {
		cmd.Printf("✅ Participant is now available at %s\n", participantURL)
	}
	time.Sleep(1 * time.Second)

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
				// Continue polling on error
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
					// Continue polling on unmarshal error
					continue
				}

				if participant.Pubkey != "" {
					cmd.Printf("\n")
					cmd.Printf("✅ Found participant with pubkey: %s (balance: %d)\n", participant.Pubkey, participant.Balance)
					return nil
				}
			} else {
				resp.Body.Close()
			}

		}
	}
}
