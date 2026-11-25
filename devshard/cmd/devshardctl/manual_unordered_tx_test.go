package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"devshard/gatewayprotocol/settlement"
	"devshard/gatewayprotocol/settlementwire"
	"devshard/signing"
)

const (
	settleEscrowMessageTypeURL = "/inference.inference.MsgSettleDevshardEscrow"
	secp256k1PublicKeyTypeURL  = "/cosmos.crypto.secp256k1.PubKey"
	signModeDirect             = 1
)

// Test flow:
//  1. Skip unless MANUAL_SETTLEMENT_FILE, MANUAL_PRIVATE_KEY and MANUAL_BROADCAST_FILE are set.
//  2. Read the settlement JSON and the signer, chain id, account number, fee and gas limit from the environment.
//  3. Build the unordered settle tx around the `settlementwire` message and write it as a BroadcastTx JSON payload.
func TestManualBuildUnorderedSettleTx(t *testing.T) {
	settlementPath := os.Getenv("MANUAL_SETTLEMENT_FILE")
	privateKey := os.Getenv("MANUAL_PRIVATE_KEY")
	outPath := os.Getenv("MANUAL_BROADCAST_FILE")
	if settlementPath == "" || privateKey == "" || outPath == "" {
		t.Skip("manual helper requires MANUAL_SETTLEMENT_FILE, MANUAL_PRIVATE_KEY, MANUAL_BROADCAST_FILE")
	}

	settlementData, err := os.ReadFile(settlementPath)
	if err != nil {
		t.Fatalf("read settlement: %v", err)
	}
	var settlementJSON SettlementJSON
	if err := json.Unmarshal(settlementData, &settlementJSON); err != nil {
		t.Fatalf("parse settlement: %v", err)
	}

	signer, _, err := signerFromRequestKey(privateKey, "")
	if err != nil {
		t.Fatalf("load signer: %v", err)
	}
	chainID := firstNonEmpty(os.Getenv("MANUAL_CHAIN_ID"), "gonka-mainnet")
	accountNumber, err := parseRequiredUintEnv("MANUAL_ACCOUNT_NUMBER")
	if err != nil {
		t.Fatal(err)
	}
	feeAmount, err := parseRequiredUintEnv("MANUAL_FEE_AMOUNT")
	if err != nil {
		t.Fatal(err)
	}
	gasLimit, err := parseRequiredUintEnv("MANUAL_GAS_LIMIT")
	if err != nil {
		t.Fatal(err)
	}
	feeDenom := firstNonEmpty(os.Getenv("MANUAL_FEE_DENOM"), "ngonka")

	txBytes, err := buildManualUnorderedSettleTx(
		signer,
		chainID,
		accountNumber,
		feeDenom,
		feeAmount,
		gasLimit,
		settlementJSON,
		time.Now().UTC().Add(10*time.Minute),
	)
	if err != nil {
		t.Fatalf("build tx: %v", err)
	}

	payload := map[string]string{
		"tx_bytes": base64.StdEncoding.EncodeToString(txBytes),
		"mode":     "BROADCAST_MODE_SYNC",
	}
	payloadData, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := os.WriteFile(outPath, append(payloadData, '\n'), 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
}

func buildManualUnorderedSettleTx(signer *signing.Secp256k1Signer, chainID string, accountNumber uint64, feeDenom string, feeAmount, gasLimit uint64, settlementJSON SettlementJSON, timeout time.Time) ([]byte, error) {
	message, err := settlementMessageFromJSON(settlementJSON)
	if err != nil {
		return nil, err
	}
	messageBytes, err := settlementwire.Marshal(signer.Address(), message)
	if err != nil {
		return nil, err
	}
	bodyBytes := encodeManualUnorderedTxBody(encodeAny(settleEscrowMessageTypeURL, messageBytes), timeout)
	publicKey := encodeAny(secp256k1PublicKeyTypeURL, encodeSecp256k1PublicKey(signer.CompressedPublicKeyBytes()))
	authInfoBytes := encodeAuthInfo(publicKey, 0, feeDenom, feeAmount, gasLimit)
	signDoc := encodeSignDoc(bodyBytes, authInfoBytes, chainID, accountNumber)
	signature, err := signer.Sign(signDoc)
	if err != nil {
		return nil, err
	}
	if len(signature) < 64 {
		return nil, fmt.Errorf("invalid signature length %d", len(signature))
	}
	return encodeTxRaw(bodyBytes, authInfoBytes, signature[:64]), nil
}

func settlementMessageFromJSON(settlementJSON SettlementJSON) (settlement.Message, error) {
	stateRoot, err := decodeBase64Field(settlementJSON.StateRoot, "state_root")
	if err != nil {
		return settlement.Message{}, err
	}
	restHash, err := decodeBase64Field(settlementJSON.RestHash, "rest_hash")
	if err != nil {
		return settlement.Message{}, err
	}
	escrowID, err := parseEscrowIDString(settlementJSON.EscrowID)
	if err != nil {
		return settlement.Message{}, err
	}
	hostStats := make([]settlement.HostStat, 0, len(settlementJSON.HostStats))
	for _, hostStat := range settlementJSON.HostStats {
		hostStats = append(hostStats, settlement.HostStat(hostStat))
	}
	slotSignatures := make([]settlement.SlotSignature, 0, len(settlementJSON.Signatures))
	for _, slotSignature := range settlementJSON.Signatures {
		signature, err := decodeBase64Field(slotSignature.Signature, fmt.Sprintf("signature slot %d", slotSignature.SlotID))
		if err != nil {
			return settlement.Message{}, err
		}
		slotSignatures = append(slotSignatures, settlement.SlotSignature{SlotID: slotSignature.SlotID, Signature: signature})
	}
	return settlement.Message{
		EscrowID:       escrowID,
		StateRoot:      stateRoot,
		Nonce:          settlementJSON.Nonce,
		RestHash:       restHash,
		HostStats:      hostStats,
		SlotSignatures: slotSignatures,
		Fees:           settlementJSON.Fees,
		Version:        settlementJSON.StateRootAndProtocolVersion,
	}, nil
}

func parseRequiredUintEnv(name string) (uint64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return value, nil
}

func encodeManualUnorderedTxBody(msgAny []byte, timeout time.Time) []byte {
	var out []byte
	out = appendBytesField(out, 1, msgAny)
	out = appendVarintField(out, 4, 1)
	out = appendBytesField(out, 5, encodeManualTimestamp(timeout))
	return out
}

func encodeManualTimestamp(ts time.Time) []byte {
	var out []byte
	out = appendVarintField(out, 1, uint64(ts.Unix()))
	if nanos := ts.Nanosecond(); nanos != 0 {
		out = appendVarintField(out, 2, uint64(nanos))
	}
	return out
}

func encodeSecp256k1PublicKey(key []byte) []byte {
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

func encodeAuthInfo(publicKeyAny []byte, sequence uint64, feeDenom string, feeAmount uint64, gasLimit uint64) []byte {
	signerInfo := encodeSignerInfo(publicKeyAny, sequence)
	fee := encodeFee(feeDenom, feeAmount, gasLimit)
	var out []byte
	out = appendBytesField(out, 1, signerInfo)
	out = appendBytesField(out, 2, fee)
	return out
}

func encodeSignerInfo(publicKeyAny []byte, sequence uint64) []byte {
	var single []byte
	single = appendVarintField(single, 1, signModeDirect)
	var modeInfo []byte
	modeInfo = appendBytesField(modeInfo, 1, single)
	var out []byte
	out = appendBytesField(out, 1, publicKeyAny)
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

func appendVarintField(destination []byte, fieldNumber int, value uint64) []byte {
	destination = appendVarint(destination, uint64(fieldNumber<<3))
	return appendVarint(destination, value)
}

func appendBytesField(destination []byte, fieldNumber int, value []byte) []byte {
	destination = appendVarint(destination, uint64(fieldNumber<<3|2))
	destination = appendVarint(destination, uint64(len(value)))
	return append(destination, value...)
}

func appendVarint(destination []byte, value uint64) []byte {
	for value >= 0x80 {
		destination = append(destination, byte(value)|0x80)
		value >>= 7
	}
	return append(destination, byte(value))
}
