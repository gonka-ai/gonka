package mlnode

import (
	"bytes"
	"net/url"

	"decentralized-api/internal/callbackauth"
)

var testCallbackSigner = mustTestCallbackSigner()

func mustTestCallbackSigner() *callbackauth.Signer {
	signer, err := callbackauth.NewSigner(bytes.Repeat([]byte{7}, callbackauth.SecretSize))
	if err != nil {
		panic(err)
	}
	return signer
}

const testValidatedPubKey = "02b463f7f42e5f4f1d2d0bb1c4b9f8d2c3b1a09c72fbc5d0b8d4c53b37f6f2a540"

func callbackModelSegment(modelID string) string {
	return url.PathEscape(url.PathEscape(modelID))
}

func generatedCallbackPath(modelID string, stage int64, perModel bool, nodeID int) string {
	path := "/v2/poc-batches/" + callbackModelSegment(modelID) +
		callbackauth.GenerationPath(testCallbackSigner.GenerationToken(stage, modelID, perModel, nodeID))
	if perModel {
		path += "/m"
	}
	return path + "/generated"
}

func validatedCallbackPath(modelID string, stage int64, pubKey string, sampled int64) string {
	return validatedCallbackPathWithMin(modelID, stage, pubKey, sampled, 0)
}

func validatedCallbackPathWithMin(modelID string, stage int64, pubKey string, sampled int64, minCount uint32) string {
	return "/v2/poc-batches/" + callbackModelSegment(modelID) +
		callbackauth.ValidationPath(sampled, minCount, testCallbackSigner.ValidationToken(stage, modelID, pubKey, sampled, minCount)) +
		"/validated"
}
