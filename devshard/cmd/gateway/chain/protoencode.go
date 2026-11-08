package chain

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Type URLs for the Any-wrapped messages and pubkey this package signs, the protobuf wire constants, and the field numbers of every message this package encodes by hand.
const (
	createEscrowMsgTypeURL = "/inference.inference.MsgCreateDevshardEscrow"
	settleEscrowMsgTypeURL = "/inference.inference.MsgSettleDevshardEscrow"
	secp256k1PubKeyTypeURL = "/cosmos.crypto.secp256k1.PubKey"

	protobufTagFieldShift           = 3
	protobufWireTypeVarint          = 0
	protobufWireTypeLengthDelimited = 2
	protobufBoolTrue                = 1
	varintContinuationBit           = 0x80
	varintPayloadBits               = 7

	anyFieldTypeURL = 1
	anyFieldValue   = 2

	secp256k1PubKeyFieldKey = 1

	coinFieldDenom  = 1
	coinFieldAmount = 2

	feeFieldAmount   = 1
	feeFieldGasLimit = 2

	modeInfoSingleFieldMode = 1
	signModeDirect          = 1
	modeInfoFieldSingle     = 1

	signerInfoFieldPublicKey = 1
	signerInfoFieldModeInfo  = 2
	signerInfoFieldSequence  = 3

	authInfoFieldSignerInfos = 1
	authInfoFieldFee         = 2

	signDocFieldBodyBytes     = 1
	signDocFieldAuthInfoBytes = 2
	signDocFieldChainID       = 3
	signDocFieldAccountNumber = 4

	txRawFieldBodyBytes     = 1
	txRawFieldAuthInfoBytes = 2
	txRawFieldSignatures    = 3

	txBodyFieldMessages         = 1
	txBodyFieldUnordered        = 4
	txBodyFieldTimeoutTimestamp = 5

	timestampFieldSeconds = 1
	timestampFieldNanos   = 2
)

// appendVarintField appends a protobuf varint-wire-type field: tag then value.
func appendVarintField(dst []byte, field int, value uint64) []byte {
	dst = appendVarint(dst, uint64(field<<protobufTagFieldShift|protobufWireTypeVarint))
	return appendVarint(dst, value)
}

// appendBytesField appends a protobuf length-delimited field: tag, length, bytes.
func appendBytesField(dst []byte, field int, value []byte) []byte {
	dst = appendVarint(dst, uint64(field<<protobufTagFieldShift|protobufWireTypeLengthDelimited))
	dst = appendVarint(dst, uint64(len(value)))
	return append(dst, value...)
}

// appendVarint appends value as a base-128 little-endian protobuf varint.
func appendVarint(dst []byte, value uint64) []byte {
	for value >= varintContinuationBit {
		dst = append(dst, byte(value)|varintContinuationBit)
		value >>= varintPayloadBits
	}
	return append(dst, byte(value))
}

// encodeAny wraps value in a google.protobuf.Any: field 1 type_url, field 2 value.
func encodeAny(typeURL string, value []byte) []byte {
	var out []byte
	out = appendBytesField(out, anyFieldTypeURL, []byte(typeURL))
	out = appendBytesField(out, anyFieldValue, value)
	return out
}

// encodeSecp256k1PubKey wraps a compressed key as cosmos.crypto.secp256k1.PubKey.
func encodeSecp256k1PubKey(compressed []byte) []byte {
	var out []byte
	out = appendBytesField(out, secp256k1PubKeyFieldKey, compressed)
	return out
}

// encodeFee builds a cosmos tx Fee; the Coin amount is a decimal string field.
func encodeFee(denom string, amount uint64, gasLimit uint64) []byte {
	var coin []byte
	coin = appendBytesField(coin, coinFieldDenom, []byte(denom))
	coin = appendBytesField(coin, coinFieldAmount, []byte(strconv.FormatUint(amount, 10)))
	var out []byte
	out = appendBytesField(out, feeFieldAmount, coin)
	out = appendVarintField(out, feeFieldGasLimit, gasLimit)
	return out
}

// encodeSignerInfo builds a SignerInfo with a single SIGN_MODE_DIRECT mode.
func encodeSignerInfo(pubKeyAny []byte, sequence uint64) []byte {
	var single []byte
	single = appendVarintField(single, modeInfoSingleFieldMode, signModeDirect)
	var modeInfo []byte
	modeInfo = appendBytesField(modeInfo, modeInfoFieldSingle, single)
	var out []byte
	out = appendBytesField(out, signerInfoFieldPublicKey, pubKeyAny)
	out = appendBytesField(out, signerInfoFieldModeInfo, modeInfo)
	out = appendVarintField(out, signerInfoFieldSequence, sequence)
	return out
}

// encodeAuthInfo builds AuthInfo: field 1 SignerInfo, field 2 Fee.
func encodeAuthInfo(pubKeyAny []byte, sequence uint64, denom string, amount, gasLimit uint64) []byte {
	signerInfo := encodeSignerInfo(pubKeyAny, sequence)
	fee := encodeFee(denom, amount, gasLimit)
	var out []byte
	out = appendBytesField(out, authInfoFieldSignerInfos, signerInfo)
	out = appendBytesField(out, authInfoFieldFee, fee)
	return out
}

// encodeSignDoc builds the SignDoc that gets hashed and signed for broadcast.
func encodeSignDoc(bodyBytes, authInfoBytes []byte, chainID string, accountNumber uint64) []byte {
	var out []byte
	out = appendBytesField(out, signDocFieldBodyBytes, bodyBytes)
	out = appendBytesField(out, signDocFieldAuthInfoBytes, authInfoBytes)
	out = appendBytesField(out, signDocFieldChainID, []byte(chainID))
	out = appendVarintField(out, signDocFieldAccountNumber, accountNumber)
	return out
}

// encodeTxRaw builds the broadcastable TxRaw: body, auth info, signature.
func encodeTxRaw(bodyBytes, authInfoBytes, signature []byte) []byte {
	var out []byte
	out = appendBytesField(out, txRawFieldBodyBytes, bodyBytes)
	out = appendBytesField(out, txRawFieldAuthInfoBytes, authInfoBytes)
	out = appendBytesField(out, txRawFieldSignatures, signature)
	return out
}

// encodeUnorderedTxBody builds a TxBody for the unordered-tx extension: field 1 msg, field 4 unordered=true, field 5 timeout_timestamp.
func encodeUnorderedTxBody(msgAny []byte, timeout time.Time) []byte {
	var out []byte
	out = appendBytesField(out, txBodyFieldMessages, msgAny)
	out = appendVarintField(out, txBodyFieldUnordered, protobufBoolTrue)
	out = appendBytesField(out, txBodyFieldTimeoutTimestamp, encodeTimestamp(timeout))
	return out
}

// encodeTimestamp builds a google.protobuf.Timestamp; nanos is omitted when zero.
func encodeTimestamp(ts time.Time) []byte {
	var out []byte
	out = appendVarintField(out, timestampFieldSeconds, uint64(ts.Unix()))
	if nanos := ts.Nanosecond(); nanos != 0 {
		out = appendVarintField(out, timestampFieldNanos, uint64(nanos))
	}
	return out
}

// txHashFromBytes returns the cosmos tx hash: upper-hex SHA-256 of the tx bytes.
func txHashFromBytes(txBytes []byte) string {
	sum := sha256.Sum256(txBytes)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}
