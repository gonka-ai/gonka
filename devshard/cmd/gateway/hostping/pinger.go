package hostping

import (
	"context"
	"net/http"
	"time"

	"common/httpguard"
	"common/probe"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/logging"
)

// Sink records what a wave found; *metrics.HostPingRecorder satisfies it.
type Sink interface {
	probe.Sink
	probe.SchedObserver
}

// Pinger reaches the live escrows' hosts on a wall-clock cadence. See README.md.
type Pinger struct {
	scheduler *probe.Scheduler
}

// New returns nil when the probe is off or its schedule does not hold. See README.md.
func New(settings config.HostPing, live liveDials, sink Sink) *Pinger {
	if settings.Disabled || live == nil || sink == nil {
		return nil
	}
	prober, err := probe.New(probe.Config{
		Interval:    time.Duration(settings.IntervalMS) * time.Millisecond,
		Timeout:     time.Duration(settings.TimeoutMS) * time.Millisecond,
		Concurrency: int(settings.Concurrency),
		Transport:   newGuardedTransport(),
	})
	if err != nil {
		logging.Warn("host pings are off: the probe schedule does not hold",
			logkey.Subsystem, "hostping", logkey.Error, err)
		return nil
	}
	return &Pinger{scheduler: probe.NewScheduler(prober, Targets{Live: live}, sink, sink)}
}

func (p *Pinger) Start(ctx context.Context) {
	if p == nil {
		return
	}
	go p.scheduler.Run(ctx)
}

func newGuardedTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = httpguard.NewDialer().DialContext
	return transport
}
