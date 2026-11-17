package tx

import (
	"encoding/hex"
	"testing"
)

func TestSettlementWirePreservesManualProfile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input SettleParams
		want  string
	}{
		{
			name:  "empty fields",
			input: SettleParams{EscrowID: 1},
			want:  "0a0010011a0020002a0040004a00",
		},
		{
			name:  "empty nested fields",
			input: SettleParams{EscrowID: 1, HostStats: []HostStats{{}}, Signatures: []SlotSignature{{}}},
			want:  "0a0010011a0020002a00320c0800100018002000280030003a040800120040004a00",
		},
		{
			name: "signed counters and full width cost",
			input: SettleParams{EscrowID: 1, HostStats: []HostStats{{
				SlotID: 2, Missed: -1, Invalid: -1 << 31, Cost: 1<<64 - 1,
				RequiredValidations: 1<<31 - 1,
			}}},
			want: "0a0010011a0020002a00322b080210ffffffffffffffffff011880808080f8ffffffff0120ffffffffffffffffff0128ffffffff07300040004a00",
		},
		{
			name:  "input slot order",
			input: SettleParams{EscrowID: 1, HostStats: []HostStats{{SlotID: 2}, {SlotID: 1}}, Signatures: []SlotSignature{{SlotID: 2}, {SlotID: 1}}},
			want:  "0a0010011a0020002a00320c080210001800200028003000320c0801100018002000280030003a04080212003a040801120040004a00",
		},
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
