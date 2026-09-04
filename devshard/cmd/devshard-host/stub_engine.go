package main

import (
	"crypto/sha256"
	"fmt"

	devshardpkg "devshard"
	"devshard/internal/boolvalue"
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
	echoSession, err := e2econfig.StringFromEnv(e2econfig.StubInferenceEchoSessionEnv)
	if err != nil {
		return nil, err
	}
	if echoSession != "" {
		if stubResponseBody != "" {
			return nil, fmt.Errorf("%s and %s are mutually exclusive: the echo replaces the body",
				e2econfig.StubInferenceEchoSessionEnv, e2econfig.StubInferenceResponseBodyEnv)
		}
		if stubEngine.EchoSessionID, err = boolvalue.Parse(echoSession); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e2econfig.StubInferenceEchoSessionEnv, err)
		}
	}
	return stubEngine, nil
}
