//go:build txgold

package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/signing"
)

// TestGenerateTxGoldens runs each deterministic old tx encoder over fixed
// inputs and writes its output as the byte-exact oracle cmd/gateway/chain
// must reproduce. Run manually:
// go test ./cmd/devshardctl/ -tags txgold -run TestGenerateTxGoldens -count=1
func TestGenerateTxGoldens(t *testing.T) {
	goldensDir := filepath.Join("..", "gateway", "chain", "testdata", "txgold")
	if err := os.MkdirAll(goldensDir, 0o755); err != nil {
		t.Fatalf("creating goldens dir: %v", err)
	}

	signer, err := signing.SignerFromHex(strings.Repeat("11", 32))
	if err != nil {
		t.Fatalf("fixed signer: %v", err)
	}

	const (
		fixedCreator       = "gonka1creator0000000000000000000000000"
		fixedSettler       = "gonka1settler0000000000000000000000000"
		fixedModelID       = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"
		fixedAmount        = uint64(1_000_000)
		fixedFeeDenom      = "ngonka"
		fixedFeeAmount     = uint64(1_000_000)
		fixedGasLimit      = uint64(500_000)
		fixedChainID       = "gonka-devshard-golden"
		fixedAccountNumber = uint64(7)
		fixedSequence      = uint64(0) // unordered tx: sequence is always hardcoded 0
	)
	fixedTTL := time.Unix(1700000000, 0).UTC()

	settlementFull := SettlementJSON{
		EscrowID:                    "123",
		StateRootAndProtocolVersion: "v1.2.3",
		StateRoot:                   base64.StdEncoding.EncodeToString([]byte("fixed-state-root-32-bytes-long!")),
		Nonce:                       456,
		Fees:                        789,
		RestHash:                    base64.StdEncoding.EncodeToString([]byte("fixed-rest-hash-bytes")),
		HostStats: []HostStatsJSON{
			{SlotID: 1, Missed: 2, Invalid: 3, Cost: 400, RequiredValidations: 5, CompletedValidations: 6},
			{SlotID: 7, Missed: 8, Invalid: 9, Cost: 1000, RequiredValidations: 11, CompletedValidations: 12},
		},
		Signatures: []SlotSignatureJSON{
			{SlotID: 1, Signature: base64.StdEncoding.EncodeToString([]byte("fixed-signature-one"))},
			{SlotID: 7, Signature: base64.StdEncoding.EncodeToString([]byte("fixed-signature-two"))},
		},
	}
	settlementEmptyOptional := SettlementJSON{
		EscrowID:                    "124",
		StateRootAndProtocolVersion: "v1.2.3",
		StateRoot:                   base64.StdEncoding.EncodeToString([]byte("fixed-state-root-32-bytes-long!")),
		Nonce:                       456,
		Fees:                        789,
		RestHash:                    base64.StdEncoding.EncodeToString([]byte("fixed-rest-hash-bytes")),
	}

	createMsgBytes := encodeMsgCreateDevshardEscrow(fixedCreator, fixedAmount, fixedModelID)
	settleMsgFullBytes, err := encodeMsgSettleDevshardEscrow(fixedSettler, settlementFull)
	if err != nil {
		t.Fatalf("encode settle msg (full): %v", err)
	}
	settleMsgEmptyOptionalBytes, err := encodeMsgSettleDevshardEscrow(fixedSettler, settlementEmptyOptional)
	if err != nil {
		t.Fatalf("encode settle msg (empty-optional): %v", err)
	}
	hostStatsBytes := encodeSettlementHostStats(HostStatsJSON{SlotID: 42, Missed: 3, Invalid: 1, Cost: 555, RequiredValidations: 10, CompletedValidations: 9})
	slotSigBytes, err := encodeSlotSignature(SlotSignatureJSON{SlotID: 42, Signature: base64.StdEncoding.EncodeToString([]byte("standalone-fixed-signature"))})
	if err != nil {
		t.Fatalf("encode slot signature: %v", err)
	}
	pubKeyBytes := encodeSecp256k1PubKey(signer.CompressedPublicKeyBytes())
	anyCreateMsgBytes := encodeAny(createEscrowMsgTypeURL, createMsgBytes)
	pubKeyAnyBytes := encodeAny(secp256k1PubKeyTypeURL, pubKeyBytes)
	authInfoBytes := encodeAuthInfo(pubKeyAnyBytes, fixedSequence, fixedFeeDenom, fixedFeeAmount, fixedGasLimit)
	signerInfoBytes := encodeSignerInfo(pubKeyAnyBytes, fixedSequence)
	feeBytes := encodeFee(fixedFeeDenom, fixedFeeAmount, fixedGasLimit)
	unorderedTxBodyBytes := encodeUnorderedTxBody(anyCreateMsgBytes, fixedTTL)
	signDocBytes := encodeSignDoc(unorderedTxBodyBytes, authInfoBytes, fixedChainID, fixedAccountNumber)
	placeholderSignature := bytes.Repeat([]byte{0xCD}, 64) // opaque fixed bytes; encodeTxRaw doesn't validate signatures
	txRawBytes := encodeTxRaw(unorderedTxBodyBytes, authInfoBytes, placeholderSignature)
	timestampBytes := encodeTimestamp(fixedTTL)
	// txHashFromBytes returns a hex string; golden its UTF-8 bytes like every other case.
	txHashBytes := []byte(txHashFromBytes([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}))

	cases := []struct {
		name string
		data []byte
	}{
		{"create_msg", createMsgBytes},
		{"settle_msg_full", settleMsgFullBytes},
		{"settle_msg_empty_optional", settleMsgEmptyOptionalBytes},
		{"settlement_host_stats", hostStatsBytes},
		{"slot_signature", slotSigBytes},
		{"secp256k1_pubkey", pubKeyBytes},
		{"any_create_msg", anyCreateMsgBytes},
		{"auth_info", authInfoBytes},
		{"signer_info", signerInfoBytes},
		{"fee", feeBytes},
		{"sign_doc", signDocBytes},
		{"tx_raw", txRawBytes},
		{"tx_hash", txHashBytes},
		{"unordered_tx_body", unorderedTxBodyBytes},
		{"timestamp", timestampBytes},
	}

	generated := 0
	for _, tc := range cases {
		encoded := base64.StdEncoding.EncodeToString(tc.data)
		path := filepath.Join(goldensDir, tc.name+".b64")
		if err := os.WriteFile(path, []byte(encoded+"\n"), 0o644); err != nil {
			t.Fatalf("%s: writing golden: %v", tc.name, err)
		}
		generated++
	}
	t.Logf("generated %d tx-encoding goldens", generated)
	if generated == 0 {
		t.Fatal("no goldens generated")
	}
}
