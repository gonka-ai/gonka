package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"common/completionapi"
)

// Test flow:
//  1. Feed the client a stream of a receipt, a comment line, an answer chunk, [DONE] and a meta tail.
//  2. Assert the received hashes include the envelope of exactly the lines the executor stored, with no devshard framing.
func TestParseSSE_HashesTheLinesTheExecutorStoredWithoutProtocolEvents(t *testing.T) {
	answer := []string{
		": keep-alive",
		`data: {"choices":[{"delta":{"content":"Hi <b>"}}]}`,
		`data: [DONE]`,
	}
	stream := `data: {"devshard_receipt":{"nonce":1}}` + "\n\n" +
		answer[0] + "\n" + answer[1] + "\n\n" + answer[2] + "\n\n" +
		`data: {"devshard_meta":{}}` + "\n\n"
	client := streamBoundClient(DefaultMaxSSEStreamBytes)
	var forwarded bytes.Buffer

	response, err := client.parseSSEResponse(context.Background(), strings.NewReader(stream), &forwarded, nil)

	require.NoError(t, err)
	envelope, err := json.Marshal(completionapi.SerializedStreamedResponse{Events: answer})
	require.NoError(t, err)
	require.Contains(t, response.ReceivedResponseHashes, sha256.Sum256(envelope),
		"the hash is of the answer the host signed, not of the devshard framing around it")
}
