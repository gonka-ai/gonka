package engine

import (
	"testing"
	"time"
)

// stream replays a host's chunk timings straight through the state the writer owns.
func stream(state *attemptState, start time.Time, chunks ...struct {
	after   time.Duration
	payload string
},
) {
	for _, chunk := range chunks {
		state.streamChunks++
		state.outputBytes += int64(len(chunk.payload))
		at := start.Add(chunk.after)
		if state.firstToken.IsZero() {
			state.firstToken = at
		}
		state.recordChunkGap(at, []byte(chunk.payload))
	}
}

type timedChunk = struct {
	after   time.Duration
	payload string
}

// Test flow:
//  1. Replay a steady stream of three chunks 40ms apart, and a stalled stream with the same three chunks but a 55-second gap in the middle, both through `stream`.
//  2. Assert the steady attempt's `maxChunkGap` is 40ms and the stalled attempt's is 55s.
//  3. Assert both attempts recorded the same chunk count, so the gap comparison is meaningful.
func TestAttemptCadence_PartsAStalledHostFromASlowOne(t *testing.T) {
	t.Parallel()
	start := time.Unix(1786114580, 0)
	content := `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"

	steady := &attemptState{}
	stream(steady, start,
		timedChunk{0, content}, timedChunk{40 * time.Millisecond, content}, timedChunk{80 * time.Millisecond, content})

	stalled := &attemptState{}
	stream(stalled, start,
		timedChunk{0, content}, timedChunk{55 * time.Second, content}, timedChunk{55*time.Second + 40*time.Millisecond, content})

	if steady.maxChunkGap != 40*time.Millisecond {
		t.Errorf("steady maxChunkGap = %v, want 40ms", steady.maxChunkGap)
	}
	if stalled.maxChunkGap != 55*time.Second {
		t.Errorf("stalled maxChunkGap = %v, want 55s", stalled.maxChunkGap)
	}
	if steady.streamChunks != stalled.streamChunks {
		t.Fatalf("the chunk counts must be equal for the comparison to mean anything")
	}
}

// Test flow:
//  1. Replay a content chunk followed by a `[DONE]` chunk 90 seconds later through `stream`.
//  2. Assert `maxChunkGap` stays 0 — the silence before `[DONE]` is not counted as a stall.
func TestAttemptCadence_IgnoresTheSilenceBeforeDone(t *testing.T) {
	t.Parallel()
	start := time.Unix(1786114580, 0)
	content := `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"

	ended := &attemptState{}
	stream(ended, start, timedChunk{0, content}, timedChunk{90 * time.Second, "data: [DONE]\n\n"})

	if ended.maxChunkGap != 0 {
		t.Errorf("maxChunkGap = %v, want 0", ended.maxChunkGap)
	}
}

// Test flow:
//  1. Replay four chunks through `stream`, with a 30-second gap between the second and third.
//  2. Assert `maxGapChunk` names chunk 3 as the one the largest gap preceded.
//  3. Assert `outputBytes` totals the bytes of all four chunks.
func TestAttemptCadence_NamesTheChunkTheSilenceFollowed(t *testing.T) {
	t.Parallel()
	start := time.Unix(1786114580, 0)
	content := `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"

	state := &attemptState{}
	stream(state, start,
		timedChunk{0, content}, timedChunk{10 * time.Millisecond, content},
		timedChunk{30 * time.Second, content}, timedChunk{30*time.Second + 10*time.Millisecond, content})

	if state.maxGapChunk != 3 {
		t.Errorf("maxGapChunk = %d, want 3", state.maxGapChunk)
	}
	if state.outputBytes != int64(4*len(content)) {
		t.Errorf("outputBytes = %d, want %d", state.outputBytes, 4*len(content))
	}
}

// Test flow:
//  1. Replay four evenly-spaced chunks (100ms/200ms/300ms gaps) through `stream` and compute `meanChunkGap`.
//  2. Assert the mean gap is 200ms.
//  3. Replay a single chunk and assert both its mean gap and max gap are 0.
func TestAttemptCadence_AveragesTheGapsAndReportsNothingBeforeASecondChunk(t *testing.T) {
	t.Parallel()
	start := time.Unix(1786114580, 0)
	content := `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n"

	averaged := &attemptState{}
	stream(averaged, start,
		timedChunk{0, content}, timedChunk{100 * time.Millisecond, content},
		timedChunk{300 * time.Millisecond, content}, timedChunk{600 * time.Millisecond, content})
	if averaged.meanChunkGap() != 200*time.Millisecond {
		t.Errorf("meanChunkGap = %v, want 200ms", averaged.meanChunkGap())
	}

	lone := &attemptState{}
	stream(lone, start, timedChunk{0, content})
	if lone.meanChunkGap() != 0 || lone.maxChunkGap != 0 {
		t.Errorf("a single chunk has no gap: mean=%v max=%v", lone.meanChunkGap(), lone.maxChunkGap)
	}
}

// Test flow:
//  1. Build an `AttemptSpec` with an events channel buffered for one event.
//  2. Offer a chunk event (accepted), then offer another with no room left (reports the drop).
//  3. Drain the channel and offer again, asserting the freed room is usable.
func TestAttemptOffer_ReportsTheProgressItCouldNotHandOver(t *testing.T) {
	t.Parallel()
	events := make(chan AttemptEvent, 1)
	spec := AttemptSpec{Events: events}

	if !spec.offer(AttemptEvent{Kind: AttemptChunk}) {
		t.Fatal("the first offer had room and must be taken")
	}
	if spec.offer(AttemptEvent{Kind: AttemptChunk}) {
		t.Error("an offer with no room must report the drop, not swallow it")
	}

	<-events
	if !spec.offer(AttemptEvent{Kind: AttemptChunk}) {
		t.Error("room freed must be usable again")
	}
}
