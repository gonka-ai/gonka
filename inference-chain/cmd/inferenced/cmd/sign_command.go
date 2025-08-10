package cmd

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/spf13/cobra"
)

const (
	AccountAddress  = "account-address"
	File            = "file"
	Signature       = "signature"
	NodeAddress     = "node-address"
	Timestamp       = "timestamp"
	EndpointAccount = "endpoint-account" // Optional, used for specifying the account that will receive the request
)

func SignatureCommands() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        "signature",
		Short:                      "Sign or validate a text with the private key of a local account",
		DisableFlagParsing:         false,
		SuggestionsMinimumDistance: 2,
	}
	cmd.AddCommand(
		GetPayloadSignCommand(),
		GetPayloadVerifyCommand(),
		PostSignedRequest(),
	)
	return cmd
}

func GetPayloadVerifyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        "verify [text]",
		Short:                      "Verify a signature on arbitrary data",
		DisableFlagParsing:         false,
		SuggestionsMinimumDistance: 2,
		RunE:                       verifyPayload,
	}
	cmd.Flags().String(AccountAddress, "", "Address of the account that will sign the transaction")
	cmd.Flags().String(File, "", "File containing the payload to sign instead of text")
	cmd.Flags().String(Signature, "", "Signature to verify")
	cmd.Flags().Int64(Timestamp, 0, "Timestamp for the request (optional)")
	cmd.Flags().String(EndpointAccount, "", "Address of the account that will receive the request (optional)")
	flags.AddKeyringFlags(cmd.PersistentFlags())
	return cmd
}

func verifyPayload(cmd *cobra.Command, args []string) error {
	components, err := getSignatureComponents(cmd, args)
	if err != nil {
		return err
	}
	signature, err := cmd.Flags().GetString(Signature)
	if err != nil {
		return err
	}
	context, err := client.GetClientQueryContext(cmd)
	if err != nil {
		return err
	}
	address, err := getAddress(cmd, context)
	if err != nil {
		return err
	}

	cmd.Printf("Address: %s\n", address)

	key, err := context.Keyring.KeyByAddress(address)
	if err != nil {
		return err
	}
	pubKey, err := key.GetPubKey()
	if err != nil {
		return err
	}

	pubKeyStr := base64.StdEncoding.EncodeToString(pubKey.Bytes())
	err = calculations.ValidateSignature(components, calculations.Developer, pubKeyStr, signature)
	if err != nil {
		cmd.Printf("Signature not verified: %s\n", err)
	} else {
		cmd.Printf("Signature verified\n")
	}
	return nil
}

func GetPayloadSignCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        "create [text]",
		Short:                      "Sign arbitrary data with the private key of a local account",
		DisableFlagParsing:         false,
		SuggestionsMinimumDistance: 2,
		RunE:                       signPayload,
	}
	cmd.Flags().String(AccountAddress, "", "Address of the account that will sign the transaction")
	cmd.Flags().String(File, "", "File containing the payload to sign instead of text")
	cmd.Flags().Int64(Timestamp, 0, "Timestamp for the request (optional)")
	cmd.Flags().String(EndpointAccount, "", "Address of the account that will receive the request (optional)")
	flags.AddKeyringFlags(cmd.PersistentFlags())

	return cmd
}

func signPayload(cmd *cobra.Command, args []string) (err error) {
	components, err := getSignatureComponents(cmd, args)
	if err != nil {
		return err
	}
	context, err := client.GetClientQueryContext(cmd)
	if err != nil {
		return err
	}

	addr, err := getAddress(cmd, context)
	if err != nil {
		return err
	}

	cmd.Printf("Address: %s\n", addr)

	signer := &AccountSigner{
		Addr:    addr,
		Keyring: &context.Keyring,
	}
	signatureString, err := calculations.Sign(signer, components, calculations.Developer)
	if err != nil {
		return err
	}

	cmd.Printf("Signature: %s\n", signatureString)
	return nil
}

func getAddress(cmd *cobra.Command, context client.Context) (sdk.AccAddress, error) {
	accountAddress, err := cmd.Flags().GetString(AccountAddress)
	if err != nil {
		return nil, err
	}

	if accountAddress != "" {
		return sdk.AccAddressFromBech32(accountAddress)
	}

	list, _ := context.Keyring.List()
	for _, key := range list {
		address, err := key.GetAddress()
		if err != nil {
			return nil, err
		}
		if key.GetLocal() != nil && !strings.HasPrefix(key.Name, "POOL_") {
			return address, nil
		}
	}
	return nil, errors.New("no local address found")
}

func getInputString(cmd *cobra.Command, args []string) (string, error) {
	filename, err := cmd.Flags().GetString(File)
	if err != nil {
		return "", err
	}

	switch filename {
	case "":
		if len(args) == 0 {
			return "", errors.New("no text provided")
		}
		return args[0], nil
	case "-":
		stdioBytes, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(stdioBytes), nil
	default:
		fileBytes, err := os.ReadFile(filename)
		if err != nil {
			return "", err
		}
		return string(fileBytes), nil
	}
}

func getSignatureComponents(cmd *cobra.Command, args []string) (calculations.SignatureComponents, error) {
	payload, err := getInputString(cmd, args)
	if err != nil {
		return calculations.SignatureComponents{}, err
	}

	// Get timestamp from flag
	timestamp, err := cmd.Flags().GetInt64(Timestamp)
	if err != nil {
		return calculations.SignatureComponents{}, err
	}

	// Get endpoint account from flag
	endpointAccount, err := cmd.Flags().GetString(EndpointAccount)
	if err != nil {
		return calculations.SignatureComponents{}, err
	}

	return calculations.SignatureComponents{
		Payload:         payload,
		Timestamp:       timestamp,
		TransferAddress: endpointAccount,
		ExecutorAddress: "", // This is not set from CLI flags
	}, nil
}

func PostSignedRequest() *cobra.Command {
	cmd := &cobra.Command{
		Use:                        "send-request [text]",
		Short:                      "Sign and send a completion request",
		DisableFlagParsing:         false,
		SuggestionsMinimumDistance: 2,
		RunE:                       postSignedRequest,
	}
	cmd.Flags().String(AccountAddress, "", "Address of the account that will sign the transaction")
	cmd.Flags().String(NodeAddress, "", "Address of the node to send the request to. Example: http://<ip>:<port>")
	cmd.Flags().String(File, "", "File containing the payload to sign instead of text")
	cmd.Flags().Int64(Timestamp, 0, "Timestamp for the request (optional)")
	cmd.Flags().String(EndpointAccount, "", "Address of the account that will receive the request (optional)")
	return cmd
}

func postSignedRequest(cmd *cobra.Command, args []string) error {
	nodeAddress, err := cmd.Flags().GetString(NodeAddress)
	if err != nil {
		return err
	}

	components, err := getSignatureComponents(cmd, args)
	if err != nil {
		return err
	}

	context := client.GetClientContextFromCmd(cmd)
	addr, err := getAddress(cmd, context)
	if err != nil {
		return err
	}

	cmd.Printf("Address: %s\n", addr)

	signer := &AccountSigner{
		Addr:    addr,
		Keyring: &context.Keyring,
	}
	signatureString, err := calculations.Sign(signer, components, calculations.Developer)
	if err != nil {
		return err
	}

	cmd.Printf("Signature: %s\n", signatureString)
	// Use the payload from components for the request
	return sendSignedRequest(cmd, nodeAddress, []byte(components.Payload), signatureString, addr)
}

func sendSignedRequest(cmd *cobra.Command, nodeAddress string, payloadBytes []byte, signature string, requesterAddress sdk.AccAddress) error {
	url := nodeAddress + "/v1/chat/completions"

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return err
	}

	cmd.Printf("Sending POST request to %s\n", url)
	cmd.Printf("Authorization: %s\n", signature)
	cmd.Printf("X-Requester-Address: %s\n", requesterAddress.String())

	// TODO use constants from decentralized-api/utils here
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", signature)
	req.Header.Set("X-Requester-Address", requesterAddress.String())

	httpClient := &http.Client{}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	cmd.Println("Response:")

	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			cmd.Println(line)
		}

		if err := scanner.Err(); err != nil {
			return err
		}
	} else {
		var bodyBytes, err = io.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		cmd.Println(string(bodyBytes))
	}
	return nil
}

type AccountSigner struct {
	Addr    sdk.AccAddress
	Keyring *keyring.Keyring
}

func (s *AccountSigner) SignBytes(data []byte) (string, error) {
	kr := *s.Keyring
	outputBytes, _, err := kr.SignByAddress(s.Addr, data, signing.SignMode_SIGN_MODE_DIRECT)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(outputBytes), nil
}
