package settlement

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

const settlementFileGolden = `{
  "escrow_id": "42",
  "version": "v7-1234",
  "state_root": "nNS5e02lKRR16ELGpv9kf27LoEHr63U2owk60nqaRos=",
  "nonce": 9,
  "fees": 7,
  "rest_hash": "cmVzdC1oYXNoLTMyLWJ5dGVzLTAxMjM0NTY3ODlhYmM=",
  "host_stats": [
    {
      "slot_id": 1,
      "missed": 5,
      "invalid": 6,
      "cost": 700,
      "required_validations": 8,
      "completed_validations": 7,
      "validated": 1,
      "finished": 9
    },
    {
      "slot_id": 3,
      "missed": 2,
      "invalid": 1,
      "cost": 500,
      "required_validations": 5,
      "completed_validations": 5,
      "validated": 3,
      "finished": 4
    },
    {
      "slot_id": 5,
      "missed": 1,
      "invalid": 2,
      "cost": 300,
      "required_validations": 4,
      "completed_validations": 3,
      "validated": 2,
      "finished": 6
    }
  ],
  "signatures": [
    {
      "slot_id": 0,
      "signature": "c2lnbmF0dXJlLXplcm8="
    },
    {
      "slot_id": 2,
      "signature": "c2lnbmF0dXJlLXR3bw=="
    },
    {
      "slot_id": 4,
      "signature": "c2lnbmF0dXJlLWZvdXI="
    }
  ]
}`

type chainSettlementFile struct {
	EscrowID                    string `json:"escrow_id"`
	StateRootAndProtocolVersion string `json:"version"`
	StateRoot                   string `json:"state_root"`
	Nonce                       uint64 `json:"nonce"`
	Fees                        uint64 `json:"fees"`
	RestHash                    string `json:"rest_hash"`
	HostStats                   []struct {
		SlotID               uint32 `json:"slot_id"`
		Missed               uint32 `json:"missed"`
		Invalid              uint32 `json:"invalid"`
		Cost                 uint64 `json:"cost"`
		RequiredValidations  uint32 `json:"required_validations,omitempty"`
		CompletedValidations uint32 `json:"completed_validations,omitempty"`
	} `json:"host_stats"`
	Signatures []struct {
		SlotID    uint32 `json:"slot_id"`
		Signature string `json:"signature"`
	} `json:"signatures"`
}

func settlementFileFixture(t *testing.T) Message {
	t.Helper()
	stateRoot, err := base64.StdEncoding.DecodeString("nNS5e02lKRR16ELGpv9kf27LoEHr63U2owk60nqaRos=")
	if err != nil {
		t.Fatal(err)
	}
	return Message{
		EscrowID:  42,
		StateRoot: stateRoot,
		Nonce:     9,
		RestHash:  []byte("rest-hash-32-bytes-0123456789abc"),
		HostStats: []HostStat{
			{SlotID: 1, Missed: 5, Invalid: 6, Cost: 700, RequiredValidations: 8, CompletedValidations: 7, Validated: 1, Finished: 9},
			{SlotID: 3, Missed: 2, Invalid: 1, Cost: 500, RequiredValidations: 5, CompletedValidations: 5, Validated: 3, Finished: 4},
			{SlotID: 5, Missed: 1, Invalid: 2, Cost: 300, RequiredValidations: 4, CompletedValidations: 3, Validated: 2, Finished: 6},
		},
		SlotSignatures: []SlotSignature{
			{SlotID: 0, Signature: []byte("signature-zero")},
			{SlotID: 2, Signature: []byte("signature-two")},
			{SlotID: 4, Signature: []byte("signature-four")},
		},
		Fees:    7,
		Version: "v7-1234",
	}
}

// Test flow:
//  1. Build a settlement message in which every field is non-zero and encode it as a settlement file with two-space indentation.
//  2. Compare the bytes with the golden text the original settlement encoder wrote for the same payload, root included.
//  3. Assert they are identical, so the file keeps its field names, order, base64 and omitted counters.
func TestTheSettlementFileIsByteIdenticalToTheGoldenEncoding(t *testing.T) {
	encoded, err := json.MarshalIndent(NewFile(settlementFileFixture(t)), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != settlementFileGolden {
		t.Fatalf("settlement file:\n%s\nwant:\n%s", encoded, settlementFileGolden)
	}
}

// Test flow:
//  1. Encode the all-non-zero settlement message as a file.
//  2. Parse it into a copy of the struct `inferenced tx inference settle-devshard-escrow` decodes with, then decode its numeric id and base64 fields as the command does.
//  3. Assert every field the command reads equals the message's, in the message's slot order.
func TestTheChainCommandReadsEveryFieldOfTheSettlementFile(t *testing.T) {
	message := settlementFileFixture(t)
	encoded, err := json.Marshal(NewFile(message))
	if err != nil {
		t.Fatal(err)
	}

	var parsed chainSettlementFile
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		t.Fatal(err)
	}

	escrowID, err := strconv.ParseUint(parsed.EscrowID, 10, 64)
	if err != nil || escrowID != message.EscrowID {
		t.Fatalf("escrow_id = %q (%v), want %d", parsed.EscrowID, err, message.EscrowID)
	}
	if parsed.StateRootAndProtocolVersion != message.Version || parsed.Nonce != message.Nonce || parsed.Fees != message.Fees {
		t.Fatalf("version, nonce, fees = %q, %d, %d, want %q, %d, %d", parsed.StateRootAndProtocolVersion, parsed.Nonce, parsed.Fees, message.Version, message.Nonce, message.Fees)
	}
	for name, pair := range map[string][2]string{
		"state_root": {parsed.StateRoot, string(message.StateRoot)},
		"rest_hash":  {parsed.RestHash, string(message.RestHash)},
	} {
		decoded, err := base64.StdEncoding.DecodeString(pair[0])
		if err != nil || string(decoded) != pair[1] {
			t.Fatalf("%s = %q (%v), want the message's bytes", name, pair[0], err)
		}
	}
	if len(parsed.HostStats) != len(message.HostStats) || len(parsed.Signatures) != len(message.SlotSignatures) {
		t.Fatalf("host stats %d, signatures %d, want %d and %d", len(parsed.HostStats), len(parsed.Signatures), len(message.HostStats), len(message.SlotSignatures))
	}
	for index, want := range message.HostStats {
		got := parsed.HostStats[index]
		if got.SlotID != want.SlotID || got.Missed != want.Missed || got.Invalid != want.Invalid || got.Cost != want.Cost ||
			got.RequiredValidations != want.RequiredValidations || got.CompletedValidations != want.CompletedValidations {
			t.Fatalf("host stats[%d] = %+v, want %+v", index, got, want)
		}
	}
	for index, want := range message.SlotSignatures {
		got := parsed.Signatures[index]
		decoded, err := base64.StdEncoding.DecodeString(got.Signature)
		if got.SlotID != want.SlotID || err != nil || string(decoded) != string(want.Signature) {
			t.Fatalf("signatures[%d] = %+v (%v), want slot %d and %q", index, got, err, want.SlotID, want.Signature)
		}
	}
}

// Test flow:
//  1. Encode a settlement message that carries no host stats, no signatures and zero counters.
//  2. Inspect the JSON text.
//  3. Assert the two lists are empty arrays rather than null, and the four optional counters are absent.
func TestAnEmptySettlementFileKeepsItsListsAsArraysAndOmitsZeroCounters(t *testing.T) {
	encoded, err := json.Marshal(NewFile(Message{EscrowID: 1, Version: "v7"}))
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"host_stats":[]`) || !strings.Contains(text, `"signatures":[]`) {
		t.Fatalf("file = %s, want empty arrays for host_stats and signatures", text)
	}
	withStat, err := json.Marshal(NewFile(Message{HostStats: []HostStat{{SlotID: 2, Cost: 3}}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"required_validations", "completed_validations", "validated", "finished"} {
		if strings.Contains(string(withStat), absent) {
			t.Fatalf("file = %s, want %s omitted while zero", withStat, absent)
		}
	}
}
