package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"devshard/signing"
)

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
	var settlement SettlementJSON
	if err := json.Unmarshal(settlementData, &settlement); err != nil {
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
		settlement,
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

func buildManualUnorderedSettleTx(signer *signing.Secp256k1Signer, chainID string, accountNumber uint64, feeDenom string, feeAmount, gasLimit uint64, settlement SettlementJSON, timeout time.Time) ([]byte, error) {
	msg, err := encodeMsgSettleDevshardEscrow(signer.Address(), settlement)
	if err != nil {
		return nil, err
	}
	bodyBytes := encodeManualUnorderedTxBody(encodeAny(settleEscrowMsgTypeURL, msg), timeout)
	pubKey := encodeAny(secp256k1PubKeyTypeURL, encodeSecp256k1PubKey(signer.CompressedPublicKeyBytes()))
	authInfoBytes := encodeAuthInfo(pubKey, 0, feeDenom, feeAmount, gasLimit)
	signDoc := encodeSignDoc(bodyBytes, authInfoBytes, chainID, accountNumber)
	sig, err := signer.Sign(signDoc)
	if err != nil {
		return nil, err
	}
	if len(sig) < 64 {
		return nil, fmt.Errorf("invalid signature length %d", len(sig))
	}
	return encodeTxRaw(bodyBytes, authInfoBytes, sig[:64]), nil
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
