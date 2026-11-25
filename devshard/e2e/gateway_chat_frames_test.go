package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/e2e/testutil"
)

// Test flow:
//  1. Start the new gateway and three real hosts with default joined or explicit separate frame scheduling.
//  2. Request a streaming completion, then a buffered completion, recording the session nonce before each.
//  3. Require an authorized signed observation for a newer nonce after each response and no signature warning.
func TestE2E_GatewayVerifiesSignedChatFrames(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		variables map[string]string
	}{
		{name: "joined"},
		{name: "separate", variables: map[string]string{"GATEWAY_CHAT_FRAME_EVENTS": "2", "GATEWAY_CHAT_FRAME_SEPARATE": "true"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			environment, client := startGatewayEnv(t, e2eEnvOptions{gatewayEnvOverrides: testCase.variables})
			beforeStreaming := gatewaySessionNonce(t, client, environment.clientURL)
			testutil.RequireOpenAIStream(t, testutil.SendStreamingCompletion(t, client, environment.clientURL, "signed streaming response"))
			requireSignedFrameCheckAfter(t, environment, beforeStreaming)
			beforeBuffered := gatewaySessionNonce(t, client, environment.clientURL)
			testutil.RequireOpenAINonStreamingCompletion(t, testutil.SendCompletionRaw(t, client, environment.clientURL, "signed buffered response", testutil.AdminAPIKey))
			requireSignedFrameCheckAfter(t, environment, beforeBuffered)
		})
	}
}

func requireSignedFrameCheckAfter(t *testing.T, environment *e2eEnv, previousNonce uint64) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, line := range gatewayLogLines(t, environment, "chat frames checked") {
			start := strings.IndexByte(line, '{')
			if start < 0 {
				continue
			}
			var observation struct {
				Escrow string `json:"escrow"`
				Nonce  uint64 `json:"nonce"`
				Frames uint64 `json:"chat_frames"`
				Signed uint64 `json:"chat_frames_signed"`
			}
			if json.Unmarshal([]byte(line[start:]), &observation) == nil &&
				observation.Escrow == defaultEscrowID && observation.Nonce > previousNonce &&
				observation.Signed > 0 && observation.Frames >= observation.Signed {
				return true
			}
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "no signed frame check after nonce %d", previousNonce)
	require.Empty(t, gatewayLogLines(t, environment, "chat_frame_signature_invalid"))
}
