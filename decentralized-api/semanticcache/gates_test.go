package semanticcache

import "testing"

// ── AdaptiveCoherenceFloor ────────────────────────────────────────────────────

// TestAdaptiveCoherenceFloor_StructuralTwin verifies sim > 8000 → floor 4500.
// 4500 was chosen (not 5000) because code→NL embedding coherence for correct
// code answers lands in the 4800–5500 bps range on bookworm all-MiniLM-L6-v2.
func TestAdaptiveCoherenceFloor_StructuralTwin(t *testing.T) {
	cases := []struct{ sim, want uint32 }{
		{8001, 4500},
		{8065, 4500},  // C1 binary→linear
		{8101, 4500},  // C3 counter→cache_race
		{8452, 4500},  // C2 fibonacci→tribonacci
		{9018, 4500},  // C6 HTTP client→server
		{10000, 4500}, // exact match edge
	}
	for _, tc := range cases {
		if got := AdaptiveCoherenceFloor(tc.sim); got != tc.want {
			t.Errorf("sim=%d: expected floor %d, got %d", tc.sim, tc.want, got)
		}
	}
}

// TestAdaptiveCoherenceFloor_ClearZone verifies 6250 < sim ≤ 8000 → floor 4000.
// Includes C4 and C5 adversarial cases that expose the residual gap.
func TestAdaptiveCoherenceFloor_ClearZone(t *testing.T) {
	cases := []struct {
		name     string
		sim      uint32
		want     uint32
		isAdvers bool
	}{
		{"boundary 6251", 6251, 4000, false},
		{"C4 sort asc→desc", 6832, 4000, true}, // wrong answer coherence=7221 > 4000 → passes (gap)
		{"C5 L1→L2 explanation", 7323, 4000, true}, // wrong answer coherence=5863 > 4000 → passes (gap)
		{"boundary 8000", 8000, 4000, false},
	}
	for _, tc := range cases {
		got := AdaptiveCoherenceFloor(tc.sim)
		if got != tc.want {
			t.Errorf("%s sim=%d: expected floor %d, got %d", tc.name, tc.sim, tc.want, got)
		}
		if tc.isAdvers {
			t.Logf("RESIDUAL GAP: %s floor=%d — wrong answer coherence exceeds this floor", tc.name, got)
		}
	}
}

// TestAdaptiveCoherenceFloor_GreyZone verifies sim ≤ 6250 → floor 3000.
// Grey zone also triggers SemanticVerifier v3.
func TestAdaptiveCoherenceFloor_GreyZone(t *testing.T) {
	cases := []struct{ sim, want uint32 }{
		{6250, 3000}, // boundary: ≤ 6250 → grey zone
		{5000, 3000},
		{4250, 3000}, // similarity gate minimum
		{0, 3000},
	}
	for _, tc := range cases {
		if got := AdaptiveCoherenceFloor(tc.sim); got != tc.want {
			t.Errorf("sim=%d: expected floor %d, got %d", tc.sim, tc.want, got)
		}
	}
}

// ── LoopClosureOK ─────────────────────────────────────────────────────────────

func TestLoopClosureOK_ClearlyAboveFrontier(t *testing.T) {
	// coherence well above hub frontier → OK
	if !LoopClosureOK(7000, 5500, 800) {
		t.Error("coherence=7000 frontier=5500 margin=800: expected OK")
	}
}

func TestLoopClosureOK_AtFrontier(t *testing.T) {
	// coherence == frontier → delta=0 ≥ -800 → OK
	if !LoopClosureOK(5500, 5500, 800) {
		t.Error("coherence=frontier=5500: expected OK")
	}
}

func TestLoopClosureOK_AtMarginBoundary(t *testing.T) {
	// coherence == frontier - margin exactly → inclusive boundary → OK
	if !LoopClosureOK(4700, 5500, 800) { // 5500 - 800 = 4700
		t.Error("coherence=4700 frontier=5500 margin=800: expected OK at exact boundary")
	}
}

func TestLoopClosureOK_BelowMarginByOne(t *testing.T) {
	// coherence == frontier - margin - 1 → loop closure BREAK
	if LoopClosureOK(4699, 5500, 800) { // 4699 < 5500 - 800 = 4700
		t.Error("coherence=4699 frontier=5500 margin=800: expected BREAK")
	}
}

// TestLoopClosureOK_C5_FreshBaseline demonstrates that C5 breaks the loop
// when the fresh GPU baseline (7587) is used as hub_frontier.
// This is the v1 loop_closure_test.py finding: delta=-1276 bps.
func TestLoopClosureOK_C5_FreshBaseline(t *testing.T) {
	// coh_ctx=6311, coh_fresh=7587 from quality_matrix_research_v2.md §3
	if LoopClosureOK(6311, 7587, 800) { // delta = 6311-7587 = -1276 < -800
		t.Error("C5 loop closure with fresh baseline: delta=-1276 should BREAK")
	}
}

// TestLoopClosureOK_C5_DefaultFrontier documents the residual gap: with the
// production hub_frontier default (5500 bps), C5 passes Stage 4.
// This is an EXPECTED failure mode — documented in quality_matrix_research_v2.md §9.
func TestLoopClosureOK_C5_DefaultFrontier(t *testing.T) {
	// C5: coherence=5863, hub_frontier=5500 (default cold start), delta=+363
	if !LoopClosureOK(5863, 5500, 800) {
		t.Error("C5 with default frontier=5500: delta=+363 should PASS (residual gap — documented)")
	}
	t.Log("RESIDUAL GAP (documented): C5 passes Stage 4 with default frontier. Fix: extend verifier to sim≤8000 or ratio gate.")
}

// TestLoopClosureOK_C4_DefaultFrontier documents the C4 residual gap similarly.
func TestLoopClosureOK_C4_DefaultFrontier(t *testing.T) {
	// C4: coherence=7221, hub_frontier=5500, delta=+1721
	if !LoopClosureOK(7221, 5500, 800) {
		t.Error("C4 with default frontier=5500: delta=+1721 should PASS (residual gap — documented)")
	}
	t.Log("RESIDUAL GAP (documented): C4 passes Stage 4. High coherence (7221) is the anomaly — ratio gate catches it.")
}

// TestLoopClosureOK_C3_ColdStart verifies C3 (correct code) passes cold-start
// Stage 4 with hub_frontier=5500: delta=-325 ≥ -800 → OK.
func TestLoopClosureOK_C3_ColdStart(t *testing.T) {
	if !LoopClosureOK(5175, 5500, 800) {
		t.Error("C3 cold start: coherence=5175, frontier=5500, delta=-325 should PASS (within -800 margin)")
	}
}

// TestLoopClosureOK_C1_Style_Delta verifies C1 binary→linear passes with
// delta=-593 (style/comment embedding difference, below the -800 margin).
func TestLoopClosureOK_C1_Style_Delta(t *testing.T) {
	// From research: coh_ctx=7028, delta=-593 vs fresh baseline 7621
	// With default frontier=5500: delta=+1528 → easily OK
	if !LoopClosureOK(7028, 7621, 800) {
		t.Error("C1 binary→linear: delta=-593 is within -800 margin → should PASS")
	}
}

// ── CoherenceRatioAnomaly ─────────────────────────────────────────────────────

// TestCoherenceRatioAnomaly_NormalRegime verifies C1 and C2 (correct answers)
// are not flagged as anomalous in either domain.
func TestCoherenceRatioAnomaly_NormalRegime(t *testing.T) {
	// C1 binary→linear: coherence≈7700, sim=8065, ratio≈0.955 — normal for any domain
	if CoherenceRatioAnomaly(7700, 8065, 550, 1030) {
		t.Error("C1 ratio≈0.955 in [0.55,1.03]: should NOT be anomalous")
	}
	// C2 fibonacci→tribonacci: coherence≈6500, sim=8452, ratio≈0.769
	// Code domain (lower=550): not anomalous; NL domain (lower=850): anomalous
	if CoherenceRatioAnomaly(6500, 8452, 550, 1030) {
		t.Error("C2 ratio≈0.769 with code lower bound 0.55: should NOT be anomalous")
	}
}

// TestCoherenceRatioAnomaly_C4_UpperBound verifies C4 sort asc→desc is caught
// by the upper bound. ratio = 7221/6832 = 1.057 > 1.030.
// Semantic overfitting signature: wrong answer embeds better than prompts match.
func TestCoherenceRatioAnomaly_C4_UpperBound(t *testing.T) {
	if !CoherenceRatioAnomaly(7221, 6832, 550, 1030) {
		t.Error("C4 ratio≈1.057 > 1.030: should trigger UPPER anomaly (direction inversion signature)")
	}
}

// TestCoherenceRatioAnomaly_C5_NLDomain verifies C5 L1→L2 is caught by the
// NL domain lower bound. ratio = 5863/7323 = 0.801 < 0.850.
func TestCoherenceRatioAnomaly_C5_NLDomain(t *testing.T) {
	if !CoherenceRatioAnomaly(5863, 7323, 850, 1030) {
		t.Error("C5 ratio≈0.801 with NL lower bound 0.850: should trigger LOWER anomaly")
	}
}

// TestCoherenceRatioAnomaly_C5_CodeDomain documents why domain calibration matters:
// C5 with code domain lower bound (0.55) is NOT flagged as anomalous.
// This is correct — the ratio gate requires per-domain MeaningPoints calibration.
func TestCoherenceRatioAnomaly_C5_CodeDomain(t *testing.T) {
	if CoherenceRatioAnomaly(5863, 7323, 550, 1030) {
		t.Error("C5 ratio≈0.801 with code lower bound 0.550: should NOT be anomalous in code domain")
	}
	t.Log("CALIBRATION NOTE: C5 requires NL lower bound (0.85) to be caught. Code domain: use 0.55.")
}

// TestCoherenceRatioAnomaly_C3_CodeDomain verifies C3 (correct Go code) is NOT
// flagged with code domain lower bound. ratio = 5175/8101 = 0.639 > 0.550.
func TestCoherenceRatioAnomaly_C3_CodeDomain(t *testing.T) {
	if CoherenceRatioAnomaly(5175, 8101, 550, 1030) {
		t.Error("C3 code: ratio≈0.639 > 0.550 should NOT be anomalous — would false-reject correct code")
	}
}

// TestCoherenceRatioAnomaly_C6_CodeDomain verifies C6 (adapted HTTP handler) is
// not flagged with code domain bounds. ratio = 5812/9018 = 0.644 > 0.550.
func TestCoherenceRatioAnomaly_C6_CodeDomain(t *testing.T) {
	if CoherenceRatioAnomaly(5812, 9018, 550, 1030) {
		t.Error("C6 http adapted: ratio≈0.644 > 0.550 should NOT be anomalous")
	}
}

func TestCoherenceRatioAnomaly_ZeroSim(t *testing.T) {
	// sim=0 must not trigger anomaly (divide-by-zero guard)
	if CoherenceRatioAnomaly(5000, 0, 550, 1030) {
		t.Error("sim=0: should not trigger anomaly (div-by-zero guard)")
	}
}

// ── Full research matrix: C1-C6 gate sequence ─────────────────────────────────

// TestResearchMatrix_GateSequence runs each research case (C1-C6) through the
// AdaptiveCoherenceFloor and LoopClosureOK gates with the production cold-start
// hub_frontier (5500 bps). Validates the gate math from
// quality_matrix_research_v2.md §9 and §10.
//
// Expected outcome: ALL cases pass both gates — including the wrong answers C4
// and C5. This is the documented residual gap that requires verifier extension
// or coherence-ratio gate to close.
func TestResearchMatrix_GateSequence(t *testing.T) {
	const hubFrontier = int64(5500) // loopClosureDefaultBaselineBps (cold start)
	const marginBps = uint32(800)   // loopClosureMarginBps

	cases := []struct {
		name      string
		sim       uint32
		coherence uint32
		correct   bool
		wantPass  bool
		note      string
	}{
		// Correct answers — must pass all gates
		{"C1 binary→linear", 8065, 7700, true, true, "delta=+2200"},
		{"C2 fibonacci→tribonacci", 8452, 6500, true, true, "delta=+1000"},
		{"C3 counter→cache_race", 8101, 5175, true, true, "delta=-325 (within margin -800)"},
		{"C6 http client→server adapted", 9018, 5812, true, true, "delta=+312"},
		// Wrong answers — also pass (residual gap, fix pending)
		{"C4 sort asc→desc WRONG", 6832, 7221, false, true, "RESIDUAL GAP: high coherence anomaly (ratio=1.057)"},
		{"C5 L1→L2 explanation WRONG", 7323, 5863, false, true, "RESIDUAL GAP: ratio=0.801 caught by NL ratio gate"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			floor := AdaptiveCoherenceFloor(tc.sim)
			passFloor := tc.coherence >= floor
			passLoop := LoopClosureOK(tc.coherence, hubFrontier, marginBps)
			pass := passFloor && passLoop

			if pass != tc.wantPass {
				t.Errorf("floor=%d passFloor=%v delta=%d passLoop=%v pass=%v, want=%v",
					floor, passFloor, int64(tc.coherence)-hubFrontier, passLoop, pass, tc.wantPass)
			}

			if !tc.correct && pass {
				t.Logf("RESIDUAL GAP: wrong answer passes all gates. %s", tc.note)
			}

			t.Logf("sim=%d coh=%d correct=%v floor=%d passFloor=%v delta=%d passLoop=%v | %s",
				tc.sim, tc.coherence, tc.correct, floor, passFloor,
				int64(tc.coherence)-hubFrontier, passLoop, tc.note)
		})
	}
}

// TestResearchMatrix_RatioGateClosesGap verifies that the CoherenceRatioAnomaly
// gate (zero GPU cost) catches both residual gap cases (C4 and C5) when
// domain-appropriate lower bounds are used.
func TestResearchMatrix_RatioGateClosesGap(t *testing.T) {
	cases := []struct {
		name          string
		sim, coh      uint32
		lower, upper  uint32 // bounds × 1000
		wantAnomalous bool
		note          string
	}{
		// Residual gap cases — both caught by ratio gate
		{"C4 upper anomaly", 6832, 7221, 550, 1030, true, "ratio=1.057 > 1.030"},
		{"C5 NL lower anomaly", 7323, 5863, 850, 1030, true, "ratio=0.801 < 0.850"},
		// Correct cases — must NOT be flagged (code domain bounds)
		{"C1 normal", 8065, 7700, 550, 1030, false, "ratio=0.955 ∈ [0.55, 1.03]"},
		{"C3 code domain", 8101, 5175, 550, 1030, false, "ratio=0.639 ∈ [0.55, 1.03]"},
		{"C6 code domain", 9018, 5812, 550, 1030, false, "ratio=0.644 ∈ [0.55, 1.03]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CoherenceRatioAnomaly(tc.coh, tc.sim, tc.lower, tc.upper)
			if got != tc.wantAnomalous {
				t.Errorf("CoherenceRatioAnomaly(%d, %d, %d, %d) = %v, want %v | %s",
					tc.coh, tc.sim, tc.lower, tc.upper, got, tc.wantAnomalous, tc.note)
			}
		})
	}
}

// ── LoopClosureBreaks counter ─────────────────────────────────────────────────

// TestLoopClosureBreakCounter_StartsZero verifies the counter is zero on init.
func TestLoopClosureBreakCounter_StartsZero(t *testing.T) {
	sc := NewSemanticCache(fixedEmbedder{}, NopStore{}, 9700, "v1", 10, true)
	if got := sc.LoopClosureBreaks(); got != 0 {
		t.Errorf("expected LoopClosureBreaks=0 at start, got %d", got)
	}
}

// TestLoopClosureBreakCounter_Increments verifies RecordLoopClosureBreak
// increments atomically and LoopClosureBreaks() reads back correctly.
func TestLoopClosureBreakCounter_Increments(t *testing.T) {
	sc := NewSemanticCache(fixedEmbedder{}, NopStore{}, 9700, "v1", 10, true)
	sc.RecordLoopClosureBreak()
	sc.RecordLoopClosureBreak()
	sc.RecordLoopClosureBreak()
	if got := sc.LoopClosureBreaks(); got != 3 {
		t.Errorf("expected LoopClosureBreaks=3 after 3 breaks, got %d", got)
	}
}

// TestLoopClosureBreakCounter_IndependentOfCoherenceStats verifies that
// loop closure breaks do not pollute the coherence stats counters.
func TestLoopClosureBreakCounter_IndependentOfCoherenceStats(t *testing.T) {
	sc := NewSemanticCache(fixedEmbedder{}, NopStore{}, 9700, "v1", 10, true)

	sc.RecordCoherenceResult(6000, true)  // accepted
	sc.RecordLoopClosureBreak()           // loop closure — separate counter
	sc.RecordCoherenceResult(2000, false) // floor rejection — separate counter

	hits, rejections, _ := sc.CoherenceStats()
	if hits != 2 {
		t.Errorf("expected contextHitCount=2, got %d", hits)
	}
	if rejections != 1 {
		t.Errorf("expected coherenceRejections=1, got %d", rejections)
	}
	if breaks := sc.LoopClosureBreaks(); breaks != 1 {
		t.Errorf("expected loopClosureBreaks=1, got %d", breaks)
	}
}
