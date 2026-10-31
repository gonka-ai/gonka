package main

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/e2econfig"
	"devshard/stub"
)

// Test flow:
//  1. Set the stub answer to a body carrying a bareword -Infinity that encoding/json refuses.
//  2. Build the stub engine from the environment.
//  3. Assert it starts and serves that body, hashed as written.
func TestStubInferenceEngineFromEnv_KeepsABodyGoCannotParse(t *testing.T) {
	const body = `{"choices":[{"message":{"content":"stub"},"logprobs":{"content":[{"logprob":-Infinity,"top_logprobs":[]}]}}]}`
	t.Setenv("DEVSHARD_E2E", "1")
	t.Setenv(e2econfig.StubInferenceResponseBodyEnv, body)

	engine, err := stubInferenceEngineFromEnv()
	require.NoError(t, err)

	stubEngine := engine.(*stub.InferenceEngine)
	sum := sha256.Sum256([]byte(body))
	require.Equal(t, []byte(body), stubEngine.ResponseBody)
	require.Equal(t, sum[:], stubEngine.ServedHash)
}
