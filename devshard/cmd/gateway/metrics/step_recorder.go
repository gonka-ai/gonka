package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	stepBucketStartSeconds = 0.0005
	stepBucketCount        = 18
)

var stepBuckets = prometheus.ExponentialBuckets(stepBucketStartSeconds, bucketGrowthFactor, stepBucketCount)

// StepRecorder times where a request waits inside the gateway itself. See operations.md, "Where the gateway spends a request's time".
type StepRecorder struct {
	sessionSteps *prometheus.HistogramVec
	queueWait    prometheus.Histogram
}

func NewStepRecorder(telemetry *Metrics) *StepRecorder {
	recorder := &StepRecorder{
		sessionSteps: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "devshard_gateway_session_step_seconds",
			Help:    "Time an escrow session spent in one step: waiting for or holding its lock, writing to its store, catching a host up.",
			Buckets: stepBuckets,
		}, []string{"step"}),
		queueWait: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "devshard_gateway_escrow_queue_wait_seconds",
			Help:    "Time a request waited in an escrow's dispatch queue before it was handed a nonce.",
			Buckets: stepBuckets,
		}),
	}
	telemetry.Register(recorder.sessionSteps, recorder.queueWait)
	return recorder
}

func (r *StepRecorder) SessionStep(step string, elapsed time.Duration) {
	r.sessionSteps.WithLabelValues(metricLabel(step, labelUnknown)).Observe(elapsed.Seconds())
}

func (r *StepRecorder) QueueWaited(waited time.Duration) {
	r.queueWait.Observe(waited.Seconds())
}
