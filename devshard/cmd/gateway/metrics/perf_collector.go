package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"devshard/cmd/gateway/perf"
)

type HostPerfSource interface {
	Snapshot() []perf.HostState
}

type PerfCollector struct {
	hosts HostPerfSource

	ejected  *prometheus.Desc
	inflight *prometheus.Desc
	decode   *prometheus.Desc
}

func NewPerfCollector(hosts HostPerfSource) *PerfCollector {
	return &PerfCollector{
		hosts:    hosts,
		ejected:  gaugeDesc("devshard_gateway_host_ejected", "Whether outlier detection currently withholds a host from routing.", "participant_key", "model"),
		inflight: gaugeDesc("devshard_gateway_host_inflight_requests", "Attempts currently in flight against a host.", "participant_key"),
		decode:   gaugeDesc("devshard_gateway_host_seconds_per_output_token", "Slow-quartile decode cost per output token, measured from the first content chunk.", "participant_key", "model"),
	}
}

func (c *PerfCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.ejected
	ch <- c.inflight
	ch <- c.decode
}

// Collect reports in-flight once per participant; ejection is per model, so the loop would duplicate it.
func (c *PerfCollector) Collect(ch chan<- prometheus.Metric) {
	reported := map[string]struct{}{}
	for _, host := range c.hosts.Snapshot() {
		gauge(ch, c.ejected, boolGauge(host.Ejected), host.Participant, host.Model)
		if host.TimePerOutputToken > 0 {
			gauge(ch, c.decode, host.TimePerOutputToken.Seconds(), host.Participant, host.Model)
		}
		if _, seen := reported[host.Participant]; seen {
			continue
		}
		reported[host.Participant] = struct{}{}
		gauge(ch, c.inflight, float64(host.Inflight), host.Participant)
	}
}
