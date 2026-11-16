package metrics

import (
	"testing"
	"time"
)

// Test flow:
//  1. Build a `ShadowProbeRecorder` and record one answered streamed probe, one answered probe with no streamed byte and one unanswered probe for the same participant.
//  2. Assert the probe counter counts each outcome separately, the first-token histogram holds only the streamed probe, the duration histogram both answered ones, and the output tokens are summed.
func TestTheShadowProbeRecorderSeparatesOutcomesAndTimings(t *testing.T) {
	telemetry := New()
	recorder := NewShadowProbeRecorder(telemetry)

	recorder.ShadowProbeAnswered("gonka1shadow", "served", true, 300*time.Millisecond, time.Second, 5)
	recorder.ShadowProbeAnswered("gonka1shadow", "served", false, 0, 2*time.Second, 7)
	recorder.ShadowProbeUnanswered("gonka1shadow", "failed")

	expectCounter(t, telemetry, "devshard_gateway_shadow_probes_total", labels{"participant": "gonka1shadow", "outcome": "served"}, 2)
	expectCounter(t, telemetry, "devshard_gateway_shadow_probes_total", labels{"participant": "gonka1shadow", "outcome": "failed"}, 1)
	expectCounter(t, telemetry, "devshard_gateway_shadow_probe_output_tokens_total", labels{"participant": "gonka1shadow"}, 12)
	expectHistogram(t, telemetry, "devshard_gateway_shadow_probe_first_token_seconds", labels{"participant": "gonka1shadow"}, 1, 0.3)
	expectHistogram(t, telemetry, "devshard_gateway_shadow_probe_duration_seconds", labels{"participant": "gonka1shadow"}, 2, 3)
}
