package metrics

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const labelUnknown = "unknown"

// metricLabel keeps a label value non-empty. See operations.md, "Cardinality rules".
func metricLabel(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(fallback); trimmed != "" {
		return trimmed
	}
	return labelUnknown
}

func gaugeDesc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(name, help, labels, nil)
}

func gauge(ch chan<- prometheus.Metric, desc *prometheus.Desc, value float64, labels ...string) {
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value, labels...)
}

func counterDesc(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(name, help, labels, nil)
}

func counter(ch chan<- prometheus.Metric, desc *prometheus.Desc, value float64, labels ...string) {
	ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, value, labels...)
}

func boolGauge(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func elapsedSeconds(from, to time.Time) float64 {
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return 0
	}
	return to.Sub(from).Seconds()
}
