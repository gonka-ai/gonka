package engine

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Test flow:
//  1. For each case, start a one-host simulator whose host streams content and reports a completion token count against a 256-token reservation, either through usage or through logprobs alone.
//  2. Run the race and wait for its report.
//  3. Assert the client still received every byte the host sent.
//  4. Assert an output overrun was recorded against the host only when its count passed the reservation.
func TestAnAttemptPastItsReservedOutputIsRecordedAgainstItsHost(t *testing.T) {
	logprobChunk := func(tokens int) string {
		entries := ""
		for index := range tokens {
			if index > 0 {
				entries += ","
			}
			entries += `{"token":"a","logprob":-0.1}`
		}
		return fmt.Sprintf(`data: {"choices":[{"delta":{"content":"a"},"logprobs":{"content":[%s]}}]}`+"\n\n", entries)
	}
	usageChunk := func(tokens int) string {
		return fmt.Sprintf(`data: {"choices":[],"usage":{"completion_tokens":%d}}`+"\n\n", tokens)
	}
	cases := []struct {
		name         string
		chunks       []string
		wantOverruns int
	}{
		{name: "usage past the reservation", chunks: []string{contentEvent("hello"), usageChunk(300)}, wantOverruns: 1},
		{name: "usage at the reservation", chunks: []string{contentEvent("hello"), usageChunk(256)}, wantOverruns: 0},
		{name: "logprobs past the reservation", chunks: []string{logprobChunk(257)}, wantOverruns: 1},
		{name: "logprobs inside the reservation", chunks: []string{logprobChunk(10)}, wantOverruns: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			sim := newSimulator(t, settledPolicy(), 1, qwenModel)
			sim.clock.step = time.Millisecond
			sim.host(10, 0, "host-0", &hostScript{receipt: true, chunks: testCase.chunks, confirmed: true, finished: true})

			if _, err := sim.run(context.Background()); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			sim.reported(t)

			want := ""
			for _, chunk := range testCase.chunks {
				want += chunk
			}
			if got := string(sim.client.forwarded()); got != want {
				t.Fatalf("client received %q, want every byte the host sent %q", got, want)
			}
			overruns := sim.perf.outputOverruns()
			if len(overruns) != testCase.wantOverruns {
				t.Fatalf("output overruns = %v, want %d", overruns, testCase.wantOverruns)
			}
			for _, participant := range overruns {
				if participant != "host-0" {
					t.Fatalf("overrun recorded against %q, want host-0", participant)
				}
			}
		})
	}
}
