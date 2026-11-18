package devshard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScanJSONShape(t *testing.T) {
	cases := []struct {
		in    string
		shape JSONShape
	}{
		{``, JSONShape{}},
		{`null`, JSONShape{}},
		{`[]`, JSONShape{MaxDepth: 1, Composites: 1}},
		{` [ ] `, JSONShape{MaxDepth: 1, Composites: 1}},
		{`[1]`, JSONShape{MaxDepth: 1, Composites: 1, TopLevelItems: 1}},
		{`[1, 22 ,true,null]`, JSONShape{MaxDepth: 1, Composites: 1, TopLevelItems: 4}},
		{`[{},{},[]]`, JSONShape{MaxDepth: 2, Composites: 4, TopLevelItems: 3}},
		{`[{"a":[1,2,3]},{"b":{"c":[]}}]`, JSONShape{MaxDepth: 4, Composites: 6, TopLevelItems: 2}},
		{`["a,b", "[{", "\"],[\\"]`, JSONShape{MaxDepth: 1, Composites: 1, TopLevelItems: 3}},
		{`{"a":[1,2],"b":"{[,"}`, JSONShape{MaxDepth: 2, Composites: 2}},
	}
	for _, tc := range cases {
		require.Equal(t, tc.shape, ScanJSONShape([]byte(tc.in)), tc.in)
	}
}

func TestScanJSONShape_DoesNotAllocate(t *testing.T) {
	body := []byte("[" + strings.Repeat(`{"a":[1,"x"]},`, 1000) + "{}]")
	allocs := testing.AllocsPerRun(10, func() { ScanJSONShape(body) })
	require.Zero(t, allocs)
}

func nested(depth int) []byte {
	return []byte(strings.Repeat(`{"a":`, depth-1) + `{}` + strings.Repeat(`}`, depth-1))
}

func TestCanonicalizeJSON_DepthLimit(t *testing.T) {
	_, err := CanonicalizeJSON(nested(MaxCanonicalJSONDepth))
	require.NoError(t, err)

	_, err = CanonicalizeJSON(nested(MaxCanonicalJSONDepth + 1))
	require.ErrorIs(t, err, ErrJSONTooComplex)
}

func emptyObjects(n int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"messages":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{}`)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

func TestCanonicalizeJSON_CompositeLimit(t *testing.T) {
	_, err := CanonicalizeJSON(emptyObjects(MaxCanonicalJSONComposites - 2))
	require.NoError(t, err)

	_, err = CanonicalizeJSON(emptyObjects(MaxCanonicalJSONComposites - 1))
	require.ErrorIs(t, err, ErrJSONTooComplex)

	_, err = CanonicalPromptHash(emptyObjects(MaxCanonicalJSONComposites - 1))
	require.ErrorIs(t, err, ErrJSONTooComplex)
}

func TestCanonicalizeJSON_BracketsInStringsDoNotCount(t *testing.T) {
	prompt := []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat(`[{`, MaxCanonicalJSONComposites) + `"}]}`)
	out, err := CanonicalizeJSON(prompt)
	require.NoError(t, err)
	require.Contains(t, string(out), strings.Repeat(`[{`, 8))
}
