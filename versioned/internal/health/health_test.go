package health

import "testing"

func TestBuildSummaryUsesConservativeInflightCount(t *testing.T) {
	summary := BuildSummary("serving", true, 1, []StatusEntry{
		{Status: "running", LifecycleInflight: 2, InflightKnown: true},
		{Status: "running", LifecycleInflight: 1, InflightKnown: true},
	})
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

func TestBuildSummaryIsNotReadyBeforeAChildRuns(t *testing.T) {
	for _, children := range [][]StatusEntry{
		nil,
		{{Status: "starting"}},
		{{Status: "stopped"}},
	} {
		summary := BuildSummary("serving", true, 0, children)
		if summary.Ready {
			t.Fatalf("children %+v unexpectedly reported ready", children)
		}
	}
}

func TestBuildSummaryDoesNotReportUnknownChildrenIdle(t *testing.T) {
	summary := BuildSummary("draining", false, 0, []StatusEntry{{InflightKnown: false}})
	if summary.InflightKnown {
		t.Fatal("summary should preserve unknown child inflight state")
	}
	if summary.Idle {
		t.Fatal("summary with unknown child state must not be idle")
	}
}
