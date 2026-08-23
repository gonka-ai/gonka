package tx

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	createEscrowMsgTypeURL = "/inference.inference.MsgCreateDevshardEscrow"
	settleEscrowMsgTypeURL = "/inference.inference.MsgSettleDevshardEscrow"
	secp256k1PubKeyTypeURL = "/cosmos.crypto.secp256k1.PubKey"
)

func buildCreateDevshardEscrowTx(signer UnorderedSigner, chainID string, account chainAccount, feeDenom string, feeAmount, gasLimit, amount uint64, modelID string) ([]byte, error) {
	if strings.TrimSpace(chainID) == "" {
		return nil, fmt.Errorf("chain id is required")
	}
	msg := encodeMsgCreateDevshardEscrow(signer.Address(), amount, modelID)
	bodyBytes := encodeUnorderedTxBody(encodeAny(createEscrowMsgTypeURL, msg), time.Now().UTC().Add(defaultUnorderedTTL))
	pubKey := encodeAny(secp256k1PubKeyTypeURL, encodeSecp256k1PubKey(signer.CompressedPublicKeyBytes()))
	authInfoBytes := encodeAuthInfo(pubKey, 0, feeDenom, feeAmount, gasLimit)
	signDoc := encodeSignDoc(bodyBytes, authInfoBytes, chainID, account.AccountNumber)
	sig, err := signer.Sign(signDoc)
	if err != nil {
		return nil, err
	}
	if len(sig) < 64 {
		return nil, fmt.Errorf("invalid signature length %d", len(sig))
	}
	return encodeTxRaw(bodyBytes, authInfoBytes, sig[:64]), nil
}

func buildSettleDevshardEscrowTx(signer UnorderedSigner, chainID string, account chainAccount, feeDenom string, feeAmount, gasLimit uint64, settlement SettleParams) ([]byte, error) {
	if strings.TrimSpace(chainID) == "" {
		return nil, fmt.Errorf("chain id is required")
	}
	msg, err := encodeMsgSettleDevshardEscrow(signer.Address(), settlement)
	if err != nil {
		return nil, err
	}
	bodyBytes := encodeUnorderedTxBody(encodeAny(settleEscrowMsgTypeURL, msg), time.Now().UTC().Add(defaultUnorderedTTL))
	pubKey := encodeAny(secp256k1PubKeyTypeURL, encodeSecp256k1PubKey(signer.CompressedPublicKeyBytes()))
	authInfoBytes := encodeAuthInfo(pubKey, 0, feeDenom, feeAmount, gasLimit)
	signDoc := encodeSignDoc(bodyBytes, authInfoBytes, chainID, account.AccountNumber)
	sig, err := signer.Sign(signDoc)
	if err != nil {
		return nil, err
	}
	if len(sig) < 64 {
		return nil, fmt.Errorf("invalid signature length %d", len(sig))
	}
	return encodeTxRaw(bodyBytes, authInfoBytes, sig[:64]), nil
}

func encodeMsgCreateDevshardEscrow(creator string, amount uint64, modelID string) []byte {
	var out []byte
	out = appendBytesField(out, 1, []byte(creator))
	out = appendVarintField(out, 2, amount)
	out = appendBytesField(out, 3, []byte(modelID))
	return out
}

func encodeMsgSettleDevshardEscrow(settler string, settlement SettleParams) ([]byte, error) {
	var out []byte
	out = appendBytesField(out, 1, []byte(settler))
	out = appendVarintField(out, 2, settlement.EscrowID)
	out = appendBytesField(out, 3, settlement.StateRoot)
	out = appendVarintField(out, 4, settlement.Nonce)
	out = appendBytesField(out, 5, settlement.RestHash)
	for _, hs := range settlement.HostStats {
		out = appendBytesField(out, 6, encodeSettlementHostStats(hs))
	}
	for _, sig := range settlement.Signatures {
		out = appendBytesField(out, 7, encodeSlotSignature(sig))
	}
	out = appendVarintField(out, 8, settlement.Fees)
	out = appendBytesField(out, 9, settlement.StateRootAndProtocolVersion)
	return out, nil
}

func encodeSettlementHostStats(hs HostStats) []byte {
	var out []byte
	out = appendVarintField(out, 1, uint64(hs.SlotID))
	out = appendVarintField(out, 2, uint64(hs.Missed))
	out = appendVarintField(out, 3, uint64(hs.Invalid))
	out = appendVarintField(out, 4, hs.Cost)
	out = appendVarintField(out, 5, uint64(hs.RequiredValidations))
	out = appendVarintField(out, 6, uint64(hs.CompletedValidations))
	return out
}

func encodeSlotSignature(sig SlotSignature) []byte {
	var out []byte
	out = appendVarintField(out, 1, uint64(sig.SlotID))
	out = appendBytesField(out, 2, sig.Signature)
	return out
}

func encodeSecp256k1PubKey(key []byte) []byte {
	var out []byte
	out = appendBytesField(out, 1, key)
	return out
}

func encodeAny(typeURL string, value []byte) []byte {
	var out []byte
	out = appendBytesField(out, 1, []byte(typeURL))
	out = appendBytesField(out, 2, value)
	return out
}

func encodeUnorderedTxBody(msgAny []byte, timeout time.Time) []byte {
	var out []byte
	out = appendBytesField(out, 1, msgAny)
	out = appendVarintField(out, 4, 1)
	out = appendBytesField(out, 5, encodeTimestamp(timeout))
	return out
}

func encodeTimestamp(ts time.Time) []byte {
	var out []byte
	out = appendVarintField(out, 1, uint64(ts.Unix()))
	if nanos := ts.Nanosecond(); nanos != 0 {
		out = appendVarintField(out, 2, uint64(nanos))
	}
	return out
}

func encodeAuthInfo(pubKeyAny []byte, sequence uint64, feeDenom string, feeAmount uint64, gasLimit uint64) []byte {
	signerInfo := encodeSignerInfo(pubKeyAny, sequence)
	fee := encodeFee(feeDenom, feeAmount, gasLimit)
	var out []byte
	out = appendBytesField(out, 1, signerInfo)
	out = appendBytesField(out, 2, fee)
	return out
}

func encodeSignerInfo(pubKeyAny []byte, sequence uint64) []byte {
	var single []byte
	single = appendVarintField(single, 1, 1) // SIGN_MODE_DIRECT
	var modeInfo []byte
	modeInfo = appendBytesField(modeInfo, 1, single)
	var out []byte
	out = appendBytesField(out, 1, pubKeyAny)
	out = appendBytesField(out, 2, modeInfo)
	out = appendVarintField(out, 3, sequence)
	return out
}

func encodeFee(denom string, amount uint64, gasLimit uint64) []byte {
	var coin []byte
	coin = appendBytesField(coin, 1, []byte(denom))
	coin = appendBytesField(coin, 2, []byte(strconv.FormatUint(amount, 10)))
	var out []byte
	out = appendBytesField(out, 1, coin)
	out = appendVarintField(out, 2, gasLimit)
	return out
}

func encodeSignDoc(bodyBytes []byte, authInfoBytes []byte, chainID string, accountNumber uint64) []byte {
	var out []byte
	out = appendBytesField(out, 1, bodyBytes)
	out = appendBytesField(out, 2, authInfoBytes)
	out = appendBytesField(out, 3, []byte(chainID))
	out = appendVarintField(out, 4, accountNumber)
	return out
}

func encodeTxRaw(bodyBytes []byte, authInfoBytes []byte, signature []byte) []byte {
	var out []byte
	out = appendBytesField(out, 1, bodyBytes)
	out = appendBytesField(out, 2, authInfoBytes)
	out = appendBytesField(out, 3, signature)
	return out
}

func appendVarintField(dst []byte, fieldNumber int, value uint64) []byte {
	dst = appendVarint(dst, uint64(fieldNumber<<3))
	return appendVarint(dst, value)
}

func appendBytesField(dst []byte, fieldNumber int, value []byte) []byte {
	dst = appendVarint(dst, uint64(fieldNumber<<3|2))
	dst = appendVarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendVarint(dst []byte, value uint64) []byte {
	for value >= 0x80 {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	return append(dst, byte(value))
}
