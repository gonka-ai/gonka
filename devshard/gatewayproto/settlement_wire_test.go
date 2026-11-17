package gatewayproto

import (
	"encoding/hex"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"testing"
)

func TestSettlementWireProfilesKeepTheirBaselines(t *testing.T) {
	input := SettlementInput{EscrowID: 1}
	for _, tc := range []struct {
		name   string
		encode func(string, SettlementInput) ([]byte, error)
		want   string
	}{
		{"legacy", MarshalLegacySettlement, "0a0010011a0020002a0040004a00"},
		{"proto", MarshalProtoSettlement, "1001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.encode("", input)
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(data) != tc.want {
				t.Fatalf("wire=%x; want %s", data, tc.want)
			}
		})
	}
}

func TestSettlementWireProfilesPreserveOrderedLists(t *testing.T) {
	input := SettlementInput{EscrowID: 1, HostStats: []SettlementHostStat{{SlotID: 2, Missed: 4294967295}, {SlotID: 1}}, SlotSigs: []SettlementSlotSig{{SlotID: 2}, {SlotID: 1}}}
	for _, encode := range []func(string, SettlementInput) ([]byte, error){MarshalLegacySettlement, MarshalProtoSettlement} {
		data, err := encode("settler", input)
		if err != nil {
			t.Fatal(err)
		}
		var decoded inferencetypes.MsgSettleDevshardEscrow
		if err := decoded.Unmarshal(data); err != nil {
			t.Fatal(err)
		}
		if decoded.HostStats[0].SlotId != 2 || decoded.HostStats[1].SlotId != 1 || decoded.HostStats[0].Missed != 4294967295 || decoded.Signatures[0].SlotId != 2 {
			t.Fatal("order or uint32 boundary changed")
		}
	}
}
