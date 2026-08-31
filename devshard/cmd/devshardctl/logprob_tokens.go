package main

import (
	"bytes"
	"encoding/json"
	"strconv"

	"common/completionapi"
)

var sseLogprobsKeyMarker = []byte(`"logprobs"`)

// isTokenID mirrors the rule a validator applies to the same field
// (common/validation.HasNonNumericTokens): a token names an id, never its text.
func isTokenID(token string) bool {
	id, err := strconv.Atoi(token)
	return err == nil && id >= 0
}

// sseChunkLogprobsDecoded reports whether a chunk names logprob tokens by their decoded text instead of
// their id, and whether any token was found to judge. A validator replays the inference from these ids;
// text cannot be replayed, so it votes the answer invalid and the host loses the reward.
//
// It decodes into completionapi.Response so the two logprob shapes -- chat `content` and completions
// `tokens` -- are normalized the same way the validator normalizes them. A host forwards logprobs only to a
// client that asked, so ordinary traffic leaves nothing here to judge.
func sseChunkLogprobsDecoded(payload []byte) (decoded, found bool) {
	if len(payload) == 0 || !bytes.Contains(payload, sseLogprobsKeyMarker) {
		return false, false
	}
	for _, line := range bytes.Split(payload, []byte("\n")) {
		event, ok := sseEventData(line)
		if !ok {
			continue
		}
		var response completionapi.Response
		if err := json.Unmarshal(event, &response); err != nil {
			continue
		}
		for _, choice := range response.Choices {
			for _, token := range choice.Logprobs.Content {
				return !isTokenID(token.Token), true
			}
		}
	}
	return false, false
}

// sseEventData returns one SSE line's JSON payload, skipping the terminator and anything not an event.
func sseEventData(line []byte) ([]byte, bool) {
	line = bytes.TrimRight(line, "\r")
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	event := bytes.TrimSpace(line[len("data:"):])
	if len(event) == 0 || bytes.Equal(event, []byte("[DONE]")) {
		return nil, false
	}
	return event, true
}
