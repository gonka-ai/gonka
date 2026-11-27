package transport

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
)

// Test flow:
//  1. Parse an SSE line whose object carries 257 `devshard_`-prefixed keys and no protocol envelope.
//  2. Assert it is forwarded to the client as data rather than read as an envelope.
func TestParseSSE_WideEnvelopeCandidateIsForwardedAsData(t *testing.T) {
	keys := make([]string, 0, 256+1)
	for i := 0; i <= 256; i++ {
		keys = append(keys, fmt.Sprintf(`"devshard_k%d":""`, i))
	}
	line := "data: {" + strings.Join(keys, ",") + "}\n\n"
	client := streamBoundClient(DefaultMaxSSEStreamBytes)
	var forwarded bytes.Buffer

	_, err := client.parseSSEResponse(context.Background(), strings.NewReader(line+sseTerminator), &forwarded, nil)

	require.NoError(t, err)
	require.Contains(t, forwarded.String(), `"devshard_k0":""`)
}

// Test flow:
//  1. Parse a receipt line padded with 256 extra members, and the same line made invalid by a malformed member.
//  2. Assert the padded receipt is read and not forwarded, and the invalid one is forwarded as data without a receipt.
func TestParseSSE_ReceiptProjectionHandlesPaddingAndRejectsMalformedJSON(t *testing.T) {
	var padding strings.Builder
	for i := 0; i < 256; i++ {
		fmt.Fprintf(&padding, `,"k%d":0`, i)
	}
	client := streamBoundClient(DefaultMaxSSEStreamBytes)
	for _, tc := range []struct {
		name  string
		json  string
		nonce uint64
	}{
		{"wide", `{"devshard_receipt":{"nonce":7}` + padding.String() + "}", 7},
		{"malformed", `{"devshard_receipt":{"nonce":7},"extension":{[}` + padding.String() + "}", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var forwarded bytes.Buffer
			calls := 0
			result, err := client.parseSSEResponse(context.Background(), strings.NewReader("data: "+tc.json+"\n\n"+sseTerminator), &forwarded, func(*host.HostResponse) { calls++ })
			require.NoError(t, err)
			require.Equal(t, tc.nonce, result.Nonce)
			if tc.nonce == 0 {
				require.Zero(t, calls)
				require.Contains(t, forwarded.String(), tc.json)
			} else {
				require.Equal(t, 1, calls)
				require.NotContains(t, forwarded.String(), tc.json)
			}
		})
	}
}

// Test flow:
//  1. Decode a JSON inference response whose mempool is one entry past the limit, under a key differing only in case, and one at the limit.
//  2. Assert the response past the limit decodes with no mempool and the rest of its fields, and the one at the limit keeps its mempool.
func TestDecodeInferenceResponseJSON_OversizedMempoolIsDropped(t *testing.T) {
	entries := func(count int) string {
		return strings.TrimSuffix(strings.Repeat(`"",`, count), ",")
	}
	past, err := decodeInferenceResponseJSON([]byte(`{"MEMPOOL":[` + entries(maxResponseMempoolEntries+1) + `],"nonce":7}`))
	require.NoError(t, err)
	require.Empty(t, past.Mempool)
	require.EqualValues(t, 7, past.Nonce)

	within, err := decodeInferenceResponseJSON([]byte(`{"mempool":[` + entries(maxResponseMempoolEntries) + `],"nonce":7}`))
	require.NoError(t, err)
	require.Len(t, within.Mempool, maxResponseMempoolEntries)
}

// Test flow:
//  1. Check bodies with a top-level `message` that encoding/json accepts and that only the lenient decoder accepts, and bodies without one.
//  2. Assert every body with a top-level `message` the lenient decoder reads is rejected, as before the response limits, and the rest pass.
func TestRejectDeprecatedJSONEnvelopeKeysKeepsItsVerdicts(t *testing.T) {
	for _, body := range []string{`{"message":"x"}`, `{"message":null}`, `{"message":01}`} {
		require.Errorf(t, rejectDeprecatedJSONEnvelopeKeys([]byte(body)), "%s", body)
	}
	for _, body := range []string{`{"Message":"x"}`, `{"x":{"message":1}}`, `["message"]`, `not json`} {
		require.NoErrorf(t, rejectDeprecatedJSONEnvelopeKeys([]byte(body)), "%s", body)
	}
}
