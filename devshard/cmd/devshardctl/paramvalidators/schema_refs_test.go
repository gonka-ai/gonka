package paramvalidators

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDereferenceLocalSchemaRefsRejectsRecursiveRefs(t *testing.T) {
	schema := parseDocument(t, `{"$defs":{"A":{"$ref":"#/$defs/B"},"B":{"$ref":"#/$defs/A"}},"properties":{"x":{"$ref":"#/$defs/A"}}}`)

	_, err := DereferenceLocalSchemaRefs(schema, 256)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSchemaRef)
	require.Contains(t, err.Error(), "recursive reference")
}

func TestDereferenceLocalSchemaRefsMergesRefSiblings(t *testing.T) {
	schema := parseDocument(t, `{"$defs":{"Path":{"type":"string","description":"from ref"}},"properties":{"path":{"$ref":"#/$defs/Path","description":"from sibling"}}}`)

	resolved, err := DereferenceLocalSchemaRefs(schema, 256)
	require.NoError(t, err)
	require.NotContains(t, resolved, "$defs")

	path := resolved["properties"].(map[string]any)["path"].(map[string]any)
	require.Equal(t, "string", path["type"])
	require.Equal(t, "from sibling", path["description"])
}

func TestDereferenceLocalSchemaRefsEnforcesExpandedNodeBudget(t *testing.T) {
	schema := parseDocument(t, `{"$defs":{"Obj":{"type":"object","properties":{"x":{"type":"string"}}}},"properties":{"a":{"$ref":"#/$defs/Obj"},"b":{"$ref":"#/$defs/Obj"}}}`)

	_, err := DereferenceLocalSchemaRefs(schema, 3)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSchemaNodes)
}
