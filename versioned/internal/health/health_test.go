package health

import "testing"

func TestBuildSummaryUsesConservativeInflightCount(t *testing.T) {
	summary := BuildSummary("serving", true, 1, []StatusEntry{
		{Status: "running", LifecycleInflight: 2, InflightKnown: true},
		{Status: "running", LifecycleInflight: 1, InflightKnown: true},
	}, Conditions{Available: true, Reconciled: true})
	if summary.Inflight != 3 {
		t.Fatalf("inflight = %d, want 3", summary.Inflight)
	}
	if summary.Idle {
		t.Fatal("summary with in-flight work must not be idle")
	}
	if !summary.Ready {
		t.Fatal("serving summary should be ready")
	}
}

func TestBuildSummaryUsesAvailabilityForReadiness(t *testing.T) {
	summary := BuildSummary(
		"serving",
		true,
		0,
		[]StatusEntry{{Status: "running"}, {Status: "starting"}},
		Conditions{Available: true, Reconciled: false, Degraded: true},
	)
	if !summary.Ready {
		t.Fatal("an available host became unready while another version was starting")
	}
	if !summary.Degraded || summary.Reconciled {
		t.Fatalf("summary conditions = degraded:%v reconciled:%v", summary.Degraded, summary.Reconciled)
	}
}

func TestBuildSummaryDoesNotReportUnknownChildrenIdle(t *testing.T) {
	summary := BuildSummary(
		"draining",
		false,
		0,
		[]StatusEntry{{InflightKnown: false}},
		Conditions{},
	)
	if summary.InflightKnown {
		t.Fatal("summary should preserve unknown child inflight state")
	}
	if summary.Idle {
		t.Fatal("summary with unknown child state must not be idle")
	}
}
