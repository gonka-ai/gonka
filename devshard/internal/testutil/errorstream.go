package testutil

import "strings"

// LongErrorStream returns an SSE error stream whose error lies past a retention bound: 255-byte content events until the data lines, each counted as its bytes plus one separator, exceed retainedBytes, then a terminal error event and `[DONE]`.
func LongErrorStream(retainedBytes int) []byte {
	content := `data: {"choices":[{"delta":{"content":"` + strings.Repeat("x", 211) + `"}}]}`
	var stream strings.Builder
	for held := 0; held <= retainedBytes; held += len(content) + 1 {
		stream.WriteString(content + "\n\n")
	}
	stream.WriteString(`data: {"error":{"code":500,"message":"out of memory","type":"InternalServerError"}}` + "\n\n")
	stream.WriteString("data: [DONE]\n\n")
	return []byte(stream.String())
}
