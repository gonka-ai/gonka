package config

import "testing"

// Test flow:
//  1. Start from stored overrides that open a model, switch the gateway off and pin a request cap.
//  2. Merge a patch naming only a new request cap, then one setting the kill switch to null.
//  3. Assert the first keeps model_access and disabled and takes the new cap, and the second clears disabled alone: a partial write changes only what it names.
func TestMergingAPatchChangesOnlyWhatItNames(t *testing.T) {
	disabled, capBefore := true, int64(100)
	stored := Overrides{ModelAccess: map[string]string{"model-a": ModelAccessOpen}, Disabled: &disabled, MaxConcurrentRequests: &capBefore}

	merged, err := stored.Merged([]byte(`{"max_concurrent_requests":250}`))
	if err != nil {
		t.Fatalf("Merged() = %v, want nil", err)
	}
	if merged.ModelAccess["model-a"] != ModelAccessOpen || merged.Disabled == nil || !*merged.Disabled || *merged.MaxConcurrentRequests != 250 {
		t.Fatalf("merged = access %v disabled %v cap %v, want the access and the kill switch kept and the cap 250", merged.ModelAccess, merged.Disabled, *merged.MaxConcurrentRequests)
	}

	cleared, err := merged.Merged([]byte(`{"disabled":null}`))
	if err != nil {
		t.Fatalf("Merged() with null = %v, want nil", err)
	}
	if cleared.Disabled != nil || cleared.ModelAccess["model-a"] != ModelAccessOpen || *cleared.MaxConcurrentRequests != 250 {
		t.Fatalf("cleared = disabled %v access %v cap %v, want only disabled cleared", cleared.Disabled, cleared.ModelAccess, cleared.MaxConcurrentRequests)
	}
}

// Test flow:
//  1. Merge a patch naming a field the overrides do not have into empty overrides.
//  2. Assert it is refused: a typo must not pass as a no-op write.
func TestMergingAPatchRefusesAnUnknownField(t *testing.T) {
	if _, err := (Overrides{}).Merged([]byte(`{"max_concurrent_requestz":1}`)); err == nil {
		t.Fatal("Merged() = nil, want an unknown-field error")
	}
}

// Test flow:
//  1. Merge a patch naming a field in another case than its tag.
//  2. Assert it is refused like any unknown field: the patch never answers 200 for a setting it left unchanged.
func TestMergingAPatchRefusesAFieldNamedInAnotherCase(t *testing.T) {
	if _, err := (Overrides{}).Merged([]byte(`{"Disabled":true}`)); err == nil {
		t.Fatal("Merged() = nil, want the field refused as unknown")
	}
}
