package tx

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	chaintypes "github.com/productscience/inference/x/inference/types"
)

func TestEncodeSettlementHostStats_RoundTrip(t *testing.T) {
	in := HostStats{
		SlotID: 3, Missed: 1, Invalid: 2, Cost: 4_000,
		RequiredValidations: 5, CompletedValidations: 6, Validated: 7, Finished: 8,
	}
	var out chaintypes.DevshardSettlementHostStats
	require.NoError(t, out.Unmarshal(encodeSettlementHostStats(in)))
	require.Equal(t, chaintypes.DevshardSettlementHostStats{
		SlotId: 3, Missed: 1, Invalid: 2, Cost: 4_000,
		RequiredValidations: 5, CompletedValidations: 6, Validated: 7, Finished: 8,
	}, out)
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
