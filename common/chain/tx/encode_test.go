package tx

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestEncodeSettlementHostStats_RoundTrip(t *testing.T) {
	in := HostStats{
		SlotID: 3, Missed: 1, Invalid: 2, Cost: 4_000,
		RequiredValidations: 5, CompletedValidations: 6, Validated: 7, Finished: 8,
	}
	want := map[protowire.Number]uint64{1: 3, 2: 1, 3: 2, 4: 4_000, 5: 5, 6: 6, 7: 7, 8: 8}
	require.Equal(t, want, varintFields(t, encodeSettlementHostStats(in)))
}

func varintFields(t *testing.T, b []byte) map[protowire.Number]uint64 {
	t.Helper()
	out := map[protowire.Number]uint64{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		require.Greater(t, n, 0)
		require.Equal(t, protowire.VarintType, typ)
		b = b[n:]
		v, m := protowire.ConsumeVarint(b)
		require.Greater(t, m, 0)
		b = b[m:]
		out[num] = v
	}
	return out
}

func TestEncodeSettlementHostStats_ValidatedOmittedWhenZero(t *testing.T) {
	fieldNumbers := func(b []byte) []protowire.Number {
		var nums []protowire.Number
		for len(b) > 0 {
			num, typ, n := protowire.ConsumeTag(b)
			require.Greater(t, n, 0)
			b = b[n:]
			m := protowire.ConsumeFieldValue(num, typ, b)
			require.Greater(t, m, 0)
			b = b[m:]
			nums = append(nums, num)
		}
		return nums
	}

	withZero := encodeSettlementHostStats(HostStats{SlotID: 1, Cost: 10})
	require.NotContains(t, fieldNumbers(withZero), protowire.Number(7))
	require.NotContains(t, fieldNumbers(withZero), protowire.Number(8))

	withPasses := encodeSettlementHostStats(HostStats{SlotID: 1, Cost: 10, Validated: 2, Finished: 3})
	require.Contains(t, fieldNumbers(withPasses), protowire.Number(7))
	require.Contains(t, fieldNumbers(withPasses), protowire.Number(8))
}
