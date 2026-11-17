package engine_test

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/metrics"
	"devshard/cmd/gateway/perf"
)

const (
	attemptsTerminalFamily = "devshard_gateway_attempts_terminal_total"
	attemptFailuresFamily  = "devshard_gateway_attempt_failures_total"
	hiddenFailuresFamily   = "devshard_gateway_user_requests_with_hidden_failure_total"
	requestsFamily         = "devshard_gateway_requests_total"
)

var (
	winnerOutcomeFamilies = []string{attemptsTerminalFamily, attemptFailuresFamily, hiddenFailuresFamily, requestsFamily}
	recorderEpoch         = time.Unix(1700000000, 0)
)

func newWinnerRecorder() (*metrics.Metrics, *metrics.RaceRecorder) {
	telemetry := metrics.New()
	recorder := metrics.NewRaceRecorder(telemetry, func() time.Time { return recorderEpoch }, func() time.Duration { return time.Hour })
	return telemetry, recorder
}

func counterSeries(t *testing.T, telemetry *metrics.Metrics) map[string]float64 {
	t.Helper()
	gathered, err := telemetry.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	series := map[string]float64{}
	for _, family := range gathered {
		if !slices.Contains(winnerOutcomeFamilies, family.GetName()) {
			continue
		}
		for _, metric := range family.GetMetric() {
			pairs := make([]string, 0, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				if label.GetName() != "model" {
					pairs = append(pairs, label.GetName()+"="+label.GetValue())
				}
			}
			series[family.GetName()+"{"+strings.Join(pairs, ",")+"}"] = metric.GetCounter().GetValue()
		}
	}
	return series
}

func assertSeries(t *testing.T, telemetry *metrics.Metrics, want map[string]float64) {
	t.Helper()
	if got := counterSeries(t, telemetry); !maps.Equal(got, want) {
		t.Fatalf("counters = %v, want %v", got, want)
	}
}

func assertOneHostSample(t *testing.T, samples []perf.Sample, responsive bool) {
	t.Helper()
	if len(samples) != 1 || samples[0].ParticipantKey != "host-0" || samples[0].Responsive != responsive {
		t.Fatalf("samples = %+v, want one host-0 sample with Responsive %v", samples, responsive)
	}
}

func assertDecidedAfterRaceEnd(t *testing.T, telemetry *metrics.Metrics) func(samples []perf.Sample) {
	t.Helper()
	return func(samples []perf.Sample) {
		assertSeries(t, telemetry, map[string]float64{
			requestsFamily + "{outcome=success,reason=none}": 1,
		})
		if len(samples) != 0 {
			t.Fatalf("samples at race end = %+v, want none while the winner's nonce is open", samples)
		}
	}
}

// Test flow:
//  1. Build a real race recorder, park the votes of a one-host simulator, and race host-0, whose whole answer ends with its nonce still open.
//  2. At race end, assert no perf sample, no terminal, attempt-failure or hidden-failure series, and the request counted as a success.
//  3. Apply the winner's finish before its vote returns and stop the engine.
//  4. Assert one responsive host-0 sample, one successful user-visible winner terminal, and still no attempt failure or hidden failure.
func TestServedWinnerFinishedBeforeItsVoteIsRecordedAsAnOnTimeWinner(t *testing.T) {
	telemetry, recorder := newWinnerRecorder()

	samples := engine.RunServedWinnerFinishedBeforeItsVote(t, recorder, assertDecidedAfterRaceEnd(t, telemetry))

	assertOneHostSample(t, samples, true)
	assertSeries(t, telemetry, map[string]float64{
		attemptsTerminalFamily + "{outcome=success,participant_key=host-0,role=primary,visibility=user_visible_winner}": 1,
		requestsFamily + "{outcome=success,reason=none}":                                                                1,
	})
}

// Test flow:
//  1. Build a real race recorder, let a one-host simulator's vote timers fire only on command, and race host-0, whose whole answer ends with its nonce never finishing.
//  2. At race end, assert no perf sample, no terminal, attempt-failure or hidden-failure series, and the request counted as a success.
//  3. Fire the winner's vote deadline and stop the engine.
//  4. Assert one unresponsive host-0 sample, one failed_not_finished terminal, one not_finished attempt failure, and one not_finished hidden failure on the successful request.
func TestServedWinnerUnfinishedAtItsVoteIsRecordedAsNotFinished(t *testing.T) {
	telemetry, recorder := newWinnerRecorder()

	samples := engine.RunServedWinnerUnfinishedAtItsVote(t, recorder, assertDecidedAfterRaceEnd(t, telemetry))

	assertOneHostSample(t, samples, false)
	assertSeries(t, telemetry, map[string]float64{
		attemptsTerminalFamily + "{outcome=failed,participant_key=host-0,role=primary,visibility=failed_not_finished}":     1,
		attemptFailuresFamily + "{participant_key=host-0,reason=not_finished,role=primary,visibility=failed_not_finished}": 1,
		hiddenFailuresFamily + "{reason=not_finished}":                                                                     1,
		requestsFamily + "{outcome=success,reason=none}":                                                                   1,
	})
}
