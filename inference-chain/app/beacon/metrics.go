package beacon

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Block beacon metrics (Step 3). Registered on the default Prometheus
// registry, which CometBFT serves on its instrumentation listener (:26660).
// CometBFT RPC does not expose vote extensions before Step 4 puts them into
// blocks, so these are how operators and e2e tests see the beacon work.
var (
	// ExtendVoteSlots is the number of slots signed in the last ExtendVote
	// (0 when the extension was empty).
	ExtendVoteSlots = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "beacon_extend_vote_slots",
		Help: "Slots signed in the last beacon vote extension (0 when empty).",
	})
	// ExtendVoteEmpty counts empty extensions by reason.
	ExtendVoteEmpty = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "beacon_extend_vote_empty_total",
		Help: "Beacon vote extensions left empty, by reason.",
	}, []string{"reason"})
	// ExtendVoteSigned counts non-empty extensions.
	ExtendVoteSigned = promauto.NewCounter(prometheus.CounterOpts{
		Name: "beacon_extend_vote_signed_total",
		Help: "Beacon vote extensions carrying partial signatures.",
	})
	// ExtendVoteDuration is the time spent building a beacon extension.
	ExtendVoteDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "beacon_extend_vote_duration_seconds",
		Help:    "Time to build a beacon vote extension.",
		Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
	})
	// CollectorPartials counts extensions seen by the collector, by verdict
	// (valid, invalid, not_owner, empty, duplicate, stale, no_pending, dropped).
	CollectorPartials = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "beacon_collector_partials_total",
		Help: "Vote extensions processed by the beacon collector, by verdict.",
	}, []string{"result"})
	// SigmaBuilt counts rounds for which the collector built and verified σ.
	SigmaBuilt = promauto.NewCounter(prometheus.CounterOpts{
		Name: "beacon_sigma_built_total",
		Help: "Rounds whose group signature the collector built and verified.",
	})
	// ProposalOutcome counts beacon txs this node proposed, by outcome (sigma, miss).
	ProposalOutcome = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "beacon_proposal_outcome_total",
		Help: "Beacon txs proposed by this node, by outcome.",
	}, []string{"outcome"})
	// ProcessProposal counts ProcessProposal verdicts by result and reason.
	ProcessProposal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "beacon_process_proposal_total",
		Help: "ProcessProposal verdicts on the beacon tx, by result and reason.",
	}, []string{"result", "reason"})
	// VerifyVoteExt counts VerifyVoteExtension verdicts by result and reason.
	VerifyVoteExt = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "beacon_verify_vote_ext_total",
		Help: "VerifyVoteExtension verdicts, by result and reason.",
	}, []string{"result", "reason"})
)

// Empty-extension reasons (label values of beacon_extend_vote_empty_total).
const (
	EmptyInactive  = "inactive"   // before the Step 3 activation height
	EmptyNoPending = "no_pending" // no PendingBeacon for the height (no key yet, VE just enabled)
	EmptyNoShares  = "no_shares"  // no verified shares for the key epoch on this node
	EmptyNotOwner  = "not_owner"  // shares do not match the key epoch or slot range
	EmptyOversize  = "oversize"   // would exceed the size cap peers enforce
	EmptyError     = "error"      // signing failed
	EmptyPanic     = "panic"      // recovered panic
)
