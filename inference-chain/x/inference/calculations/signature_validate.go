package calculations

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strconv"
	"time"

	sdkerrors "cosmossdk.io/errors"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/productscience/inference/x/inference/types"
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
	GetAccountPubKeysWithGrantees(ctx context.Context, granterAddress string) ([]string, error)
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
		agentKeys, err := pubKeyGetter.GetAccountPubKeysWithGrantees(ctx, sigData.TransferAgent.Address)
		if err != nil {
			return sdkerrors.Wrap(types.ErrParticipantNotFound, sigData.TransferAgent.Address)
		}

		err = ValidateSignatureWithGrantees(components, TransferAgent, agentKeys, sigData.TransferSignature)
		if err != nil {
			return sdkerrors.Wrap(types.ErrInvalidSignature, "transfer signature validation failed")
		}
	}

	// Check executor signature if executor participant is provided
	if sigData.Executor != nil && sigData.ExecutorSignature != "" {
		executorKeys, err := pubKeyGetter.GetAccountPubKeysWithGrantees(ctx, sigData.Executor.Address)
		if err != nil {
			return sdkerrors.Wrap(types.ErrParticipantNotFound, sigData.Executor.Address)
		}

		err = ValidateSignatureWithGrantees(components, ExecutorAgent, executorKeys, sigData.ExecutorSignature)
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
	slog.Debug("Signing components", "type", signatureType, "payload", components.Payload, "timestamp", components.Timestamp, "transferAddress", components.TransferAddress, "executorAddress", components.ExecutorAddress)
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

func ValidateSignature(components SignatureComponents, signatureType SignatureType, pubKey string, signature string) error {
	slog.Info("Validating signature", "type", signatureType, "pubKey", pubKey, "signature", signature)
	slog.Debug("Components", "payload", components.Payload, "timestamp", components.Timestamp, "transferAddress", components.TransferAddress, "executorAddress", components.ExecutorAddress)
	bytes := getSignatureBytes(components, signatureType)
	return validateSignature(bytes, pubKey, signature)
}

func ValidateSignatureWithGrantees(components SignatureComponents, signatureType SignatureType, pubKeys []string, signature string) error {
	slog.Info("Validating signature with grantees", "type", signatureType, "pubKeys", pubKeys, "signature", signature)
	slog.Debug("Components", "payload", components.Payload, "timestamp", components.Timestamp, "transferAddress", components.TransferAddress, "executorAddress", components.ExecutorAddress)
	bytes := getSignatureBytes(components, signatureType)
	return validateSignatureWithGrantees(bytes, pubKeys, signature)
}

func getSignatureBytes(components SignatureComponents, signatureType SignatureType) []byte {
	var bytes []byte

	switch signatureType {
	case Developer:
		bytes = getDevBytes(components)
	case TransferAgent:
		bytes = getTransferBytes(components)
	case ExecutorAgent:
		bytes = getTransferBytes(components)
	}

	return bytes
}

func validateSignatureWithGrantees(
	bytes []byte,
	pubKeys []string,
	signature string,
) error {
	errors := []error{}
	for _, pubKey := range pubKeys {
		err := validateSignature(bytes, pubKey, signature)
		if err == nil {
			return nil
		}
		slog.Warn("Invalid signature", "pubKey", pubKey, "error", err)
		errors = append(errors, err)
	}
	slog.Warn("Invalid signature", "errors", errors)
	if len(errors) > 0 {
		return errors[0]
	}
	return nil
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

func ValidateTimestamp(signatureTimestamp int64, currentTimestamp int64, expirationSeconds int64, advanceSeconds int64, extraTime int64) error {
	timestampExpirationNs := expirationSeconds * int64(time.Second)
	timestampAdvanceNs := advanceSeconds * int64(time.Second)

	// Use default values if parameters are not set
	if timestampExpirationNs == 0 {
		timestampExpirationNs = 10 * int64(time.Second)
	}
	if timestampAdvanceNs == 0 {
		timestampAdvanceNs = 10 * int64(time.Second)
	}
	timestampExpirationNs += extraTime
	timestampAdvanceNs += extraTime

	requestOffset := currentTimestamp - signatureTimestamp

	if requestOffset > timestampExpirationNs {
		return types.ErrSignatureTooOld
	}
	if requestOffset < -timestampAdvanceNs {
		return types.ErrSignatureInFuture
	}

	return nil
}
