package gatewayproto

import (
	"bytes"
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func fullGoldenSettlement() SettlementInput {
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

func emptyGoldenSettlement() SettlementInput {
	return SettlementInput{
		EscrowID:  124,
		StateRoot: []byte("fixed-state-root-32-bytes-long!"),
		Nonce:     456,
		RestHash:  []byte("fixed-rest-hash-bytes"),
		Fees:      789,
		Version:   "v1.2.3",
	}
}

func TestSettlementFullWireGoldensForBothProfiles(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input SettlementInput
	}{
		{"settle_msg_full", fullGoldenSettlement()},
		{"settle_msg_empty_optional", emptyGoldenSettlement()},
	} {
		fixture, err := os.ReadFile("testdata/" + tc.name + ".b64")
		if err != nil {
			t.Fatal(err)
		}
		want, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(fixture)))
		if err != nil {
			t.Fatal(err)
		}
		for _, profile := range []struct {
			name   string
			encode func(string, SettlementInput) ([]byte, error)
		}{
			{"legacy", MarshalLegacySettlement}, {"proto", MarshalProtoSettlement},
		} {
			t.Run(tc.name+"/"+profile.name, func(t *testing.T) {
				got, err := profile.encode("gonka1settler0000000000000000000000000", tc.input)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("wire=%x; want %x", got, want)
				}
			})
		}
	}
}
