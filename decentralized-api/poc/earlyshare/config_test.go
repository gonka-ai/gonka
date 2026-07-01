package earlyshare

import (
	"math"
	"testing"
)

func TestConfigNormalized(t *testing.T) {
	t.Run("zero value disables and applies defaults", func(t *testing.T) {
		got := Config{}.Normalized()
		if got.Mode != ModeDisabled {
			t.Fatalf("mode = %q, want disabled", got.Mode)
		}
		if math.Abs(got.FirstFraction-DefaultFirstFraction) > 1e-9 {
			t.Fatalf("first fraction = %v, want %v", got.FirstFraction, DefaultFirstFraction)
		}
		if got.ThresholdRatio != DefaultThresholdRatio {
			t.Fatalf("threshold ratio = %v, want %v", got.ThresholdRatio, DefaultThresholdRatio)
		}
	})

	t.Run("invalid mode falls back to disabled", func(t *testing.T) {
		got := Config{Mode: "bogus"}.Normalized()
		if got.Mode != ModeDisabled {
			t.Fatalf("mode = %q, want disabled", got.Mode)
		}
	})

	t.Run("out-of-range fraction reset", func(t *testing.T) {
		if got := (Config{FirstFraction: 1.5}).Normalized(); got.FirstFraction != DefaultFirstFraction {
			t.Fatalf("fraction = %v, want default", got.FirstFraction)
		}
		if got := (Config{FirstFraction: -0.1}).Normalized(); got.FirstFraction != DefaultFirstFraction {
			t.Fatalf("fraction = %v, want default", got.FirstFraction)
		}
	})

	t.Run("enabled and enforcing flags", func(t *testing.T) {
		if (Config{Mode: ModeDisabled}).Enabled() {
			t.Fatal("disabled must not be enabled")
		}
		obs := Config{Mode: ModeObserve}
		if !obs.Enabled() || obs.Enforcing() {
			t.Fatal("observe must be enabled but not enforcing")
		}
		enf := Config{Mode: ModeEnforce}
		if !enf.Enabled() || !enf.Enforcing() {
			t.Fatal("enforce must be enabled and enforcing")
		}
	})
}
