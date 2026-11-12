package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"devshard/transport"
)

// PeerRPCRecorder is where peer-RPC adoption lands: how each escrow reached each host, Connect over HTTP/2 or JSON.
type PeerRPCRecorder struct {
	sessions *prometheus.CounterVec
	hosts    *prometheus.GaugeVec
}

var _ transport.PeerRPCAdoptionSink = (*PeerRPCRecorder)(nil)

func NewPeerRPCRecorder(telemetry *Metrics) *PeerRPCRecorder {
	recorder := &PeerRPCRecorder{
		sessions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_escrow_sessions_total",
			Help: "Escrow-to-host bindings, counted once per pair, by whether the host was reached over Connect on HTTP/2 or over JSON.",
		}, []string{"path"}),
		hosts: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "devshard_gateway_host_rpc",
			Help: "1 for each host and the way a live escrow reaches it, Connect on HTTP/2 or JSON; deleted when no live escrow binds the host.",
		}, []string{"peer", "mode"}),
	}
	telemetry.Register(recorder.sessions, recorder.hosts)
	return recorder
}

func (r *PeerRPCRecorder) IncEscrowSession(path string) {
	if r == nil {
		return
	}
	r.sessions.WithLabelValues(metricLabel(path, labelUnknown)).Inc()
}

func (r *PeerRPCRecorder) SetHostRPC(peer, mode string, on bool) {
	if r == nil {
		return
	}
	value := 0.0
	if on {
		value = 1
	}
	r.hosts.WithLabelValues(metricLabel(peer, labelUnknown), metricLabel(mode, labelUnknown)).Set(value)
}

func (r *PeerRPCRecorder) DeleteHostRPC(peer, mode string) {
	if r == nil {
		return
	}
	r.hosts.DeleteLabelValues(metricLabel(peer, labelUnknown), metricLabel(mode, labelUnknown))
}
