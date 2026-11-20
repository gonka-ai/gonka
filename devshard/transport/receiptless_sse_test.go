package transport

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
)

func TestReceiptlessSSEAccepted(t *testing.T) {
	const body = "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n"
	var output bytes.Buffer
	callbackCalled := false
	resp, err := (&HTTPClient{}).parseSSEResponse(context.Background(), strings.NewReader(body), &output, func(*host.HostResponse) {
		callbackCalled = true
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.False(t, callbackCalled)
	require.Empty(t, resp.Receipt)
	require.Empty(t, resp.Mempool)
	require.Contains(t, output.String(), `"content":"x"`)
	require.Contains(t, output.String(), "data: [DONE]")
}

func TestEmptyExecutorReceiptEnvelopeAccepted(t *testing.T) {
	const body = "data: {\"devshard_receipt\":{\"state_sig\":\"c2ln\",\"state_hash\":\"aGFzaA==\",\"nonce\":1}}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n"
	var output bytes.Buffer
	var callbackResponse *host.HostResponse
	resp, err := (&HTTPClient{}).parseSSEResponse(context.Background(), strings.NewReader(body), &output, func(h *host.HostResponse) {
		callbackResponse = h
	})
	require.NoError(t, err)
	require.NotNil(t, callbackResponse)
	require.Equal(t, []byte("sig"), callbackResponse.StateSig)
	require.Empty(t, callbackResponse.Receipt)
	require.Empty(t, resp.Receipt)
	require.Empty(t, resp.Mempool)
	require.Contains(t, output.String(), `"content":"x"`)
	require.Contains(t, output.String(), "data: [DONE]")
}
