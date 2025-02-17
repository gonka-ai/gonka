package cmd

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/flags"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	AccountAddress = "account-address"
	File           = "file"
	Signature      = "signature"
	NodeAddress    = "node-address"
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
	flags.AddKeyringFlags(cmd.PersistentFlags())
	return cmd
}

func verifyPayload(cmd *cobra.Command, args []string) error {
	bytes, err := getInputBytes(cmd, args)
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
	signatureBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return err
	}
	key, err := context.Keyring.KeyByAddress(address)
	if err != nil {
		return err
	}
	pubKey, err := key.GetPubKey()
	if err != nil {
		return err
	}
	if pubKey.VerifySignature(bytes, signatureBytes) {
		cmd.Printf("Signature verified\n")
	} else {
		cmd.Printf("Signature not verified\n")
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
	flags.AddKeyringFlags(cmd.PersistentFlags())

	return cmd
}

func signPayload(cmd *cobra.Command, args []string) (err error) {
	bytes, err := getInputBytes(cmd, args)
	if err != nil {
		return err
	}
	context, err := client.GetClientQueryContext(cmd)
	if err != nil {
		return err
	}
	addr, err2 := getAddress(cmd, context)
	if err2 != nil {
		return err2
	}

	signatureString, err := getSignature(bytes, addr, context)
	if err != nil {
		return err
	}

	cmd.Printf("Signature: %s\n", signatureString)
	// Get the account address from the account name
	// Sign the payload
	return nil
}

func getSignature(inputBytes []byte, addr sdk.AccAddress, context client.Context) (string, error) {
	outputBytes, _, err := context.Keyring.SignByAddress(addr, inputBytes, signing.SignMode_SIGN_MODE_DIRECT)
	if err != nil {
		return "", err
	}
	// Hash the bytes to readable string
	return base64.StdEncoding.EncodeToString(outputBytes), nil
}

func getAddress(cmd *cobra.Command, context client.Context) (sdk.AccAddress, error) {
	accountAddress, err := cmd.Flags().GetString(AccountAddress)
	if err != nil {
		return nil, err
	}
	var addr sdk.AccAddress
	if accountAddress == "" {
		list, _ := context.Keyring.List()
		for _, key := range list {
			address, err := key.GetAddress()
			if err != nil {
				return nil, err
			}
			if key.GetLocal() != nil && !strings.HasPrefix(key.Name, "POOL_") {
				addr = address
			}
		}
	} else {
		addr2, err2 := sdk.AccAddressFromBech32(accountAddress)
		if err2 != nil {
			return nil, err2
		}
		addr = addr2
	}
	cmd.Println("Address: " + addr.String())
	return addr, nil
}

func getInputBytes(cmd *cobra.Command, args []string) ([]byte, error) {
	var bytes []byte
	filename, err := cmd.Flags().GetString(File)
	if err != nil {
		return nil, err
	}
	if filename != "" {
		// Read the file
		if filename == "-" {
			// Read from stdin
			bytes, err = io.ReadAll(os.Stdin)
		} else {
			// Read from file
			bytes, err = os.ReadFile(filename)
		}
	} else {
		// Read from args
		if len(args) == 0 {
			return nil, errors.New("no text provided")
		}
		bytes = []byte(args[0])
	}
	if err != nil {
		return nil, err
	}
	return bytes, nil
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
	return cmd
}

func postSignedRequest(cmd *cobra.Command, args []string) error {
	nodeAddress, err := cmd.Flags().GetString(NodeAddress)
	if err != nil {
		return err
	}

	inputBytes, err := getInputBytes(cmd, args)
	if err != nil {
		return err
	}

	context := client.GetClientContextFromCmd(cmd)
	addr, err2 := getAddress(cmd, context)
	if err2 != nil {
		return err2
	}

	signatureString, err := getSignature(inputBytes, addr, context)
	if err != nil {
		return err
	}

	cmd.Printf("Signature: %s\n", signatureString)

	return sendSignedRequest(cmd, nodeAddress, inputBytes, signatureString, addr)
}

func sendSignedRequest(cmd *cobra.Command, nodeAddress string, payloadBytes []byte, signature string, requesterAddress sdk.AccAddress) error {
	url := nodeAddress + "/v1/chat/completions"

	// Create a new request
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return err
	}

	// Set the required headers
	cmd.Printf("Sending POST request to %s\n", url)
	cmd.Printf("Authorization: %s\n", signature)
	cmd.Printf("X-Requester-Address: %s\n", requesterAddress.String())

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", signature)
	req.Header.Set("X-Requester-Address", requesterAddress.String())

	// Send the request
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
