package chain

import (
	"fmt"
	"strings"
	"time"

	inferencetypes "github.com/productscience/inference/x/inference/types"

	"devshard/signing"
)

const compactSignatureLength = 64

// The escrow messages are marshalled by the chain's own generated types, so their wire layout tracks the chain by construction. See README.md, "How a message is encoded".
func encodeMsgCreateDevshardEscrow(creator string, amount uint64, modelID string) ([]byte, error) {
	message := &inferencetypes.MsgCreateDevshardEscrow{Creator: creator, Amount: amount, ModelId: modelID}
	return message.Marshal()
}

func encodeMsgSettleDevshardEscrow(settler string, input SettlementInput) ([]byte, error) {
	hostStats := make([]*inferencetypes.DevshardSettlementHostStats, 0, len(input.HostStats))
	for _, hostStat := range input.HostStats {
		hostStats = append(hostStats, &inferencetypes.DevshardSettlementHostStats{
			SlotId:               uint32(hostStat.SlotID),
			Missed:               uint32(hostStat.Missed),
			Invalid:              uint32(hostStat.Invalid),
			Cost:                 hostStat.Cost,
			RequiredValidations:  uint32(hostStat.RequiredValidations),
			CompletedValidations: uint32(hostStat.CompletedValidations),
		})
	}
	signatures := make([]*inferencetypes.DevshardSlotSignature, 0, len(input.SlotSigs))
	for _, slotSig := range input.SlotSigs {
		signatures = append(signatures, &inferencetypes.DevshardSlotSignature{
			SlotId:    uint32(slotSig.SlotID),
			Signature: slotSig.Signature,
		})
	}
	message := &inferencetypes.MsgSettleDevshardEscrow{
		Settler:                     settler,
		EscrowId:                    input.EscrowID,
		StateRoot:                   input.StateRoot,
		Nonce:                       input.Nonce,
		RestHash:                    input.RestHash,
		HostStats:                   hostStats,
		Signatures:                  signatures,
		Fees:                        input.Fees,
		StateRootAndProtocolVersion: input.Version,
	}
	return message.Marshal()
}

// truncateSignature drops the recovery byte so only r||s (64 bytes) goes into the tx.
func truncateSignature(sig []byte) ([]byte, error) {
	if len(sig) < compactSignatureLength {
		return nil, fmt.Errorf("invalid signature length %d", len(sig))
	}
	return sig[:compactSignatureLength], nil
}

// buildCreateEscrowTx assembles and signs an unordered MsgCreateDevshardEscrow tx; ttl is caller-supplied so encoding stays deterministic.
func buildCreateEscrowTx(signer *signing.Secp256k1Signer, chainID string, accountNumber uint64, feeDenom string, feeAmount, gasLimit, amount uint64, modelID string, ttl time.Time) ([]byte, error) {
	if strings.TrimSpace(chainID) == "" {
		return nil, fmt.Errorf("chain id is required")
	}
	msg, err := encodeMsgCreateDevshardEscrow(signer.Address(), amount, modelID)
	if err != nil {
		return nil, fmt.Errorf("encode create-escrow message: %w", err)
	}
	bodyBytes := encodeUnorderedTxBody(encodeAny(createEscrowMsgTypeURL, msg), ttl)
	pubKeyAny := encodeAny(secp256k1PubKeyTypeURL, encodeSecp256k1PubKey(signer.CompressedPublicKeyBytes()))
	authInfoBytes := encodeAuthInfo(pubKeyAny, 0, feeDenom, feeAmount, gasLimit)
	signDoc := encodeSignDoc(bodyBytes, authInfoBytes, chainID, accountNumber)
	sig, err := signer.Sign(signDoc)
	if err != nil {
		return nil, fmt.Errorf("sign create-escrow tx: %w", err)
	}
	truncated, err := truncateSignature(sig)
	if err != nil {
		return nil, err
	}
	return encodeTxRaw(bodyBytes, authInfoBytes, truncated), nil
}

// buildSettleEscrowTx assembles and signs an unordered MsgSettleDevshardEscrow tx; settler may differ from the broadcasting signer.
func buildSettleEscrowTx(signer *signing.Secp256k1Signer, chainID string, accountNumber uint64, feeDenom string, feeAmount, gasLimit uint64, settler string, input SettlementInput, ttl time.Time) ([]byte, error) {
	if strings.TrimSpace(chainID) == "" {
		return nil, fmt.Errorf("chain id is required")
	}
	msg, err := encodeMsgSettleDevshardEscrow(settler, input)
	if err != nil {
		return nil, fmt.Errorf("encode settle-escrow message: %w", err)
	}
	bodyBytes := encodeUnorderedTxBody(encodeAny(settleEscrowMsgTypeURL, msg), ttl)
	pubKeyAny := encodeAny(secp256k1PubKeyTypeURL, encodeSecp256k1PubKey(signer.CompressedPublicKeyBytes()))
	authInfoBytes := encodeAuthInfo(pubKeyAny, 0, feeDenom, feeAmount, gasLimit)
	signDoc := encodeSignDoc(bodyBytes, authInfoBytes, chainID, accountNumber)
	sig, err := signer.Sign(signDoc)
	if err != nil {
		return nil, fmt.Errorf("sign settle-escrow tx: %w", err)
	}
	truncated, err := truncateSignature(sig)
	if err != nil {
		return nil, err
	}
	return encodeTxRaw(bodyBytes, authInfoBytes, truncated), nil
}
