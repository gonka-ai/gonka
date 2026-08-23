// Package testutil holds small assertion helpers shared across devshardctl test
// packages. It imports "testing" on purpose: it is test-support code (like
// net/http/httptest) and is only ever linked into _test binaries that import it.
package testutil

import "testing"

// FloatPtr returns a pointer to v, for building optional float parameters in tests.
func FloatPtr(v float64) *float64 { return &v }

// MapAt returns items[index] as a map[string]any, failing the test if the element
// is missing or not a map.
func MapAt(t *testing.T, items []any, index int) map[string]any {
	t.Helper()
	value, ok := items[index].(map[string]any)
	if !ok {
		t.Fatalf("items[%d] is not a map: %T", index, items[index])
	}
	return value
}
