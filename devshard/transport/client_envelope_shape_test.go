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

func TestDecodeInferenceResponseJSON_OversizedMempoolIsDropped(t *testing.T) {
	entries := strings.TrimSuffix(strings.Repeat(`"",`, maxResponseMempoolEntries+1), ",")
	resp, err := decodeInferenceResponseJSON([]byte(`{"mempool":[` + entries + `],"nonce":7}`))
	require.NoError(t, err)
	require.Empty(t, resp.Mempool)
	require.EqualValues(t, 7, resp.Nonce)
}
