package metrics

import "github.com/prometheus/client_golang/prometheus"

// CaptureSource is the capture sink's own account of itself; plain integers avoid an import cycle with api.
type CaptureSource interface {
	CaptureStats() (written, refused, failed, held int64)
}

type CaptureCollector struct {
	capture CaptureSource

	written *prometheus.Desc
	refused *prometheus.Desc
	failed  *prometheus.Desc
	held    *prometheus.Desc
}

func NewCaptureCollector(capture CaptureSource) *CaptureCollector {
	return &CaptureCollector{
		capture: capture,
		written: counterDesc("devshard_gateway_captured_requests_total", "Requests written to the capture directory."),
		refused: counterDesc("devshard_gateway_captured_requests_refused_total", "Captures refused because the directory is at its byte cap. Nothing evicts capture files, so a rising count means capture is off until an operator empties the directory."),
		failed:  counterDesc("devshard_gateway_captured_requests_failed_total", "Captures the sink could not write: the directory is unwritable, out of space, or gone. Distinct from refused, which is the byte cap doing its job."),
		held:    gaugeDesc("devshard_gateway_capture_bytes_held", "Bytes the capture directory holds."),
	}
}

func (c *CaptureCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.written
	ch <- c.refused
	ch <- c.failed
	ch <- c.held
}

func (c *CaptureCollector) Collect(ch chan<- prometheus.Metric) {
	if c.capture == nil {
		return
	}
	written, refused, failed, held := c.capture.CaptureStats()
	counter(ch, c.written, float64(written))
	counter(ch, c.refused, float64(refused))
	counter(ch, c.failed, float64(failed))
	gauge(ch, c.held, float64(held))
}
