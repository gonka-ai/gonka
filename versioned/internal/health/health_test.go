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

func TestBuildSummarySeparatesAvailabilityFromConvergence(t *testing.T) {
	summary := BuildSummary(
		"serving",
		true,
		0,
		[]StatusEntry{{Status: "running"}, {Status: "starting"}},
		Conditions{
			Available:   true,
			Progressing: true,
			Desired:     2,
			Running:     1,
		},
	)
	if !summary.Ready {
		t.Fatal("an available host became unready while another version was starting")
	}
	if !summary.Progressing || summary.Degraded || summary.Reconciled {
		t.Fatalf(
			"summary conditions = progressing:%v degraded:%v reconciled:%v",
			summary.Progressing,
			summary.Degraded,
			summary.Reconciled,
		)
	}
	if summary.DesiredChildren != 2 || summary.RunningChildren != 1 {
		t.Fatalf(
			"summary child counts = desired:%d running:%d",
			summary.DesiredChildren,
			summary.RunningChildren,
		)
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
