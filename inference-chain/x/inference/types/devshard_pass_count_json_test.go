package types_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestDevshardPassCountJSONNames(t *testing.T) {
	cdc := codec.NewProtoCodec(codectypes.NewInterfaceRegistry())
	sampled := types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED
	derived := types.DevshardPassCount_DEVSHARD_PASS_COUNT_DERIVED
	unspecified := types.DevshardPassCount_DEVSHARD_PASS_COUNT_UNSPECIFIED

	cases := []struct {
		raw  string
		want types.DevshardPassCount
	}{
		{`"derived"`, derived},
		{`"DERIVED"`, derived},
		{`"Derived"`, derived},
		{`"sampled"`, sampled},
		{`"SAMPLED"`, sampled},
		{`"Sampled"`, sampled},
		{`"DEVSHARD_PASS_COUNT_DERIVED"`, derived},
		{`"devshard_pass_count_sampled"`, sampled},
		{`"DEVSHARD_PASS_COUNT_UNSPECIFIED"`, unspecified},
		{`"unspecified"`, unspecified},
		{`1`, sampled},
		{`2`, derived},
		{`0`, unspecified},
		{`"1"`, sampled},
		{`"2"`, derived},
		{`3`, unspecified},
		{`"foo"`, unspecified},
		{`""`, unspecified},
		{`null`, unspecified},
		{`true`, unspecified},
		{`[]`, unspecified},
		{`{}`, unspecified},
	}
	for _, tc := range cases {
		t.Run("encoding/json "+tc.raw, func(t *testing.T) {
			var got types.DevshardPassCount
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &got))
			require.Equal(t, tc.want, got)
		})
		t.Run("proto codec "+tc.raw, func(t *testing.T) {
			raw := fmt.Sprintf(`{"name":"v4","pass_count":%s}`, tc.raw)
			var got types.DevshardApprovedVersion
			require.NoError(t, cdc.UnmarshalJSON([]byte(raw), &got), raw)
			require.Equal(t, tc.want, got.PassCount, raw)
		})
	}
}
