// Package obsmetrics exposes Prometheus metrics for the devshardd
// testenv binary. It is intentionally isolated under testenv/ so
// importing the production devshardd binary does NOT pull in the
// Prometheus client: prod callers wire their own registry from
// dapi-side observability packages.
//
// The handler is exposed by devshardd-testenv on a separate port
// (default 9600) behind EXPORT_METRICS=1. When the env var is unset,
// nothing here is instantiated and the Prometheus dependency is not
// paid for at runtime beyond the idle import cost.
//
// Metric families (per devshard/docs/testenv.md §13.6):
//
//   - devshardd_gossip_messages_total{direction,kind} — counter, incremented
//     by the echo middleware in devshardd-testenv.
//   - devshardd_diff_nonce — gauge, updated by a periodic sampler that
//     reads host.LatestNonce().
//   - devshardd_height_at_latest_nonce — gauge, updated by the same sampler
//     reading host.LatestHeight() via the block oracle.
//   - devshardd_pending_verdicts — gauge, updated by the same sampler
//     via host.MempoolTxs() count. This intentionally over-counts
//     (mempool may contain non-verdict txs too); refined labeling is a
//     follow-up so dashboards have a baseline today.
//   - devshardd_cpoc_skips_total{path,verdict} — counter, reserved; not
//     yet incremented because cPoC skip plumbing lives behind the
//     self-finalization work. Registered empty so the family is
//     discoverable in Grafana as soon as the skip handlers land.
//
// Go runtime and process collectors are always included when the
// registry is built (see New).
package obsmetrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Labels and kinds — exported so callers don't fat-finger strings.
const (
	DirectionIn  = "in"
	DirectionOut = "out"

	// Well-known gossip kinds. Middleware maps Echo routes to one of
	// these, falling back to "other" for unrecognized paths so unknown
	// traffic still shows up on the dashboard instead of silently
	// disappearing.
	KindSignature = "signature"
	KindDiff      = "diff"
	KindRecovery  = "recovery"
	KindInference = "inference"
	KindHealth    = "health"
	KindOther     = "other"
)

// Metrics is the single struct holding every registered family. The
// zero value is invalid; always construct via New.
type Metrics struct {
	reg *prometheus.Registry

	GossipMessages  *prometheus.CounterVec
	DiffNonce       prometheus.Gauge
	HeightAtLatest  prometheus.Gauge
	PendingVerdicts prometheus.Gauge
	CPoCSkips       *prometheus.CounterVec
}

// New creates a fresh registry, registers every family, and wires in
// the default Go runtime + process collectors so Prometheus clients
// see go_* and process_* out of the box. Any registration error
// returns immediately — Prometheus panics on collisions, so we check
// explicitly to keep the devshardd-testenv boot sequence observable.
func New() (*Metrics, error) {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		reg: reg,
		GossipMessages: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshardd_gossip_messages_total",
				Help: "Devshardd-testenv gossip messages observed, labeled by direction (in/out) and high-level kind.",
			},
			[]string{"direction", "kind"},
		),
		DiffNonce: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "devshardd_diff_nonce",
			Help: "Latest applied diff nonce observed by this devshardd-testenv host.",
		}),
		HeightAtLatest: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "devshardd_height_at_latest_nonce",
			Help: "Mainnet block height cached by the host's block oracle at the latest nonce.",
		}),
		PendingVerdicts: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "devshardd_pending_verdicts",
			Help: "Size of the devshardd-testenv host mempool (pending verdicts and other in-flight txs).",
		}),
		CPoCSkips: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "devshardd_cpoc_skips_total",
				Help: "Devshardd-testenv cPoC skip events, labeled by path (a/b) and verdict (valid/invalid).",
			},
			[]string{"path", "verdict"},
		),
	}

	for _, c := range []prometheus.Collector{
		m.GossipMessages, m.DiffNonce, m.HeightAtLatest, m.PendingVerdicts, m.CPoCSkips,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	} {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}

	return m, nil
}

// Handler returns an http.Handler exposing the metrics registry in
// the Prometheus text format. Suitable for mounting on any mux:
//
//	mux.Handle("/metrics", m.Handler())
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "metrics not initialized", http.StatusServiceUnavailable)
		})
	}
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

// Registry is exposed so callers that need to register extra
// collectors (e.g. integration tests) can do so without duplicating
// the Go/process wiring.
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.reg
}

// ObserveInbound increments GossipMessages for direction=in with the
// given kind. Safe on nil receiver so wiring sites can stay
// unconditional even when EXPORT_METRICS is off.
func (m *Metrics) ObserveInbound(kind string) {
	if m == nil {
		return
	}
	if kind == "" {
		kind = KindOther
	}
	m.GossipMessages.WithLabelValues(DirectionIn, kind).Inc()
}

// ObserveOutbound is the outbound twin of ObserveInbound.
func (m *Metrics) ObserveOutbound(kind string) {
	if m == nil {
		return
	}
	if kind == "" {
		kind = KindOther
	}
	m.GossipMessages.WithLabelValues(DirectionOut, kind).Inc()
}

// SetHostState is a small bulk setter for the three host-derived
// gauges. Kept on Metrics (not on host.Host) so the sampler loop in
// devshardd-testenv owns the observation cadence; production dapi
// would use its own sampler.
func (m *Metrics) SetHostState(latestNonce uint64, latestHeight int64, pending int) {
	if m == nil {
		return
	}
	m.DiffNonce.Set(float64(latestNonce))
	m.HeightAtLatest.Set(float64(latestHeight))
	m.PendingVerdicts.Set(float64(pending))
}

// ClassifyEchoPath maps an echo-normalized request path to a metric
// kind. Unknown paths return KindOther so we never drop traffic off
// the dashboard. Centralised here to keep the middleware wiring in
// devshardd-testenv trivial.
func ClassifyEchoPath(path string) string {
	// Short, explicit switch beats a regex here: the set is small and
	// bounded by the transport package.
	switch {
	case path == "/healthz":
		return KindHealth
	case pathHas(path, "gossip") && pathHas(path, "sig"):
		return KindSignature
	case pathHas(path, "diff"):
		return KindDiff
	case pathHas(path, "recovery"):
		return KindRecovery
	case pathHas(path, "inference") || pathHas(path, "chat"):
		return KindInference
	default:
		return KindOther
	}
}

// pathHas is a minimal substring check. Extracted so ClassifyEchoPath
// stays readable without pulling in strings.Contains on every call.
func pathHas(path, needle string) bool {
	for i := 0; i+len(needle) <= len(path); i++ {
		if path[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
