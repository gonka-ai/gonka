package limits

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// inflightInput reads what a host has taken on its input window the way a reader outside the package does.
func inflightInput(l *ParticipantLimiter, participant, model string) int64 {
	for _, window := range l.Snapshot() {
		if window.Participant == participant && window.Model == model {
			return window.InflightInputTokens
		}
	}
	return -1
}

// Test flow:
//  1. Build a limiter and fill the input window to `cfg.Pricing.Input.Initial` admitted requests.
//  2. Assert `Admits` now reports the window full.
//  3. Call `Overdraft` and assert it is admitted anyway, crossing the full window, and that its token is counted as in flight.
//  4. Release the overdraft and assert the in-flight count drops back to the window's size.
func TestAnOverdraftCrossesAFullWindowAndStillCountsItsTokens(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	l := newTestLimiter(cfg, fixedNow(testEpoch))

	for range cfg.Pricing.Input.Initial {
		require.True(t, l.admits("p", "m"), "the window has to fill before an overdraft means anything")
	}
	require.Equal(t, AdmissionWindowFull, l.Admits("p", "m"))

	release, admitted := l.Overdraft("p", "m", oneToken)
	require.Equal(t, AdmissionOpen, admitted, "an overdraft crosses a full window rather than asking it")
	require.Equal(t, cfg.Pricing.Input.Initial+1, inflightInput(l, "p", "m"), "an overdraft is in flight like any other request")

	release()
	require.Equal(t, cfg.Pricing.Input.Initial, inflightInput(l, "p", "m"), "an overdraft gives its tokens back")
}

// Test flow:
//  1. Trip the breaker by answering with `TransportFault` `cfg.AfterFailures` times.
//  2. Assert `Admits` reports the participant cut off.
//  3. Call `Overdraft` and assert it is refused with `AdmissionCutOff`, since an overdraft cannot cross a broken host.
func TestAnOverdraftIsRefusedByAnOpenCutOff(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	l := newTestLimiter(cfg, newMovingClock(testEpoch).now)

	for range cfg.AfterFailures {
		l.answered("p", "m", TransportFault)
	}
	require.Equal(t, AdmissionCutOff, l.Admits("p", "m"))

	_, admitted := l.Overdraft("p", "m", oneToken)
	require.Equal(t, AdmissionCutOff, admitted, "an overdraft is the window's business and never the cut-off's")
}

// Test flow:
//  1. Trip the breaker with `TransportFault` answers and advance the clock past the cut-off's expiry.
//  2. Acquire the single half-open probe and assert `Admits` still reports cut off while it is in flight.
//  3. Call `Overdraft` while the probe is outstanding and assert it is refused.
//  4. Release the probe and assert a subsequent `Overdraft` is admitted.
func TestAnOverdraftIsRefusedWhileAHalfOpenProbeIsInFlight(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	clock := newMovingClock(testEpoch)
	l := newTestLimiter(cfg, clock.now)

	for range cfg.AfterFailures {
		l.answered("p", "m", TransportFault)
	}
	clock.advance(l.states[key{participant: "p", model: "m"}].openUntil.Sub(clock.now()) + time.Millisecond)

	probe, admitted := l.Acquire("p", "m", oneToken)
	require.Equal(t, AdmissionOpen, admitted, "an elapsed cut-off leaves one probe to take")
	require.Equal(t, AdmissionCutOff, l.Admits("p", "m"), "a probe in flight is the cut-off still deciding")

	_, second := l.Overdraft("p", "m", oneToken)
	require.Equal(t, AdmissionCutOff, second, "the probe is the whole traffic a half-open host gets")

	probe()
	_, afterTheProbe := l.Overdraft("p", "m", oneToken)
	require.Equal(t, AdmissionOpen, afterTheProbe, "a half-open host with nothing in flight takes work again")
}
