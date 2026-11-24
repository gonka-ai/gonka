package metrics

import (
	"math"
	"slices"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"devshard/cmd/gateway/internal/leakcheck"
)

func TestMain(m *testing.M) {
	leakcheck.VerifyTestMain(m)
}

type labels map[string]string

func family(t *testing.T, telemetry *Metrics, name string) *dto.MetricFamily {
	t.Helper()
	gathered, err := telemetry.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, candidate := range gathered {
		if candidate.GetName() == name {
			return candidate
		}
	}
	return nil
}

func matching(metrics []*dto.Metric, wanted labels) *dto.Metric {
	for _, metric := range metrics {
		present := map[string]string{}
		for _, pair := range metric.GetLabel() {
			present[pair.GetName()] = pair.GetValue()
		}
		if len(present) != len(wanted) {
			continue
		}
		found := true
		for name, value := range wanted {
			if present[name] != value {
				found = false
				break
			}
		}
		if found {
			return metric
		}
	}
	return nil
}

func expectCounter(t *testing.T, telemetry *Metrics, name string, wanted labels, value float64) {
	t.Helper()
	found := family(t, telemetry, name)
	if found == nil {
		t.Fatalf("family %s is absent", name)
	}
	metric := matching(found.GetMetric(), wanted)
	if metric == nil {
		t.Fatalf("family %s has no series %v; present: %v", name, wanted, found.GetMetric())
	}
	if got := metric.GetCounter().GetValue(); got != value {
		t.Fatalf("%s%v: got %v, want %v", name, wanted, got, value)
	}
}

func expectGauge(t *testing.T, telemetry *Metrics, name string, wanted labels, value float64) {
	t.Helper()
	found := family(t, telemetry, name)
	if found == nil {
		t.Fatalf("family %s is absent", name)
	}
	metric := matching(found.GetMetric(), wanted)
	if metric == nil {
		t.Fatalf("family %s has no series %v; present: %v", name, wanted, found.GetMetric())
	}
	if got := metric.GetGauge().GetValue(); math.Abs(got-value) > 1e-9 {
		t.Fatalf("%s%v: got %v, want %v", name, wanted, got, value)
	}
}

func expectHistogram(t *testing.T, telemetry *Metrics, name string, wanted labels, count uint64, sum float64) {
	t.Helper()
	found := family(t, telemetry, name)
	if found == nil {
		t.Fatalf("family %s is absent", name)
	}
	metric := matching(found.GetMetric(), wanted)
	if metric == nil {
		t.Fatalf("family %s has no series %v", name, wanted)
	}
	if got := metric.GetHistogram().GetSampleCount(); got != count {
		t.Fatalf("%s%v count: got %d, want %d", name, wanted, got, count)
	}
	if got := metric.GetHistogram().GetSampleSum(); math.Abs(got-sum) > 1e-9 {
		t.Fatalf("%s%v sum: got %v, want %v", name, wanted, got, sum)
	}
}

func expectAbsent(t *testing.T, telemetry *Metrics, name string) {
	t.Helper()
	if found := family(t, telemetry, name); found != nil {
		t.Fatalf("family %s should have no series: %v", name, found.GetMetric())
	}
}

// familiesCarrying counts the families holding at least one series whose labelName is labelValue.
func familiesCarrying(t *testing.T, telemetry *Metrics, labelName, labelValue string) int {
	t.Helper()
	gathered, err := telemetry.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	families := 0
	for _, candidate := range gathered {
		if slices.ContainsFunc(candidate.GetMetric(), func(metric *dto.Metric) bool {
			return slices.ContainsFunc(metric.GetLabel(), func(pair *dto.LabelPair) bool {
				return pair.GetName() == labelName && pair.GetValue() == labelValue
			})
		}) {
			families++
		}
	}
	return families
}

func expectSeriesCount(t *testing.T, telemetry *Metrics, name string, count int) {
	t.Helper()
	found := family(t, telemetry, name)
	if found == nil && count == 0 {
		return
	}
	if found == nil {
		t.Fatalf("family %s is absent, want %d series", name, count)
	}
	if got := len(found.GetMetric()); got != count {
		t.Fatalf("family %s series: got %d, want %d", name, got, count)
	}
}
