package metrics

import "github.com/prometheus/client_golang/prometheus"

// StreamLogSource is the live stream logs' own account of themselves; plain integers avoid an import cycle with api.
type StreamLogSource interface {
	StreamLogStats() (replays, live int64)
}

type StreamLogCollector struct {
	logs StreamLogSource

	replays *prometheus.Desc
	live    *prometheus.Desc
}

func NewStreamLogCollector(logs StreamLogSource) *StreamLogCollector {
	return &StreamLogCollector{
		logs:    logs,
		replays: counterDesc("devshard_gateway_stream_log_replays_total", "Repeated streamed requests that took over a live stream log, counted when the takeover starts. Like a cache hit it runs no race; it is not counted in the cache hits."),
		live:    gaugeDesc("devshard_gateway_stream_logs", "Live stream logs the gateway currently holds, from the start of a streamed reply until its race ends or its log is dropped."),
	}
}

func (c *StreamLogCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.replays
	ch <- c.live
}

func (c *StreamLogCollector) Collect(ch chan<- prometheus.Metric) {
	if c.logs == nil {
		return
	}
	replays, live := c.logs.StreamLogStats()
	counter(ch, c.replays, float64(replays))
	gauge(ch, c.live, float64(live))
}
