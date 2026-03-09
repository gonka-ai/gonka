package completionapi

import "encoding/json"

// InjectCachedContext prepends a system message carrying a semantically related
// cached solution into the request messages array.
//
// This implements the L2 "context-augmented inference" model:
//   - On an L2 semantic cache hit the cached answer is NOT returned directly.
//   - Instead it is injected as a reference context so the LLM can adapt the
//     structural pattern to the specific problem in the current prompt.
//   - The GPU inference still runs → a new, correct answer is produced.
//   - The new answer is stored in the cache, potentially replacing a lower-
//     quality entry → the cache grows smarter over epochs (real-time learning).
//
// Why this matters for the quality guarantee:
//   L1 exact hit  → same bytes, same hash, on-chain verified → 100% correct.
//   L2 context hit → new GPU inference with reference context → correct by design.
//   L2 direct hit  → would return a mismatched answer (e.g. Counter fix for
//                    RateLimiter problem) → WRONG. This function prevents that.
//
// The system message is prepended (index 0) so it frames the entire conversation.
// Format is deliberately terse to minimise token overhead while giving the model
// enough signal to recognise the structural pattern.
func InjectCachedContext(requestBytes []byte, cachedContent string) ([]byte, error) {
	var requestMap map[string]interface{}
	if err := json.Unmarshal(requestBytes, &requestMap); err != nil {
		return nil, err
	}

	contextMsg := map[string]interface{}{
		"role": "system",
		"content": "A structurally related solution is available as a reference. " +
			"Use its pattern and approach to solve the current specific problem, " +
			"but adapt every detail (type names, field names, logic) to match " +
			"exactly what the current problem requires:\n\n" + cachedContent,
	}

	existing, _ := requestMap["messages"].([]interface{})
	requestMap["messages"] = append([]interface{}{contextMsg}, existing...)

	return json.Marshal(requestMap)
}

// ExtractCachedContent parses a cached ResponsePayload and returns the assistant
// message content as a plain string.
// Returns ("", false) if the payload cannot be parsed or has no content.
func ExtractCachedContent(responsePayload []byte) (string, bool) {
	var resp Response
	if err := json.Unmarshal(responsePayload, &resp); err != nil {
		return "", false
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message == nil {
		return "", false
	}
	content := resp.Choices[0].Message.Content
	if content == "" {
		return "", false
	}
	return content, true
}
