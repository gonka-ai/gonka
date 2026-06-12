package paramvalidators

import (
	"encoding/json"
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

func TestDereferenceLocalSchemaRefsIgnoresRefSiblings(t *testing.T) {
	schema := parseDocument(t, `{"$defs":{"Path":{"type":"string","description":"from ref"}},"properties":{"path":{"$ref":"#/$defs/Path","description":"from sibling"}}}`)

	resolved, err := DereferenceLocalSchemaRefs(schema, 256)
	require.NoError(t, err)
	require.NotContains(t, resolved, "$defs")

	path := resolved["properties"].(map[string]any)["path"].(map[string]any)
	require.Equal(t, "string", path["type"])
	require.Equal(t, "from ref", path["description"])
}

func TestDereferenceLocalSchemaRefsIgnoresConflictingRefSiblings(t *testing.T) {
	schema := parseDocument(t, `{"$defs":{"P":{"type":"integer","minimum":1,"maximum":9}},"properties":{"x":{"$ref":"#/$defs/P","type":"string"}}}`)

	resolved, err := DereferenceLocalSchemaRefs(schema, 256)
	require.NoError(t, err)

	x := resolved["properties"].(map[string]any)["x"].(map[string]any)
	require.Equal(t, "integer", x["type"])
	require.Equal(t, json.Number("1"), x["minimum"])
	require.Equal(t, json.Number("9"), x["maximum"])
	require.NotContains(t, x, "allOf")
}

func TestDereferenceLocalSchemaRefsAllowsBooleanSchemaRef(t *testing.T) {
	schema := parseDocument(t, `{"$defs":{"Anything":true,"Nothing":false},"properties":{"allowed":{"$ref":"#/$defs/Anything"},"blocked":{"$ref":"#/$defs/Nothing"}}}`)

	resolved, err := DereferenceLocalSchemaRefs(schema, 256)
	require.NoError(t, err)

	properties := resolved["properties"].(map[string]any)
	require.Equal(t, true, properties["allowed"])
	require.Equal(t, false, properties["blocked"])
}

func TestDereferenceLocalSchemaRefsRejectsNonSchemaRefTarget(t *testing.T) {
	schema := parseDocument(t, `{"$defs":{"S":"justastring"},"properties":{"x":{"$ref":"#/$defs/S"}}}`)

	_, err := DereferenceLocalSchemaRefs(schema, 256)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSchemaRef)
	require.Contains(t, err.Error(), "object or boolean schema")
}

func TestDereferenceLocalSchemaRefsEnforcesExpandedNodeBudget(t *testing.T) {
	schema := parseDocument(t, `{"$defs":{"Obj":{"type":"object","properties":{"x":{"type":"string"}}}},"properties":{"a":{"$ref":"#/$defs/Obj"},"b":{"$ref":"#/$defs/Obj"}}}`)

	_, err := DereferenceLocalSchemaRefs(schema, 3)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSchemaNodes)
}
