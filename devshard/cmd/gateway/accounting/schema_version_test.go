package accounting

import "testing"

// Test flow:
//  1. Read the package's `SchemaVersion` constant.
//  2. Assert it is above 6, the `schema_version` the released v6 gateway emits over a different shape, so the number alone tells the two shapes apart.
func TestSchemaVersionIsAboveTheLegacyLedger(t *testing.T) {
	t.Parallel()
	const releasedV6 = 6
	if SchemaVersion <= releasedV6 {
		t.Fatalf("SchemaVersion = %d, want above %d: the released v6 gateway emits a different shape under the same field name", SchemaVersion, releasedV6)
	}
}
