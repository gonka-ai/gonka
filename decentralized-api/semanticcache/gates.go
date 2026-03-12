package semanticcache

// AdaptiveCoherenceFloor returns the minimum CoherenceScoreBps required for
// an L2 context-injected response to be accepted into the hub pool (Gate 1).
//
// The floor scales with the L2 similarity tier: higher-similarity pairs
// (structural twins) face a stricter floor because the risk of the model
// copying a wrong pattern verbatim is highest in that zone.
//
// Calibrated against bookworm real embeddings (all-MiniLM-L6-v2):
//
//	sim > 8000: floor 4500 — code→NL embedding yields 4800–5500 bps for
//	            correct code answers; 5000 caused false rejections (C3: coh≈5175).
//	            Counter-verbatim wrong answers score ~3995 bps — blocked at 4500.
//	sim > 6250: floor 4000 — clear zone baseline.
//	sim ≤ 6250: floor 3000 — grey zone (SemanticVerifier v3 is also called here).
//
// Source: quality_matrix_research_v2.md §9, table "Calibrated parameters".
func AdaptiveCoherenceFloor(l2SimBps uint32) uint32 {
	switch {
	case l2SimBps > 8000:
		return 4500
	case l2SimBps > 6250:
		return 4000
	default:
		return 3000
	}
}

// LoopClosureOK returns true when the context-injected answer meets the hub
// frontier quality bar (Gate 2 — loop closure check).
//
// Returns false (loop closure break) when:
//
//	coherenceBps < hubFrontier - marginBps
//
// On a loop closure break the user still receives the answer; only hub pool
// storage is skipped to prevent degrading the semantic frontier.
//
// hubFrontier is the running average CoherenceScoreBps of all accepted entries
// (SemanticCache.CoherenceStats().sum / accepted). Default 5500 bps until
// loopClosureMinSamples entries have accumulated.
//
// Default calibration: marginBps = 800. Absorbs comment/style embedding
// variation observed on bookworm Go code responses (C1 delta=−593 still OK,
// C5 delta=−1276 correctly breaks the loop when fresh baseline is used).
func LoopClosureOK(coherenceBps uint32, hubFrontier int64, marginBps uint32) bool {
	return int64(coherenceBps)-hubFrontier >= -int64(marginBps)
}

// CoherenceRatioAnomaly returns true when the ratio coherenceBps/simBps falls
// outside the normal regime [lowerBound, upperBound], indicating a potential
// semantic anomaly in the cached response:
//
//   - ratio < lowerBound: answer embeds poorly with this prompt despite prompt
//     similarity — possible wrong_algorithm or domain mismatch.
//     Observed on C5 L1→L2 (ratio≈0.801, NL lower bound 0.85 → anomaly).
//
//   - ratio > upperBound: answer embeds better than prompts match — semantic
//     overfitting, possible direction inversion.
//     Observed on C4 sort asc→desc (ratio≈1.057, upper bound 1.03 → anomaly).
//
// Bounds are expressed as integer thousandths: 850 = 0.85, 1030 = 1.03.
//
// # Domain calibration
//
// The lower bound is domain-sensitive. Code-task embeddings (code text vs NL
// prompt) produce ratios as low as 0.55–0.64 even for correct answers:
//   - C3 counter→cache_race: ratio≈0.639 (correct Go code — NOT anomalous)
//   - C6 http client→server adapted: ratio≈0.644 (correct — NOT anomalous)
//
// Safe lower bounds per domain:
//
//	NL tasks:   lowerBound1000 = 850  (0.85)
//	Code tasks: lowerBound1000 = 550  (0.55)
//
// The upper bound 1030 (1.03) is robust across all observed domains.
//
// Status: validated on C4/C5 research cases (quality_matrix_research_v2.md §10).
// Not yet applied in production gating — requires per-domain MeaningPoints
// accumulation before the lower bound can be calibrated automatically.
func CoherenceRatioAnomaly(coherenceBps, simBps uint32, lowerBound1000, upperBound1000 uint32) bool {
	if simBps == 0 {
		return false
	}
	ratio := uint64(coherenceBps) * 1000 / uint64(simBps)
	return ratio < uint64(lowerBound1000) || ratio > uint64(upperBound1000)
}
