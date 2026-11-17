package filters

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// Test flow:
//  1. Build an SSE stream of 400 padded content deltas.
//  2. Fold the stream through a BodyFolder.
//  3. Assert the sampled early, mid and late tokens all survive in the folded body.
//  4. Assert the folded body matches what assembleSSEBody produces from the same stream.
func TestFoldingKeepsEveryTokenPastTheMeasurementInterval(t *testing.T) {
	const tokens = 400
	padding := strings.Repeat("p", 1024)
	var stream bytes.Buffer
	for token := range tokens {
		fmt.Fprintf(&stream, "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"t%d %s \"}}]}\n\n", token, padding)
	}
	stream.WriteString("data: [DONE]\n\n")

	folder := NewBodyFolder(LogprobIntent{})
	if _, err := folder.Write(stream.Bytes()); err != nil {
		t.Fatalf("Write(): %v", err)
	}
	body := folder.Body()

	for _, token := range []int{0, 1, 199, 200, 399} {
		if !bytes.Contains(body, fmt.Appendf(nil, "t%d ", token)) {
			t.Fatalf("token t%d is missing from the folded answer: %s", token, truncate(string(body)))
		}
	}
	if want := string(assembleSSEBody(stream.Bytes())); string(body) != want {
		t.Errorf("folded = %s\n want = %s", truncate(string(body)), truncate(want))
	}
}

// Test flow:
//  1. Write 20 chunks of 64KB content, each to a distinct choice index, through a BodyFolder.
//  2. Read the folder's Held() count and the actual folded body length.
//  3. Assert Held() tracks at least half of what the fold actually holds.
func TestHeldTracksWhatIsActuallyAccumulated(t *testing.T) {
	folder := NewBodyFolder(LogprobIntent{})
	content := strings.Repeat("x", 64<<10)
	for event := range 20 {
		chunk := fmt.Appendf(nil, "data: {\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":%d,\"delta\":{\"content\":%q}}]}\n\n", event, content)
		if _, err := folder.Write(chunk); err != nil {
			t.Fatalf("Write(): %v", err)
		}
	}

	held, actual := folder.Held(), int64(len(folder.Body()))
	if held < actual/2 {
		t.Errorf("Held() = %d while the fold actually holds %d: the cap and the budget both read low", held, actual)
	}
}

func truncate(body string) string {
	if len(body) <= 200 {
		return body
	}
	return body[:200] + "..."
}
