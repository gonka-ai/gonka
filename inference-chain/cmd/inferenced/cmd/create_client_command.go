package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/keys"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"strings"
)

func CreateClientCommand() *cobra.Command {
	command := keys.AddKeyCommand()
	command.Use = "create-client <name>"
	command.Short = "Add a key to the keychain and then add a participant"
	command.Flags().String(NodeAddress, "", "Node address to send the request to. Example: http://<ip>:<port>")
	addKeyAction := command.RunE

	command.RunE = func(cmd *cobra.Command, args []string) error {
		nodeAddress, err := cmd.Flags().GetString(NodeAddress)
		if err != nil {
			return err
		}
		if strings.TrimSpace(nodeAddress) == "" {
			return errors.New("node address is required")
		}

		if err := addKeyAction(cmd, args); err != nil {
			return err
		}

		command.Printf("Accessing keyring to get the public key and address. accountName = %s\n", args[0])
		body, err := getRegisterClientDto(client.GetClientContextFromCmd(cmd), args[0])
		if err != nil {
			return err
		}

		command.Printf("Sending a request. nodeAddress = %s. body = %v\n", nodeAddress, body)

		return sendRegisterParticipantRequest(cmd, nodeAddress, body)
	}

	return command
}

type RegisterClientDto struct {
	PubKey  string `json:"pub_key"`
	Address string `json:"address"`
}

func getRegisterClientDto(context client.Context, accountName string) (*RegisterClientDto, error) {
	keyRecord, err := context.Keyring.Key(accountName)
	if err != nil {
		return nil, err
	}

	addr, err := keyRecord.GetAddress()
	if err != nil {
		return nil, err
	}

	pk, err := keyRecord.GetPubKey()
	if err != nil {
		return nil, err
	}

	result := RegisterClientDto{
		PubKey:  base64.StdEncoding.EncodeToString(pk.Bytes()),
		Address: addr.String(),
	}

	return &result, nil
}

func sendRegisterParticipantRequest(cmd *cobra.Command, nodeAddress string, body *RegisterClientDto) error {
	// Encode the payload to JSON
	jsonData, err := json.Marshal(body)
	if err != nil {
		return err
	}

	url := nodeAddress + "/v1/participants"
	cmd.Printf("Sending a request to %s\n", url)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
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
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	cmd.Printf("You can check your participant at %s/v1/participants/%s\n", nodeAddress, body.Address)

	return nil
}
