package main

import (
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
