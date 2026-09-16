package main

import (
	"crypto/sha256"

	devshardpkg "devshard"
	"devshard/internal/e2econfig"
	"devshard/stub"
)

func stubInferenceEngineFromEnv() (devshardpkg.InferenceEngine, error) {
	stubEngine := stub.NewInferenceEngine()
	stubResponseBody, err := e2econfig.StringFromEnv(e2econfig.StubInferenceResponseBodyEnv)
	if err != nil {
		return nil, err
	}
	if stubResponseBody != "" {
		body := []byte(stubResponseBody)
		responseHash := sha256.Sum256(body)
		stubEngine.ResponseBody = body
		stubEngine.ResponseHash = responseHash[:]
	}
	return stubEngine, nil
}
