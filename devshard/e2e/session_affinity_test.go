package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"devshard/e2e/testutil"
	"devshard/internal/e2econfig"
	"devshard/stub"

	"github.com/stretchr/testify/require"
)

// sessionTokenHexLength is the wire session id's fixed width: a hex-encoded SHA-256 digest.
var sessionTokenHexLength = hex.EncodedLen(sha256.Size)

// sessionPromptCounter keeps every prompt in this file distinct; see engineSessionToken.
var sessionPromptCounter atomic.Int64

// startSessionEchoEnv starts the stand with every host echoing the session id its engine received,
// so a test reads what crossed the wire instead of inferring it from routing.
func startSessionEchoEnv(t *testing.T, affinityEnabled bool) (*e2eEnv, *http.Client) {
	t.Helper()
	return startNonStreamingEnvWithOptions(t, e2eEnvOptions{
		hostEnv:                 map[string]string{e2econfig.StubInferenceEchoSessionEnv: "true"},
		devshardctlEnvOverrides: map[string]string{"DEVSHARD_AFFINITY_ENABLED": strconv.FormatBool(affinityEnabled)},
	})
}

// engineSessionToken sends one completion under cacheKey and returns the session id the serving
// engine was given, empty when none reached it. The prompt is unique per call because the gateway
// response cache is keyed on the body, and a cache hit would answer without reaching a host.
func engineSessionToken(t *testing.T, env *e2eEnv, client *http.Client, cacheKey string) string {
	t.Helper()
	content := t.Name() + "/" + strconv.Itoa(int(sessionPromptCounter.Add(1)))
	resp := testutil.SendCompletionWithCacheKey(t, client, env.clientURL, content, cacheKey, testutil.AdminAPIKey)
	echoed := testutil.CompletionContent(t, resp)
	require.True(t, strings.HasPrefix(echoed, stub.SessionEchoPrefix),
		"stub must be running in session-echo mode, got %q", echoed)
	return strings.TrimPrefix(echoed, stub.SessionEchoPrefix)
}

// Test flow:
//  1. Start the stand with gateway affinity on and every host echoing the session id it receives.
//  2. Send one completion tagged with a prompt_cache_key.
//  3. Assert a session id reached the serving engine.
//  4. Assert it is the fixed-width derived token and not the client's own string.
func TestE2E_SessionAffinityDeliversADerivedTokenToTheEngine(t *testing.T) {
	env, client := startSessionEchoEnv(t, true)

	token := engineSessionToken(t, env, client, "conversation-alpha")

	require.NotEmpty(t, token, "an enabled gateway must send a session id to the host")
	require.Len(t, token, sessionTokenHexLength, "the wire session id is a hex-encoded digest")
	require.NotContains(t, token, "conversation-alpha",
		"the client's own cache key must never leave the gateway")
}

// Test flow:
//  1. Start the stand without setting the affinity switch at all, the shipped default.
//  2. Send one completion tagged with a prompt_cache_key.
//  3. Assert no session id reached the serving engine.
func TestE2E_SessionAffinityDeliversNoTokenWhenDisabled(t *testing.T) {
	env, client := startNonStreamingEnvWithOptions(t, e2eEnvOptions{
		hostEnv: map[string]string{e2econfig.StubInferenceEchoSessionEnv: "true"},
	})

	token := engineSessionToken(t, env, client, "conversation-alpha")

	require.Empty(t, token, "with the gateway switch off no session id may be derived or sent")
}

// Test flow:
//  1. Start the stand with gateway affinity on.
//  2. Send two completions carrying the same prompt_cache_key and different prompts.
//  3. Assert both turns reached the engine under one session id.
func TestE2E_SessionAffinityKeepsOneTokenAcrossAConversation(t *testing.T) {
	env, client := startSessionEchoEnv(t, true)

	firstTurn := engineSessionToken(t, env, client, "conversation-alpha")
	secondTurn := engineSessionToken(t, env, client, "conversation-alpha")

	require.NotEmpty(t, firstTurn)
	require.Equal(t, firstTurn, secondTurn,
		"cache reuse depends on both turns of one conversation sharing a session id")
}

// Test flow:
//  1. Start the stand with gateway affinity on.
//  2. Send two completions carrying different prompt_cache_key values.
//  3. Assert the engine saw two different session ids.
func TestE2E_SessionAffinityGivesDifferentConversationsDifferentTokens(t *testing.T) {
	env, client := startSessionEchoEnv(t, true)

	first := engineSessionToken(t, env, client, "conversation-alpha")
	second := engineSessionToken(t, env, client, "conversation-beta")

	require.NotEmpty(t, first)
	require.NotEqual(t, first, second,
		"two conversations must not share a cache namespace")
}
