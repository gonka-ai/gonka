package chain

import (
	"encoding/hex"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"testing"
)

func TestSettlementWireBoundaryBaseline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input SettlementInput
		want  string
	}{
		{"empty fields", SettlementInput{EscrowID: 1}, "1001"},
		{"empty nested fields", SettlementInput{EscrowID: 1, HostStats: []SettlementHostStat{{}}, SlotSigs: []SettlementSlotSig{{}}}, "100132003a00"},
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
	input := SettlementInput{EscrowID: 1, HostStats: []SettlementHostStat{{SlotID: 2, Missed: 1<<32 - 1}, {SlotID: 1}}, SlotSigs: []SettlementSlotSig{{SlotID: 2}, {SlotID: 1}}}
	data, err := encodeMsgSettleDevshardEscrow("", input)
	if err != nil {
		t.Fatal(err)
	}
	var decoded inferencetypes.MsgSettleDevshardEscrow
	if err := decoded.Unmarshal(data); err != nil {
		t.Fatal(err)
	}
	if decoded.HostStats[0].Missed != 1<<32-1 || decoded.HostStats[0].SlotId != 2 || decoded.HostStats[1].SlotId != 1 || decoded.Signatures[0].SlotId != 2 {
		t.Fatal("uint32 boundary or input order changed")
	}
}
