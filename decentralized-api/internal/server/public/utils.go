package public

import (
	"decentralized-api/logging"
	"encoding/base64"
	"errors"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/productscience/inference/x/inference/types"
)

func validateRequestAgainstPubKey(request *ChatRequest, pubKey string) error {
	logging.Debug("Checking key for request", types.Inferences, "pubkey", pubKey, "body", string(request.Body))
	pubKeyBytes, err := base64.StdEncoding.DecodeString(pubKey)
	if err != nil {
		return err
	}
	actualKey := secp256k1.PubKey{Key: pubKeyBytes}
	// Not sure about decoding/encoding the actual key bytes
	keyBytes, err := base64.StdEncoding.DecodeString(request.AuthKey)

	valid := actualKey.VerifySignature(request.Body, keyBytes)
	if !valid {
		logging.Warn("Signature did not match pubkey", types.Inferences)
		return errors.New("invalid signature")
	}
	return nil
}
