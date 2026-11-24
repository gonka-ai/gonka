package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/goleak"

	"devshard/cmd/gateway/config"
)

// Test flow:
//  1. Start a race whose drain timeout comes from `drain_timeout_seconds` at `config.MinDrainTimeoutSeconds`, the smallest value `Config.Validate` accepts, under a policy whose receipt, first-token and stall deadlines are an hour away.
//  2. Let one host return its receipt and stream content, then disconnect the client while the host is still streaming within every deadline of its own, and wait for `run` to return `context.Canceled`, which it does once the race has read the departure.
//  3. Advance the clock to one minute short of the streaming hard timeout, where a drain shorter than it would end the attempt, then by the last minute.
//  4. Assert the race reports the attempt as `TerminalHardTimeout`, not `TerminalClientCancelled`: the host's own bound ended it, not the drain.
func TestTheSmallestValidDrainOutlastsTheDeadlinesARaceArmsForItself(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.DrainTimeout = DrainTimeoutFromConfig(config.Stream{DrainTimeoutSeconds: config.MinDrainTimeoutSeconds})
	fixture.clock.step = time.Second
	streaming := make(chan uint64, 1)
	fixture.host(220, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(220)},
		streaming: streaming,
		resume:    make(chan struct{}),
	})

	clientCtx, disconnect := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		_, err := fixture.run(clientCtx)
		returned <- err
	}()

	waitForValue(t, streaming, "the host to start streaming")
	disconnect()
	if err := waitForValue(t, returned, "the race to return on the client's departure"); !errors.Is(err, context.Canceled) {
		t.Fatalf("runRace error = %v, want context.Canceled", err)
	}
	fixture.clock.advance(streamingHardTimeout - time.Minute)
	fixture.clock.advance(time.Minute)

	reported := waitForValue(t, fixture.reported, "the race outcome")
	if terminal := reported.Attempts[0].Terminal; terminal != TerminalHardTimeout {
		t.Fatalf("terminal = %v, want hard timeout: the drain must not end an attempt still within its own deadlines", terminal)
	}
	assertOneSlotPerAttempt(t, fixture, len(reported.Attempts))
}

// Test flow:
//  1. Convert `config.MinDrainTimeoutSeconds` to a duration.
//  2. Assert it is at least the streaming hard timeout, the longest deadline an attempt arms for itself.
//  3. Assert the engine's own default drain is not below it.
func TestTheDrainFloorCoversTheStreamingHardTimeout(t *testing.T) {
	floor := time.Duration(config.MinDrainTimeoutSeconds) * time.Second
	if floor < streamingHardTimeout {
		t.Fatalf("config.MinDrainTimeoutSeconds = %s, want at least the streaming hard timeout %s", floor, streamingHardTimeout)
	}
	if defaultDrainTimeout < floor {
		t.Fatalf("defaultDrainTimeout = %s, want at least the floor %s", defaultDrainTimeout, floor)
	}
}
