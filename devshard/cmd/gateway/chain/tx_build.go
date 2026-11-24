package chain

import (
	"fmt"
	"strings"
	"time"

	inferencetypes "github.com/productscience/inference/x/inference/types"

	"devshard/gatewayprotocol/settlement"
	"devshard/gatewayprotocol/settlementwire"
	"devshard/signing"
)

const compactSignatureLength = 64

// The escrow messages are marshalled by the chain's own generated types, so their wire layout tracks the chain by construction. See README.md, "How a message is encoded".
func encodeMsgCreateDevshardEscrow(creator string, amount uint64, modelID string) ([]byte, error) {
	message := &inferencetypes.MsgCreateDevshardEscrow{Creator: creator, Amount: amount, ModelId: modelID}
	return message.Marshal()
}

// truncateSignature drops the recovery byte so only r||s (64 bytes) goes into the tx.
func truncateSignature(sig []byte) ([]byte, error) {
	if len(sig) < compactSignatureLength {
		return nil, fmt.Errorf("invalid signature length %d", len(sig))
	}
	return sig[:compactSignatureLength], nil
}

const (
	actionCreateEscrow = "create-escrow"
	actionSettleEscrow = "settle-escrow"
)

type txSigning struct {
	signer        *signing.Secp256k1Signer
	chainID       string
	accountNumber uint64
	feeDenom      string
	feeAmount     uint64
	gasLimit      uint64
	ttl           time.Time
}

func (s txSigning) signMessage(action, typeURL string, encode func() ([]byte, error)) ([]byte, error) {
	if strings.TrimSpace(s.chainID) == "" {
		return nil, fmt.Errorf("chain id is required")
	}
	msg, err := encode()
	if err != nil {
		return nil, fmt.Errorf("encode %s message: %w", action, err)
	}
	bodyBytes := encodeUnorderedTxBody(encodeAny(typeURL, msg), s.ttl)
	pubKeyAny := encodeAny(secp256k1PubKeyTypeURL, encodeSecp256k1PubKey(s.signer.CompressedPublicKeyBytes()))
	authInfoBytes := encodeAuthInfo(pubKeyAny, 0, s.feeDenom, s.feeAmount, s.gasLimit)
	signDoc := encodeSignDoc(bodyBytes, authInfoBytes, s.chainID, s.accountNumber)
	sig, err := s.signer.Sign(signDoc)
	if err != nil {
		return nil, fmt.Errorf("sign %s tx: %w", action, err)
	}
	truncated, err := truncateSignature(sig)
	if err != nil {
		return nil, err
	}
	return encodeTxRaw(bodyBytes, authInfoBytes, truncated), nil
}

func buildCreateEscrowTx(parameters txSigning, amount uint64, modelID string) ([]byte, error) {
	return parameters.signMessage(actionCreateEscrow, createEscrowMsgTypeURL, func() ([]byte, error) {
		return encodeMsgCreateDevshardEscrow(parameters.signer.Address(), amount, modelID)
	})
}

func buildSettleEscrowTx(parameters txSigning, settler string, input settlement.Message) ([]byte, error) {
	return parameters.signMessage(actionSettleEscrow, settleEscrowMsgTypeURL, func() ([]byte, error) {
		return settlementwire.Marshal(settler, input)
	})
}
