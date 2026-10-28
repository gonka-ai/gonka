package chain

import (
	"bytes"
	"testing"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// Fixed message-encoder inputs; must match the values chain_txgold_test.go used
// to generate testdata/txgold/{create_msg,settle_msg_full,
// settle_msg_empty_optional,settlement_host_stats,slot_signature}.b64.
const (
	fixedCreator = "gonka1creator0000000000000000000000000"
	fixedSettler = "gonka1settler0000000000000000000000000"
	fixedModelID = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"
	fixedAmount  = uint64(1_000_000)
)

func fixedSettlementFull() SettlementInput {
	return SettlementInput{
		EscrowID:  123,
		StateRoot: []byte("fixed-state-root-32-bytes-long!"),
		Nonce:     456,
		RestHash:  []byte("fixed-rest-hash-bytes"),
		HostStats: []SettlementHostStat{
			{SlotID: 1, Missed: 2, Invalid: 3, Cost: 400, RequiredValidations: 5, CompletedValidations: 6},
			{SlotID: 7, Missed: 8, Invalid: 9, Cost: 1000, RequiredValidations: 11, CompletedValidations: 12},
		},
		SlotSigs: []SettlementSlotSig{
			{SlotID: 1, Signature: []byte("fixed-signature-one")},
			{SlotID: 7, Signature: []byte("fixed-signature-two")},
		},
		Fees:    789,
		Version: "v1.2.3",
	}
}

func fixedSettlementEmptyOptional() SettlementInput {
	return SettlementInput{
		EscrowID:  124,
		StateRoot: []byte("fixed-state-root-32-bytes-long!"),
		Nonce:     456,
		RestHash:  []byte("fixed-rest-hash-bytes"),
		Fees:      789,
		Version:   "v1.2.3",
	}
}

// Test flow:
//  1. Marshal a settlement host-stats message and a slot-signature message directly.
//  2. Encode a create-escrow message and two settle-escrow messages (full and empty-optional) via the message encoders.
//  3. For each encoded case, load its golden fixture and assert the bytes match exactly.
func TestMessageEncodersMatchGoldens(t *testing.T) {
	hostStats, err := (&inferencetypes.DevshardSettlementHostStats{
		SlotId: 42, Missed: 3, Invalid: 1, Cost: 555, RequiredValidations: 10, CompletedValidations: 9,
	}).Marshal()
	if err != nil {
		t.Fatalf("marshal host stats: %v", err)
	}
	slotSignature, err := (&inferencetypes.DevshardSlotSignature{
		SlotId: 42, Signature: []byte("standalone-fixed-signature"),
	}).Marshal()
	if err != nil {
		t.Fatalf("marshal slot signature: %v", err)
	}
	createMsg := mustEncode(t, func() ([]byte, error) {
		return encodeMsgCreateDevshardEscrow(fixedCreator, fixedAmount, fixedModelID)
	})
	settleFull := mustEncode(t, func() ([]byte, error) {
		return encodeMsgSettleDevshardEscrow(fixedSettler, fixedSettlementFull())
	})
	settleEmptyOptional := mustEncode(t, func() ([]byte, error) {
		return encodeMsgSettleDevshardEscrow(fixedSettler, fixedSettlementEmptyOptional())
	})

	cases := []struct {
		name string
		got  []byte
	}{
		{"create_msg", createMsg},
		{"settle_msg_full", settleFull},
		{"settle_msg_empty_optional", settleEmptyOptional},
		{"settlement_host_stats", hostStats},
		{"slot_signature", slotSignature},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			want := loadGolden(t, testCase.name)
			if !bytes.Equal(testCase.got, want) {
				t.Fatalf("%s mismatch\n got:  %x\n want: %x", testCase.name, testCase.got, want)
			}
		})
	}
}

func mustEncode(t *testing.T, encode func() ([]byte, error)) []byte {
	t.Helper()
	encoded, err := encode()
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return encoded
}

// Test flow:
//  1. Sign an arbitrary message with a real secp256k1 signer to get a genuine 65-byte signature.
//  2. Call truncateSignature on it.
//  3. Assert the result is 64 bytes and equals the signature's first 64 bytes (r||s).
func TestTruncateSignatureRealSecp256k1SignatureTruncatesTo64(t *testing.T) {
	signer := fixedSigner(t)
	sig, err := signer.Sign([]byte("arbitrary message"))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if len(sig) != 65 {
		t.Fatalf("test setup: secp256k1 signature length = %d, want 65", len(sig))
	}

	got, err := truncateSignature(sig)
	if err != nil {
		t.Fatalf("truncateSignature: %v", err)
	}
	if len(got) != 64 {
		t.Fatalf("truncated length = %d, want 64", len(got))
	}
	if !bytes.Equal(got, sig[:64]) {
		t.Fatalf("truncated = %x, want %x", got, sig[:64])
	}
}

// Test flow:
//  1. Call truncateSignature with a 63-byte input.
//  2. Assert it returns an error with the exact "invalid signature length 63" message.
func TestTruncateSignatureShortSignatureErrors(t *testing.T) {
	_, err := truncateSignature(make([]byte, 63))
	if err == nil {
		t.Fatal("want error for a 63-byte signature, got nil")
	}
	const wantMsg = "invalid signature length 63"
	if err.Error() != wantMsg {
		t.Fatalf("error = %q, want %q", err.Error(), wantMsg)
	}
}

// Test flow:
//  1. Build a create-escrow tx with the fixed signer and inputs.
//  2. Decode the outer TxRaw into its body, auth-info and signature fields and assert their field numbers and that no trailing bytes remain.
//  3. Assert the signature is 64 bytes.
//  4. Recompute the expected body and auth-info bytes from the golden-matched sub-encoders and assert they match the decoded ones.
//  5. Recompute the expected signature over the reconstructed sign-doc and assert it matches the decoded signature, so the builder is pinned to signing the correct bytes rather than just producing a signature of the right length.
func TestBuildCreateEscrowTxStructural(t *testing.T) {
	signer := fixedSigner(t)
	txBytes, err := buildCreateEscrowTx(signer, fixedChainID, fixedAccountNumber, fixedFeeDenom, fixedFeeAmount, fixedGasLimit, fixedAmount, fixedModelID, fixedTTL)
	if err != nil {
		t.Fatalf("buildCreateEscrowTx: %v", err)
	}

	field1, bodyBytes, rest := readLengthDelimitedField(t, txBytes)
	if field1 != 1 {
		t.Fatalf("first field = %d, want 1 (body)", field1)
	}
	field2, authInfoBytes, rest := readLengthDelimitedField(t, rest)
	if field2 != 2 {
		t.Fatalf("second field = %d, want 2 (auth info)", field2)
	}
	field3, sigBytes, rest := readLengthDelimitedField(t, rest)
	if field3 != 3 {
		t.Fatalf("third field = %d, want 3 (signature)", field3)
	}
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing bytes: %x", rest)
	}
	if len(sigBytes) != 64 {
		t.Fatalf("signature length = %d, want 64", len(sigBytes))
	}

	wantMsg := mustEncode(t, func() ([]byte, error) {
		return encodeMsgCreateDevshardEscrow(signer.Address(), fixedAmount, fixedModelID)
	})
	wantBody := encodeUnorderedTxBody(encodeAny(createEscrowMsgTypeURL, wantMsg), fixedTTL)
	if !bytes.Equal(bodyBytes, wantBody) {
		t.Fatalf("body = %x, want %x", bodyBytes, wantBody)
	}

	pubKeyAny := encodeAny(secp256k1PubKeyTypeURL, encodeSecp256k1PubKey(signer.CompressedPublicKeyBytes()))
	wantAuthInfo := encodeAuthInfo(pubKeyAny, 0, fixedFeeDenom, fixedFeeAmount, fixedGasLimit)
	if !bytes.Equal(authInfoBytes, wantAuthInfo) {
		t.Fatalf("auth info = %x, want %x", authInfoBytes, wantAuthInfo)
	}

	wantSig, err := signer.Sign(encodeSignDoc(wantBody, wantAuthInfo, fixedChainID, fixedAccountNumber))
	if err != nil {
		t.Fatalf("recompute expected signature: %v", err)
	}
	if !bytes.Equal(sigBytes, wantSig[:64]) {
		t.Fatalf("signature = %x, want %x", sigBytes, wantSig[:64])
	}
}

// Test flow:
//  1. Call buildCreateEscrowTx with a blank (whitespace-only) chain id.
//  2. Assert it returns an error.
func TestBuildCreateEscrowTxEmptyChainIDErrors(t *testing.T) {
	_, err := buildCreateEscrowTx(fixedSigner(t), "   ", fixedAccountNumber, fixedFeeDenom, fixedFeeAmount, fixedGasLimit, fixedAmount, fixedModelID, fixedTTL)
	if err == nil {
		t.Fatal("want error for a blank chain id, got nil")
	}
}

// Test flow:
//  1. Build a settle-escrow tx with the fixed signer, a decoupled settler address, and a full settlement input.
//  2. Decode the outer TxRaw into its body, auth-info and signature fields and assert their field numbers and that no trailing bytes remain.
//  3. Assert the signature is 64 bytes.
//  4. Recompute the expected body and auth-info bytes from the golden-matched sub-encoders and assert they match the decoded ones.
//  5. Recompute the expected signature over the reconstructed sign-doc and assert it matches the decoded signature, so the builder is pinned to signing the correct bytes rather than just producing a signature of the right length.
func TestBuildSettleEscrowTxStructural(t *testing.T) {
	signer := fixedSigner(t)
	input := fixedSettlementFull()
	txBytes, err := buildSettleEscrowTx(signer, fixedChainID, fixedAccountNumber, fixedFeeDenom, fixedFeeAmount, fixedGasLimit, fixedSettler, input, fixedTTL)
	if err != nil {
		t.Fatalf("buildSettleEscrowTx: %v", err)
	}

	field1, bodyBytes, rest := readLengthDelimitedField(t, txBytes)
	if field1 != 1 {
		t.Fatalf("first field = %d, want 1 (body)", field1)
	}
	field2, authInfoBytes, rest := readLengthDelimitedField(t, rest)
	if field2 != 2 {
		t.Fatalf("second field = %d, want 2 (auth info)", field2)
	}
	field3, sigBytes, rest := readLengthDelimitedField(t, rest)
	if field3 != 3 {
		t.Fatalf("third field = %d, want 3 (signature)", field3)
	}
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing bytes: %x", rest)
	}
	if len(sigBytes) != 64 {
		t.Fatalf("signature length = %d, want 64", len(sigBytes))
	}

	wantMsg := mustEncode(t, func() ([]byte, error) {
		return encodeMsgSettleDevshardEscrow(fixedSettler, input)
	})
	wantBody := encodeUnorderedTxBody(encodeAny(settleEscrowMsgTypeURL, wantMsg), fixedTTL)
	if !bytes.Equal(bodyBytes, wantBody) {
		t.Fatalf("body = %x, want %x", bodyBytes, wantBody)
	}

	pubKeyAny := encodeAny(secp256k1PubKeyTypeURL, encodeSecp256k1PubKey(signer.CompressedPublicKeyBytes()))
	wantAuthInfo := encodeAuthInfo(pubKeyAny, 0, fixedFeeDenom, fixedFeeAmount, fixedGasLimit)
	if !bytes.Equal(authInfoBytes, wantAuthInfo) {
		t.Fatalf("auth info = %x, want %x", authInfoBytes, wantAuthInfo)
	}

	wantSig, err := signer.Sign(encodeSignDoc(wantBody, wantAuthInfo, fixedChainID, fixedAccountNumber))
	if err != nil {
		t.Fatalf("recompute expected signature: %v", err)
	}
	if !bytes.Equal(sigBytes, wantSig[:64]) {
		t.Fatalf("signature = %x, want %x", sigBytes, wantSig[:64])
	}
}

// Test flow:
//  1. Call buildSettleEscrowTx with an empty chain id.
//  2. Assert it returns an error.
func TestBuildSettleEscrowTxEmptyChainIDErrors(t *testing.T) {
	_, err := buildSettleEscrowTx(fixedSigner(t), "", fixedAccountNumber, fixedFeeDenom, fixedFeeAmount, fixedGasLimit, fixedSettler, fixedSettlementFull(), fixedTTL)
	if err == nil {
		t.Fatal("want error for empty chain id, got nil")
	}
}
