package calculations

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"encoding/base64"
	"errors"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"strconv"
)

type SignatureType int

const (
	Developer SignatureType = iota
	TransferAgent
	ExecutorAgent
)

// PubKeyGetter defines an interface for retrieving public keys
type PubKeyGetter interface {
	GetAccountPubKey(ctx context.Context, address string) (string, error)
}

// SignatureData contains signature strings and participant pointers
type SignatureData struct {
	DevSignature      string             `json:"dev_signature"`
	TransferSignature string             `json:"transfer_signature"`
	ExecutorSignature string             `json:"executor_signature"`
	Dev               *types.Participant `json:"dev"`
	TransferAgent     *types.Participant `json:"transfer_agent"`
	Executor          *types.Participant `json:"executor"`
}

// VerifyKeys verifies signatures for each non-null participant in SignatureData
func VerifyKeys(ctx context.Context, components SignatureComponents, sigData SignatureData, pubKeyGetter PubKeyGetter) error {
	// Check developer signature if developer participant is provided
	if sigData.Dev != nil && sigData.DevSignature != "" {
		devKey, err := pubKeyGetter.GetAccountPubKey(ctx, sigData.Dev.Address)
		if err != nil {
			return sdkerrors.Wrap(types.ErrParticipantNotFound, sigData.Dev.Address)
		}

		err = ValidateSignature(components, Developer, devKey, sigData.DevSignature)
		if err != nil {
			return sdkerrors.Wrap(types.ErrInvalidSignature, "dev signature validation failed")
		}
	}

	// Check transfer agent signature if transfer agent participant is provided
	if sigData.TransferAgent != nil && sigData.TransferSignature != "" {
		agentKey, err := pubKeyGetter.GetAccountPubKey(ctx, sigData.TransferAgent.Address)
		if err != nil {
			return sdkerrors.Wrap(types.ErrParticipantNotFound, sigData.TransferAgent.Address)
		}

		err = ValidateSignature(components, TransferAgent, agentKey, sigData.TransferSignature)
		if err != nil {
			return sdkerrors.Wrap(types.ErrInvalidSignature, "transfer signature validation failed")
		}
	}

	// Check executor signature if executor participant is provided
	if sigData.Executor != nil && sigData.ExecutorSignature != "" {
		executorKey, err := pubKeyGetter.GetAccountPubKey(ctx, sigData.Executor.Address)
		if err != nil {
			return sdkerrors.Wrap(types.ErrParticipantNotFound, sigData.Executor.Address)
		}

		err = ValidateSignature(components, ExecutorAgent, executorKey, sigData.ExecutorSignature)
		if err != nil {
			return sdkerrors.Wrap(types.ErrInvalidSignature, "executor signature validation failed")
		}
	}

	return nil
}

type SignatureComponents struct {
	Payload         string
	Timestamp       int64
	TransferAddress string
	ExecutorAddress string
}

type Signer interface {
	SignBytes(data []byte) (string, error)
}

func Sign(signer Signer, components SignatureComponents, signatureType SignatureType) (string, error) {
	slog.Info("Signing components", "type", signatureType, "payload", components.Payload, "timestamp", components.Timestamp, "transferAddress", components.TransferAddress, "executorAddress", components.ExecutorAddress)
	bytes := getSignatureBytes(components, signatureType)
	hash := crypto.Sha256(bytes)
	slog.Info("Hash for signing", "hash", hash)
	signature, err := signer.SignBytes(bytes)
	if err != nil {
		return "", err
	}
	slog.Info("Generated signature", "type", signatureType, "signature", signature)
	return signature, nil
}

// ValidateSignature validates a signature based on the provided signature type
func ValidateSignature(components SignatureComponents, signatureType SignatureType, pubKey string, signature string) error {
	slog.Info("Validating signature", "type", signatureType, "pubKey", pubKey, "signature", signature)
	slog.Info("Components", "payload", components.Payload, "timestamp", components.Timestamp, "transferAddress", components.TransferAddress, "executorAddress", components.ExecutorAddress)
	bytes := getSignatureBytes(components, signatureType)
	return validateSignature(bytes, pubKey, signature)
}

// getSignatureBytes returns the bytes to be signed based on the signature type
func getSignatureBytes(components SignatureComponents, signatureType SignatureType) []byte {
	var bytes []byte

	switch signatureType {
	case Developer:
		bytes = getDevBytes(components)
	case TransferAgent:
		bytes = getTransferBytes(components)
	case ExecutorAgent:
		bytes = getTransferBytes(components) // For now, use the same as TransferAgent
	}

	return bytes
}

func validateSignature(bytes []byte, pubKey string, signature string) error {
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKey)
	if err != nil {
		return err
	}
	actualKey := secp256k1.PubKey{Key: pubKeyBytes}

	signatureBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return err
	}

	valid := actualKey.VerifySignature(bytes, signatureBytes)
	if !valid {
		return errors.New("invalid signature")
	}
	return nil
}

func getDevBytes(components SignatureComponents) []byte {
	// Create message payload by concatenating components
	messagePayload := []byte(components.Payload)
	if components.Timestamp > 0 {
		messagePayload = append(messagePayload, []byte(strconv.FormatInt(components.Timestamp, 10))...)
	}
	messagePayload = append(messagePayload, []byte(components.TransferAddress)...)
	return messagePayload
}

func getTransferBytes(components SignatureComponents) []byte {
	// Create message payload by concatenating components
	messagePayload := getDevBytes(components)
	messagePayload = append(messagePayload, []byte(components.ExecutorAddress)...)
	return messagePayload
}
