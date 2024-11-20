package cosmosclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/types"
	"log"
	"log/slog"

	"github.com/cosmos/btcutil/bech32"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	auth "github.com/cosmos/cosmos-sdk/x/auth/types"
	"golang.org/x/crypto/ripemd160"
)

func (icc *InferenceCosmosClient) NewAuthQueryClient() auth.QueryClient {
	// Create a new query client
	queryClient := auth.NewQueryClient(icc.Client.Context())
	return queryClient
}

func GetPubKeyByAddress(client auth.QueryClient, address string) (cryptotypes.PubKey, error) {
	// Create the request
	req := &auth.QueryAccountRequest{Address: address}

	// Send the request
	res, err := client.Account(context.Background(), req)
	if err != nil {
		log.Fatalf("Failed to query account: %v", err)
	}

	// Unmarshal the account
	var account auth.BaseAccount
	interfaceRegistry := types.NewInterfaceRegistry()
	auth.RegisterInterfaces(interfaceRegistry)
	cdc := codec.NewProtoCodec(interfaceRegistry)

	err = cdc.UnpackAny(res.Account, &account)
	if err != nil {
		slog.Error("Failed to unmarshal account: %v", err)
	}

	// Get the public key
	pubKey := account.GetPubKey()

	if pubKey == nil {
		fmt.Println("Account has no public key (no transactions signed yet)")
		return nil, errors.New("account has no public key")
	}

	return pubKey, nil
}

// PubKeyToAddress Public key bytes to Cosmos address
//
//	pubKeyHex := "A1B2C3..." // Replace with your public key hex string
func PubKeyToAddress(pubKeyHex string) (string, error) {
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		slog.Error("Invalid public key hex", "err", err)
		return "", err
	}

	// Step 1: SHA-256 hash
	shaHash := sha256.Sum256(pubKeyBytes)

	// Step 2: RIPEMD-160 hash
	ripemdHasher := ripemd160.New()
	ripemdHasher.Write(shaHash[:])
	ripemdHash := ripemdHasher.Sum(nil)

	// Step 3: Bech32 encode
	prefix := "cosmos"
	fiveBitData, err := bech32.ConvertBits(ripemdHash, 8, 5, true)
	if err != nil {
		slog.Error("Failed to convert bits", "err", err)
		return "", err
	}

	address, err := bech32.Encode(prefix, fiveBitData)
	if err != nil {
		slog.Error("Failed to encode address", "err", err)
		return "", err
	}

	return address, nil
}

func PubKeyToString(pubKey cryptotypes.PubKey) string {
	return base64.StdEncoding.EncodeToString(pubKey.Bytes())
}
