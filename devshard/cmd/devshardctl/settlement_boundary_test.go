package main

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestSettlementWireBoundaryBaseline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input SettlementJSON
		want  string
	}{
		{"empty fields", SettlementJSON{EscrowID: "1"}, "0a0010011a0020002a0040004a00"},
		{"empty nested fields", SettlementJSON{EscrowID: "1", HostStats: []HostStatsJSON{{}}, Signatures: []SlotSignatureJSON{{}}}, "0a0010011a0020002a00320c0800100018002000280030003a040800120040004a00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeMsgSettleDevshardEscrow("", tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(got) != tc.want {
				t.Fatalf("wire=%x; want %s", got, tc.want)
			}
		})
	}
}

func TestSettlementJSONCompatibilityBaseline(t *testing.T) {
	data, err := json.Marshal(SettlementJSON{StateRootAndProtocolVersion: "v6", HostStats: []HostStatsJSON{{SlotID: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version":"v6"`) || strings.Contains(string(data), "state_root_and_protocol_version") || strings.Contains(string(data), "required_validations") {
		t.Fatalf("JSON changed: %s", data)
	}
	var input SettlementJSON
	if err := json.Unmarshal([]byte(`{"host_stats":[{"missed":4294967296}]}`), &input); err == nil {
		t.Fatal("uint32 overflow was accepted")
	}
	for _, tc := range []struct {
		input  SettlementJSON
		prefix string
	}{
		{SettlementJSON{}, "parse escrow_id:"},
		{SettlementJSON{EscrowID: "1", StateRoot: "!"}, "decode state_root:"},
		{SettlementJSON{EscrowID: "1", RestHash: "!"}, "decode rest_hash:"},
		{SettlementJSON{EscrowID: "1", Signatures: []SlotSignatureJSON{{SlotID: 3, Signature: "!"}}}, "decode signature for slot 3:"},
	} {
		_, err := encodeMsgSettleDevshardEscrow("", tc.input)
		if err == nil || !strings.HasPrefix(err.Error(), tc.prefix) {
			t.Fatalf("error=%v; want prefix %q", err, tc.prefix)
		}
	}
}

func TestSettleParamsFromJSONPreservesSignedCounters(t *testing.T) {
	for _, tc := range []struct {
		value uint32
		want  int32
	}{{0, 0}, {1<<31 - 1, 2147483647}, {1 << 31, -2147483648}, {1<<32 - 1, -1}} {
		params, err := settleParamsFromJSON(SettlementJSON{
			EscrowID: "1", StateRoot: "AQ==", RestHash: "Ag==",
			HostStats: []HostStatsJSON{{
				SlotID: 2, Missed: tc.value, Invalid: tc.value, Cost: 1<<64 - 1,
				RequiredValidations: tc.value, CompletedValidations: tc.value,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		got := params.HostStats[0]
		if got.Missed != tc.want || got.Invalid != tc.want || got.RequiredValidations != tc.want || got.CompletedValidations != tc.want {
			t.Fatalf("counter %d converted to %+v; want %d", tc.value, got, tc.want)
		}
		if got.SlotID != 2 || got.Cost != 1<<64-1 {
			t.Fatalf("slot or full-width cost changed: %+v", got)
		}
	}
}
