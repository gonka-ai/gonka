package validation

import "testing"

func TestSimilarityPassesThreshold(t *testing.T) {
	if !SimilarityPassesThreshold(0.95, 0.94) {
		t.Fatal("0.95 should pass 0.94")
	}
	if SimilarityPassesThreshold(0.94, 0.94) {
		t.Fatal("0.94 should not pass 0.94 (strictly greater)")
	}
	if SimilarityPassesThreshold(0.93, 0.94) {
		t.Fatal("0.93 should not pass 0.94")
	}
}

func TestDecimalToFloat(t *testing.T) {
	got := DecimalToFloat(94, -2)
	if got < 0.939999 || got > 0.940001 {
		t.Fatalf("DecimalToFloat(94, -2) = %v, want 0.94", got)
	}
}

func TestSimilarityValidationResult_IsSuccessful_UsesLegacyThreshold(t *testing.T) {
	pass := SimilarityValidationResult{Value: LegacySimilarityThreshold + 0.001}
	if !pass.IsSuccessful() {
		t.Fatal("expected legacy threshold pass")
	}
	fail := SimilarityValidationResult{Value: LegacySimilarityThreshold}
	if fail.IsSuccessful() {
		t.Fatal("expected legacy threshold fail at exact boundary")
	}
}
