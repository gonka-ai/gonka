package selfcheck

import (
	"context"
	"testing"
	"time"
)

// TestRunPasses asserts that the selfcheck reports PASS on a healthy
// broker driven by the synthetic chain. Doubles as a regression guard
// against changes to broker construction that would break the public
// API the selfcheck depends on.
func TestRunPasses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	report, err := Run(ctx)
	if err != nil {
		t.Fatalf("Run setup error: %v", err)
	}
	if !report.Pass {
		t.Fatalf("selfcheck did not pass:\n%s", report.String())
	}
	if len(report.Stages) == 0 {
		t.Fatal("expected at least one stage in report")
	}
	for _, s := range report.Stages {
		if !s.Pass {
			t.Errorf("stage %q failed: %s", s.Name, s.Details)
		}
	}
}
