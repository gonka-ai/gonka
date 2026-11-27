package transport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCountTopLevelJSONItems(t *testing.T) {
	cases := []struct {
		in    string
		items int
	}{
		{``, 0},
		{`null`, 0},
		{`{"a":[1,2]}`, 0},
		{`[]`, 0},
		{` [ ] `, 0},
		{`[1]`, 1},
		{`[1, 22 ,true,null]`, 4},
		{`[{},{},[]]`, 3},
		{`[{"a":[1,2,3]},{"b":{"c":[]}}]`, 2},
		{`["a,b", "[{", "\"],[\\"]`, 3},
		{`[{"diff":{"txs":"W10sW10="}}]`, 1},
	}
	for _, tc := range cases {
		require.Equal(t, tc.items, countTopLevelJSONItems([]byte(tc.in)), tc.in)
	}
}

func TestCountTopLevelJSONItems_DoesNotAllocate(t *testing.T) {
	body := []byte("[" + strings.Repeat(`{"a":[1,"x"]},`, 1000) + "{}]")
	allocs := testing.AllocsPerRun(10, func() { countTopLevelJSONItems(body) })
	require.Zero(t, allocs)
}
