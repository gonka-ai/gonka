package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"devshard/signing"
)

const (
	settleEscrowMsgTypeURL = "/inference.inference.MsgSettleDevshardEscrow"
	secp256k1PubKeyTypeURL = "/cosmos.crypto.secp256k1.PubKey"
)

func txSettingDurationMS(raw string, fallback time.Duration) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

func normalizePrivateKeyHex(key string) string {
	return strings.TrimPrefix(strings.TrimSpace(key), "0x")
}

func signerFromRequestKey(privateKey, privateKeyEnv string) (*signing.Secp256k1Signer, string, error) {
	keyHex := normalizePrivateKeyHex(privateKey)
	envName := strings.TrimSpace(privateKeyEnv)
	if keyHex == "" && envName != "" {
		keyHex = normalizePrivateKeyHex(os.Getenv(envName))
	}
	if keyHex == "" {
		return nil, "", errors.New("private_key or private_key_env is required")
	}
	signer, err := signing.SignerFromHex(keyHex)
	if err != nil {
		return nil, "", err
	}
	return signer, keyHex, nil
}

func encodeMsgSettleDevshardEscrow(settler string, settlement SettlementJSON) ([]byte, error) {
	escrowID, err := strconv.ParseUint(strings.TrimSpace(settlement.EscrowID), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse escrow_id: %w", err)
	}
	stateRoot, err := base64.StdEncoding.DecodeString(settlement.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("decode state_root: %w", err)
	}
	restHash, err := base64.StdEncoding.DecodeString(settlement.RestHash)
	if err != nil {
		return nil, fmt.Errorf("decode rest_hash: %w", err)
	}
	var out []byte
	out = appendBytesField(out, 1, []byte(settler))
	out = appendVarintField(out, 2, escrowID)
	out = appendBytesField(out, 3, stateRoot)
	out = appendVarintField(out, 4, settlement.Nonce)
	out = appendBytesField(out, 5, restHash)
	for _, hs := range settlement.HostStats {
		out = appendBytesField(out, 6, encodeSettlementHostStats(hs))
	}
	for _, sig := range settlement.Signatures {
		encoded, err := encodeSlotSignature(sig)
		if err != nil {
			return nil, err
		}
		out = appendBytesField(out, 7, encoded)
	}
	out = appendVarintField(out, 8, settlement.Fees)
	out = appendBytesField(out, 9, []byte(settlement.StateRootAndProtocolVersion))
	return out, nil
}

func encodeSettlementHostStats(hs HostStatsJSON) []byte {
	var out []byte
	out = appendVarintField(out, 1, uint64(hs.SlotID))
	out = appendVarintField(out, 2, uint64(hs.Missed))
	out = appendVarintField(out, 3, uint64(hs.Invalid))
	out = appendVarintField(out, 4, hs.Cost)
	out = appendVarintField(out, 5, uint64(hs.RequiredValidations))
	out = appendVarintField(out, 6, uint64(hs.CompletedValidations))
	return out
}

func encodeSlotSignature(sig SlotSignatureJSON) ([]byte, error) {
	sigBytes, err := base64.StdEncoding.DecodeString(sig.Signature)
	if err != nil {
		return nil, fmt.Errorf("decode signature for slot %d: %w", sig.SlotID, err)
	}
	var out []byte
	out = appendVarintField(out, 1, uint64(sig.SlotID))
	out = appendBytesField(out, 2, sigBytes)
	return out, nil
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
