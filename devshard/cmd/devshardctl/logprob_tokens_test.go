package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sseEvent wraps a recorded chunk body the way a host puts it on the wire.
func sseEvent(t *testing.T, fixture string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("reading %s: %v", fixture, err)
	}
	return []byte("data: " + strings.Join(strings.Fields(string(body)), "") + "\n\n")
}

// Both fixtures are trimmed from real answers to the same prompt: one host reported token ids, another
// reported the text those ids decode to. Hand-written JSON would only prove the parser reads what it
// was written against.
func TestSSEChunkLogprobsDecodedOnRecordedAnswers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		event       []byte
		wantDecoded bool
		wantFound   bool
	}{
		{
			name:      "a host reporting token ids",
			event:     sseEvent(t, "logprobs_token_ids.json"),
			wantFound: true,
		},
		{
			name:        "a host reporting the decoded text instead",
			event:       sseEvent(t, "logprobs_decoded_text.json"),
			wantDecoded: true,
			wantFound:   true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decoded, found := sseChunkLogprobsDecoded(testCase.event)

			if decoded != testCase.wantDecoded || found != testCase.wantFound {
				t.Fatalf("sseChunkLogprobsDecoded() = (%v, %v), want (%v, %v)",
					decoded, found, testCase.wantDecoded, testCase.wantFound)
			}
		})
	}
}

// The validator rejects on the first token of either kind it cannot replay, so a numeric first token
// hides nothing: later tokens and every alternative are judged too.
func TestSSEChunkLogprobsDecodedJudgesEveryTokenAndAlternative(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		chunk       string
		wantDecoded bool
	}{
		{
			name:  "every token and alternative names an id",
			chunk: `data: {"choices":[{"logprobs":{"content":[{"token":"758","top_logprobs":[{"token":"12"}]},{"token":"91","top_logprobs":[{"token":"7"}]}]}}]}` + "\n",
		},
		{
			name:        "a later token names its text",
			chunk:       `data: {"choices":[{"logprobs":{"content":[{"token":"758"},{"token":"The"}]}}]}` + "\n",
			wantDecoded: true,
		},
		{
			name:        "an alternative of the first token names its text",
			chunk:       `data: {"choices":[{"logprobs":{"content":[{"token":"758","top_logprobs":[{"token":"12"},{"token":"The"}]}]}}]}` + "\n",
			wantDecoded: true,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decoded, found := sseChunkLogprobsDecoded([]byte(testCase.chunk))

			if !found {
				t.Fatalf("sseChunkLogprobsDecoded() found no token to judge in %s", testCase.chunk)
			}
			if decoded != testCase.wantDecoded {
				t.Errorf("sseChunkLogprobsDecoded() decoded = %v, want %v", decoded, testCase.wantDecoded)
			}
		})
	}
}

// Nothing to judge must stay nothing to judge: a chunk the detector cannot read leaves the attempt
// unjudged rather than accusing the host on the strength of a parse failure.
func TestSSEChunkLogprobsDecodedJudgesNothingWithoutATokenToRead(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		chunk string
	}{
		{name: "a content chunk carries no logprobs", chunk: `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n"},
		{name: "logprobs present but empty", chunk: `data: {"choices":[{"logprobs":{"content":[]}}]}` + "\n"},
		{name: "the stream terminator", chunk: "data: [DONE]\n"},
		{name: "a fragment split mid-event", chunk: `data: {"choices":[{"logprobs":{"cont` + "\n"},
		{name: "an empty read", chunk: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			decoded, found := sseChunkLogprobsDecoded([]byte(testCase.chunk))

			if decoded || found {
				t.Fatalf("sseChunkLogprobsDecoded() = (%v, %v), want (false, false)", decoded, found)
			}
		})
	}
}

func TestIsTokenIDAcceptsOnlyAnIndexIntoTheVocabulary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		token string
		want  bool
	}{
		{token: "0", want: true},
		{token: "758", want: true},
		{token: "The"},
		{token: "-1"},
		{token: ""},
		{token: " 758"},
		{token: "758.0"},
	}

	for _, testCase := range cases {
		t.Run("token "+testCase.token, func(t *testing.T) {
			t.Parallel()
			if got := isTokenID(testCase.token); got != testCase.want {
				t.Fatalf("isTokenID(%q) = %v, want %v", testCase.token, got, testCase.want)
			}
		})
	}
}

// A streaming chunk carries only a couple of tokens, so the judgement cannot stop at the first one: an
// answer that opens with a numeric token would otherwise mask every word that follows it.
func TestRaceWriterJudgesLogprobsBeyondTheFirstChunk(t *testing.T) {
	ctx := context.Background()
	var sink bytes.Buffer
	inf := &inflight{
		hostID: "host-A", escrowID: "escrow-x", nonce: 1,
		done: make(chan struct{}), receiptCh: make(chan struct{}), firstTokenCh: make(chan struct{}),
	}
	rw := &raceWriter{group: newRaceGroup(ctx, ctx, "escrow-x", &sink), nonce: 1, inf: inf}

	rw.classifyParseable(sseEvent(t, "logprobs_token_ids.json"))
	if inf.logprobsDecoded {
		t.Fatal("a chunk naming every token by id must not read as decoded text")
	}

	rw.classifyParseable(sseEvent(t, "logprobs_decoded_text.json"))
	if !inf.logprobsDecoded {
		t.Fatal("a later chunk carrying decoded text went unjudged")
	}
}
