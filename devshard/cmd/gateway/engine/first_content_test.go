package engine

import (
	"testing"
	"time"
)

func classifiedState() (*attemptState, *sseClassifier) {
	return &attemptState{}, newSSEClassifier(testBudget(1<<20, 1<<20, 1<<20), "participant", false, nil)
}

// Test flow:
//  1. Classify a role-only chunk with empty content, then a chunk carrying real content, through a fresh `attemptState`/`sseClassifier`.
//  2. Assert the role chunk is not counted as content and the content chunk is.
//  3. Assert the state's first-token and first-content stamps can diverge when a role chunk arrives before content.
func TestFirstContentIsNotTheFirstChunk(t *testing.T) {
	t.Parallel()
	state, classifier := classifiedState()
	role := []byte(`data: {"choices":[{"delta":{"role":"assistant","content":""}}]}` + "\n\n")
	content := []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n")
	at := time.Unix(1786114580, 0)

	state.firstToken = at
	state.record(classifier.Classify(role))
	if classifier.Classify(role).Content {
		t.Fatal("a role chunk with an empty content is not content")
	}

	facts := classifier.Classify(content)
	if !facts.Content {
		t.Fatal("a chunk carrying content is content")
	}
	state.firstContent = at.Add(120 * time.Millisecond)

	if state.firstContent.Equal(state.firstToken) {
		t.Error("the two stamps must part when a role chunk arrives first")
	}
}

// Test flow:
//  1. Classify each of a reasoning chunk, a reasoning_content chunk, and a tool_calls chunk in turn.
//  2. Assert each is counted as content.
func TestFirstContentCountsReasoningAndToolCalls(t *testing.T) {
	t.Parallel()
	for _, chunk := range []string{
		`data: {"choices":[{"delta":{"reasoning":"thinking"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}` + "\n\n",
	} {
		_, classifier := classifiedState()
		if !classifier.Classify([]byte(chunk)).Content {
			t.Errorf("not counted as content: %s", chunk)
		}
	}
}

// Test flow:
//  1. Classify each of an empty tool_calls array, an empty delta, and a role-only delta in turn.
//  2. Assert none of them is counted as content.
func TestFirstContentIgnoresAnEmptyOpening(t *testing.T) {
	t.Parallel()
	for _, chunk := range []string{
		`data: {"choices":[{"delta":{"tool_calls":[]}}]}` + "\n\n",
		`data: {"choices":[{"delta":{}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n",
	} {
		_, classifier := classifiedState()
		if classifier.Classify([]byte(chunk)).Content {
			t.Errorf("counted as content but carries none: %s", chunk)
		}
	}
}
