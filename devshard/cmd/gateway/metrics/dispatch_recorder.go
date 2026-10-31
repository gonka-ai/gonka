package metrics

import "github.com/prometheus/client_golang/prometheus"

// DispatchRecorder satisfies the scheduler's nonce-accounting hook. See operations.md, "Metrics".
type DispatchRecorder struct {
	ghostBurns          *prometheus.CounterVec
	nonceHolds          *prometheus.CounterVec
	burnBudgetExhausted *prometheus.CounterVec
}

func NewDispatchRecorder(telemetry *Metrics) *DispatchRecorder {
	recorder := &DispatchRecorder{
		ghostBurns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_ghost_nonces_burned_total",
			Help: "Total nonces burned as silent ghost probes by escrow, host and reason.",
		}, []string{"devshard_id", "participant", "reason"}),
		nonceHolds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_nonce_holds_total",
			Help: "Total times an escrow parked its next nonce instead of burning or serving it.",
		}, []string{"devshard_id"}),
		burnBudgetExhausted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "devshard_gateway_burn_budget_exhausted_total",
			Help: "Total drains that stopped because an escrow's per-drain burn budget ran out.",
		}, []string{"devshard_id"}),
	}
	telemetry.Register(recorder.ghostBurns, recorder.nonceHolds, recorder.burnBudgetExhausted)
	return recorder
}

func (r *DispatchRecorder) GhostBurned(escrowID, participant, reason string) {
	r.ghostBurns.WithLabelValues(
		metricLabel(escrowID, labelUnknown),
		metricLabel(participant, labelUnknown),
		metricLabel(reason, labelUnknown),
	).Inc()
}

func (r *DispatchRecorder) NonceHeld(escrowID string) {
	r.nonceHolds.WithLabelValues(metricLabel(escrowID, labelUnknown)).Inc()
}

func (r *DispatchRecorder) BurnBudgetExhausted(escrowID string) {
	r.burnBudgetExhausted.WithLabelValues(metricLabel(escrowID, labelUnknown)).Inc()
}

// EscrowRetired drops the escrow's series under the label its writes used: ids are never reused, so a rotation would leak them forever.
func (r *DispatchRecorder) EscrowRetired(escrowID string) {
	retired := prometheus.Labels{"devshard_id": metricLabel(escrowID, labelUnknown)}
	r.ghostBurns.DeletePartialMatch(retired)
	r.nonceHolds.DeletePartialMatch(retired)
	r.burnBudgetExhausted.DeletePartialMatch(retired)
}
