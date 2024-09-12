package cmd

import (
	"encoding/base64"
	"errors"
	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/spf13/cobra"
	"io"
	"os"
)

const (
	AccountAddress = "account-address"
	File           = "file"
	Signature      = "signature"
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
	context := client.GetClientContextFromCmd(cmd)
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
	return cmd
}

func signPayload(cmd *cobra.Command, args []string) (err error) {
	bytes, err := getInputBytes(cmd, args)
	if err != nil {
		return err
	}
	context := client.GetClientContextFromCmd(cmd)
	addr, err2 := getAddress(cmd, context)
	if err2 != nil {
		return err2
	}
	outputBytes, _, err := context.Keyring.SignByAddress(addr, bytes, signing.SignMode_SIGN_MODE_DIRECT)
	if err != nil {
		return err
	}
	// Hash the bytes to readable string
	signatureString := base64.StdEncoding.EncodeToString(outputBytes)
	cmd.Printf("Signature: %s\n", signatureString)
	// Get the account address from the account name
	// Sign the payload
	return nil
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
			if key.GetLocal() != nil {
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
