package chain

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/signing"
)

// Fixed encoder inputs shared by every golden fixture below; must match the
// values the golden corpus under testdata/txgold was generated from.
const (
	fixedFeeDenom      = "ngonka"
	fixedFeeAmount     = uint64(1_000_000)
	fixedGasLimit      = uint64(500_000)
	fixedChainID       = "gonka-devshard-golden"
	fixedAccountNumber = uint64(7)
	fixedSequence      = uint64(0)
)

var fixedTTL = time.Unix(1700000000, 0).UTC()

func fixedSigner(t *testing.T) *signing.Secp256k1Signer {
	t.Helper()
	signer, err := signing.SignerFromHex(strings.Repeat("11", 32))
	if err != nil {
		t.Fatalf("fixed signer: %v", err)
	}
	return signer
}

func loadGolden(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "txgold", name+".b64"))
	if err != nil {
		t.Fatalf("reading golden %s: %v", name, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("decoding golden %s: %v", name, err)
	}
	return decoded
}

// txFixtures composes the shared derived byte values several goldens nest,
// e.g. sign_doc wraps unordered_tx_body and auth_info.
type txFixtures struct {
	pubKeyBytes       []byte
	pubKeyAny         []byte
	anyCreateMsgBytes []byte
	unorderedTxBody   []byte
	authInfoBytes     []byte
}

func buildTxFixtures(t *testing.T, signer *signing.Secp256k1Signer) txFixtures {
	t.Helper()
	pubKeyBytes := encodeSecp256k1PubKey(signer.CompressedPublicKeyBytes())
	pubKeyAny := encodeAny(secp256k1PubKeyTypeURL, pubKeyBytes)
	createMsgBytes := loadGolden(t, "create_msg")
	anyCreateMsgBytes := encodeAny(createEscrowMsgTypeURL, createMsgBytes)
	unorderedTxBody := encodeUnorderedTxBody(anyCreateMsgBytes, fixedTTL)
	authInfoBytes := encodeAuthInfo(pubKeyAny, fixedSequence, fixedFeeDenom, fixedFeeAmount, fixedGasLimit)
	return txFixtures{
		pubKeyBytes:       pubKeyBytes,
		pubKeyAny:         pubKeyAny,
		anyCreateMsgBytes: anyCreateMsgBytes,
		unorderedTxBody:   unorderedTxBody,
		authInfoBytes:     authInfoBytes,
	}
}

// Test flow:
//  1. Build shared tx fixtures (pub key, wrapped create message, tx body, auth info) from the fixed signer and inputs.
//  2. Encode each remaining primitive (signer info, fee, sign doc, tx raw, timestamp, tx hash) alongside the fixtures.
//  3. For each named case, load its golden fixture and assert the bytes match exactly.
func TestEncodersMatchGoldens(t *testing.T) {
	fixtures := buildTxFixtures(t, fixedSigner(t))
	placeholderSignature := bytes.Repeat([]byte{0xCD}, 64)

	cases := []struct {
		name string
		got  []byte
	}{
		{"secp256k1_pubkey", fixtures.pubKeyBytes},
		{"any_create_msg", fixtures.anyCreateMsgBytes},
		{"unordered_tx_body", fixtures.unorderedTxBody},
		{"auth_info", fixtures.authInfoBytes},
		{"signer_info", encodeSignerInfo(fixtures.pubKeyAny, fixedSequence)},
		{"fee", encodeFee(fixedFeeDenom, fixedFeeAmount, fixedGasLimit)},
		{"sign_doc", encodeSignDoc(fixtures.unorderedTxBody, fixtures.authInfoBytes, fixedChainID, fixedAccountNumber)},
		{"tx_raw", encodeTxRaw(fixtures.unorderedTxBody, fixtures.authInfoBytes, placeholderSignature)},
		{"timestamp", encodeTimestamp(fixedTTL)},
		{"tx_hash", []byte(txHashFromBytes([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}))},
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

// readLengthDelimitedField decodes one protobuf tag+length-delimited value
// via the standard library varint reader, independent of this package's encoders.
func readLengthDelimitedField(t *testing.T, b []byte) (field int, value []byte, rest []byte) {
	t.Helper()
	tag, n := binary.Uvarint(b)
	if n <= 0 {
		t.Fatalf("decoding tag: invalid varint")
	}
	b = b[n:]
	if wireType := tag & 0x7; wireType != 2 {
		t.Fatalf("wire type = %d, want 2 (length-delimited)", wireType)
	}
	length, n := binary.Uvarint(b)
	if n <= 0 {
		t.Fatalf("decoding length: invalid varint")
	}
	b = b[n:]
	if uint64(len(b)) < length {
		t.Fatalf("value shorter than declared length %d", length)
	}
	return int(tag >> 3), b[:length], b[length:]
}

// Test flow:
//  1. Encode an arbitrary type URL and value with encodeAny.
//  2. Decode the first field and assert it is field 1 and carries the type URL.
//  3. Decode the second field and assert it is field 2, carries the value, and no trailing bytes remain.
func TestEncodeAnySelfConsistency(t *testing.T) {
	typeURL := "/example.SelfConsistencyType"
	value := []byte("arbitrary-any-value-bytes")
	got := encodeAny(typeURL, value)

	field1, typeURLBytes, rest := readLengthDelimitedField(t, got)
	if field1 != 1 {
		t.Fatalf("first field number = %d, want 1", field1)
	}
	if string(typeURLBytes) != typeURL {
		t.Fatalf("field 1 value = %q, want %q", typeURLBytes, typeURL)
	}

	field2, valueBytes, rest := readLengthDelimitedField(t, rest)
	if field2 != 2 {
		t.Fatalf("second field number = %d, want 2", field2)
	}
	if !bytes.Equal(valueBytes, value) {
		t.Fatalf("field 2 value = %x, want %x", valueBytes, value)
	}
	if len(rest) != 0 {
		t.Fatalf("unexpected trailing bytes: %x", rest)
	}
}

// Test flow:
//  1. For each field number in a fixed list, call appendVarintField with value 0.
//  2. Assert the resulting first byte equals field<<3.
func TestAppendVarintFieldTag(t *testing.T) {
	for _, field := range []int{1, 2, 3, 4, 5, 8, 9} {
		got := appendVarintField(nil, field, 0)
		wantTag := byte(field << 3)
		if len(got) == 0 || got[0] != wantTag {
			t.Fatalf("field %d: tag byte = %v, want %#x", field, got, wantTag)
		}
	}
}

// Test flow:
//  1. For each field number in a fixed list, call appendBytesField with a one-byte value.
//  2. Assert the resulting first byte equals field<<3|2.
func TestAppendBytesFieldTag(t *testing.T) {
	for _, field := range []int{1, 2, 3, 4, 5, 8, 9} {
		got := appendBytesField(nil, field, []byte("x"))
		wantTag := byte(field<<3 | 2)
		if len(got) == 0 || got[0] != wantTag {
			t.Fatalf("field %d: tag byte = %v, want %#x", field, got, wantTag)
		}
	}
}

// Test flow:
//  1. Build a timestamp with a nonzero nanosecond component.
//  2. Call encodeTimestamp on it.
//  3. Independently encode the expected seconds and nanos fields as varints and assert the two byte slices match.
func TestEncodeTimestampIncludesNanosWhenNonzero(t *testing.T) {
	ts := time.Unix(1700000000, 123).UTC()
	got := encodeTimestamp(ts)

	var want []byte
	want = binary.AppendUvarint(want, 1<<3)
	want = binary.AppendUvarint(want, uint64(ts.Unix()))
	want = binary.AppendUvarint(want, 2<<3)
	want = binary.AppendUvarint(want, uint64(ts.Nanosecond()))

	if !bytes.Equal(got, want) {
		t.Fatalf("encodeTimestamp with nonzero nanos = %x, want %x", got, want)
	}
}
