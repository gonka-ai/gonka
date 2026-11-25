package engine

import (
	"context"
	"testing"

	"devshard/cmd/gateway/config"
)

type secondCallHeldClient struct {
	recordingSink
	holdsFlush bool
	calls      int
	entered    chan struct{}
	release    chan struct{}
}

func (c *secondCallHeldClient) Write(chunk []byte) (int, error) {
	if !c.holdsFlush {
		c.holdOnSecondCall()
	}
	return c.recordingSink.Write(chunk)
}

func (c *secondCallHeldClient) Flush() {
	if c.holdsFlush {
		c.holdOnSecondCall()
	}
	c.recordingSink.Flush()
}

func (c *secondCallHeldClient) holdOnSecondCall() {
	c.calls++
	if c.calls == 2 {
		c.entered <- struct{}{}
		<-c.release
	}
}

// Test flow:
//  1. Start a race under the default escalation policy whose first host receipts, streams three content chunks back to back and finishes its nonce, with a second host queued for a rescue, against a client that takes the first chunk and holds the second: once in its write with no flushes, once in its flush with a flush after each chunk.
//  2. Once the second call is held, move the clock on by the default inter-chunk stall, 30 seconds, as long as a client write may block before its deadline fails it, then let the client take the chunk.
//  3. Assert the race reports one attempt, won, and never dispatched the second host: a crowned race does not escalate on a stall.
//  4. Assert the winner's longest mid-stream gap is zero: the 30 seconds the client held the chunk are not the host's silence.
func TestAClientHoldingAWriteIsNotBookedAsTheWinningHostsSlowChunk(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		holdsFlush bool
	}{
		{"held in the write with no flushes", false},
		{"held in the flush", true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client := &secondCallHeldClient{holdsFlush: testCase.holdsFlush, entered: make(chan struct{}, 1), release: make(chan struct{})}
			policy := EscalationPolicyFromConfig(config.Defaults().Engine)
			fixture := newRaceFixture(policy, 2)
			fixture.request.Client = client
			rescued := make(chan uint64, 1)
			fixture.host(60, 0, "host-0", &hostScript{receipt: true, chunks: []string{contentChunk(60), contentChunk(60), contentChunk(60)}, flush: testCase.holdsFlush, confirmed: true, finished: true})
			fixture.host(61, 1, "host-1", &hostScript{arrive: rescued, receipt: true, hold: true})

			ran := make(chan error, 1)
			go func() {
				_, err := fixture.run(context.Background())
				ran <- err
			}()
			waitForValue(t, client.entered, "the winner's second chunk being held by the client")
			fixture.clock.advance(policy.InterChunkStall)
			close(client.release)
			if err := waitForValue(t, ran, "runRace returning"); err != nil {
				t.Fatalf("runRace error = %v, want nil", err)
			}

			reported := waitForValue(t, fixture.reported, "the race outcome being reported")
			if len(reported.Attempts) != 1 || len(rescued) != 0 {
				t.Fatalf("attempts = %d, rescues dispatched = %d; want the winner alone", len(reported.Attempts), len(rescued))
			}
			winner := reported.Attempts[0]
			if winner.Terminal != TerminalWon {
				t.Fatalf("winner terminal = %v, want %v", winner.Terminal, TerminalWon)
			}
			if winner.MaxChunkGap != 0 {
				t.Fatalf("winner's longest mid-stream gap = %v, want 0: the host sent its chunks back to back and only the client held one", winner.MaxChunkGap)
			}
		})
	}
}
