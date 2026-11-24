package settlementwire

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"devshard/gatewayprotocol/settlement"
)

const (
	fixedSettler                               = "gonka1settler0000000000000000000000000"
	chainHostStatsFieldNumber protowire.Number = 6
)

func fixedMessageFull() settlement.Message {
	return settlement.Message{
		EscrowID:  123,
		StateRoot: []byte("fixed-state-root-32-bytes-long!"),
		Nonce:     456,
		RestHash:  []byte("fixed-rest-hash-bytes"),
		HostStats: []settlement.HostStat{
			{SlotID: 1, Missed: 2, Invalid: 3, Cost: 400, RequiredValidations: 5, CompletedValidations: 6},
			{SlotID: 7, Missed: 8, Invalid: 9, Cost: 1000, RequiredValidations: 11, CompletedValidations: 12},
		},
		SlotSignatures: []settlement.SlotSignature{
			{SlotID: 1, Signature: []byte("fixed-signature-one")},
			{SlotID: 7, Signature: []byte("fixed-signature-two")},
		},
		Fees:    789,
		Version: "v1.2.3",
	}
}

func fixedMessageEmptyOptional() settlement.Message {
	return settlement.Message{
		EscrowID:  124,
		StateRoot: []byte("fixed-state-root-32-bytes-long!"),
		Nonce:     456,
		RestHash:  []byte("fixed-rest-hash-bytes"),
		Fees:      789,
		Version:   "v1.2.3",
	}
}

func fixedMessageZeroScalars() settlement.Message {
	return settlement.Message{
		EscrowID:  125,
		StateRoot: []byte("fixed-state-root-32-bytes-long!"),
		Nonce:     456,
		RestHash:  []byte("fixed-rest-hash-bytes"),
		HostStats: []settlement.HostStat{
			{SlotID: 0},
			{SlotID: 1, Cost: 400},
			{SlotID: 2, Missed: 3, Invalid: 1, Cost: 555, RequiredValidations: 10, CompletedValidations: 9},
		},
		SlotSignatures: []settlement.SlotSignature{
			{SlotID: 0, Signature: []byte("fixed-signature-zero")},
			{SlotID: 1, Signature: []byte("fixed-signature-one")},
			{SlotID: 2, Signature: []byte("fixed-signature-two")},
		},
		Fees:    789,
		Version: "v1.2.3",
	}
}

func loadGolden(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name+".b64"))
	if err != nil {
		t.Fatalf("reading golden %s: %v", name, err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("decoding golden %s: %v", name, err)
	}
	return decoded
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
//  1. Marshal a full settle message, one with empty host stats and signatures, and one whose slot 0 and counters are zero through `Marshal`.
//  2. Encode one host-stats entry without and one with validated and finished counts and map one slot signature through the package's mappings and marshal the chain entry alone.
//  3. For each case, load its golden fixture and assert the bytes match exactly.
func TestMarshalMatchesGoldens(t *testing.T) {
	settleFull := mustEncode(t, func() ([]byte, error) { return Marshal(fixedSettler, fixedMessageFull()) })
	settleEmptyOptional := mustEncode(t, func() ([]byte, error) { return Marshal(fixedSettler, fixedMessageEmptyOptional()) })
	settleZeroScalars := mustEncode(t, func() ([]byte, error) { return Marshal(fixedSettler, fixedMessageZeroScalars()) })
	hostStats := mustEncode(t, func() ([]byte, error) {
		return hostStatsEntry(settlement.HostStat{SlotID: 42, Missed: 3, Invalid: 1, Cost: 555, RequiredValidations: 10, CompletedValidations: 9})
	})
	hostStatsValidatedFinished := mustEncode(t, func() ([]byte, error) {
		return hostStatsEntry(settlement.HostStat{SlotID: 42, Missed: 3, Invalid: 1, Cost: 555, RequiredValidations: 10, CompletedValidations: 9, Validated: 4, Finished: 9})
	})
	slotSignature := mustEncode(t, chainSignatures([]settlement.SlotSignature{
		{SlotID: 42, Signature: []byte("standalone-fixed-signature")},
	})[0].Marshal)

	cases := []struct {
		name string
		got  []byte
	}{
		{"settle_msg_full", settleFull},
		{"settle_msg_empty_optional", settleEmptyOptional},
		{"settle_msg_zero_scalars", settleZeroScalars},
		{"settlement_host_stats", hostStats},
		{"settlement_host_stats_validated_finished", hostStatsValidatedFinished},
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

// Test flow:
//  1. Marshal a message whose host stats hold both counters, only validated, only finished and neither.
//  2. Walk the raw message fields and collect, for each host-stats entry, its varint fields by number.
//  3. Assert fields 1 to 6 are present, fields 7 and 8 carry the validated and finished counts, and a zero count writes no field.
func TestMarshalWritesValidatedAndFinishedAsFieldsSevenAndEight(t *testing.T) {
	message := fixedMessageFull()
	message.HostStats = []settlement.HostStat{
		{SlotID: 1, Missed: 2, Invalid: 3, Cost: 400, RequiredValidations: 5, CompletedValidations: 6, Validated: 4, Finished: 9},
		{SlotID: 2, Missed: 2, Invalid: 3, Cost: 400, RequiredValidations: 5, CompletedValidations: 6, Validated: 4},
		{SlotID: 3, Missed: 2, Invalid: 3, Cost: 400, RequiredValidations: 5, CompletedValidations: 6, Finished: 300},
		{SlotID: 4, Missed: 2, Invalid: 3, Cost: 400, RequiredValidations: 5, CompletedValidations: 6},
	}
	encoded := mustEncode(t, func() ([]byte, error) { return Marshal(fixedSettler, message) })

	var entries []map[protowire.Number]uint64
	for remaining := encoded; len(remaining) > 0; {
		number, wireType, tagLength := protowire.ConsumeTag(remaining)
		if tagLength < 0 {
			t.Fatalf("reading a message field tag: %v", protowire.ParseError(tagLength))
		}
		fieldLength := protowire.ConsumeFieldValue(number, wireType, remaining[tagLength:])
		if fieldLength < 0 {
			t.Fatalf("reading message field %d: %v", number, protowire.ParseError(fieldLength))
		}
		if number == chainHostStatsFieldNumber {
			entry, _ := protowire.ConsumeBytes(remaining[tagLength:])
			entries = append(entries, varintFields(t, entry))
		}
		remaining = remaining[tagLength+fieldLength:]
	}

	want := []map[protowire.Number]uint64{
		{1: 1, 2: 2, 3: 3, 4: 400, 5: 5, 6: 6, 7: 4, 8: 9},
		{1: 2, 2: 2, 3: 3, 4: 400, 5: 5, 6: 6, 7: 4},
		{1: 3, 2: 2, 3: 3, 4: 400, 5: 5, 6: 6, 8: 300},
		{1: 4, 2: 2, 3: 3, 4: 400, 5: 5, 6: 6},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("host-stats entries mismatch\n got:  %v\n want: %v", entries, want)
	}
}

func varintFields(t *testing.T, entry []byte) map[protowire.Number]uint64 {
	t.Helper()
	fields := map[protowire.Number]uint64{}
	for len(entry) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(entry)
		if tagLength < 0 || wireType != protowire.VarintType {
			t.Fatalf("host-stats entry holds field %d of wire type %d, want varints only", number, wireType)
		}
		value, valueLength := protowire.ConsumeVarint(entry[tagLength:])
		if valueLength < 0 {
			t.Fatalf("reading host-stats field %d: %v", number, protowire.ParseError(valueLength))
		}
		fields[number] = value
		entry = entry[tagLength+valueLength:]
	}
	return fields
}
