package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"devshard/storage"

	"github.com/stretchr/testify/require"
)

func TestAdminStorageProofEndpoints(t *testing.T) {
	proof := func(_ context.Context, operation storage.ProofOperation, nonce string) (storage.StorageProof, error) {
		return storage.StorageProof{Identity: "database-1", Found: operation == storage.ProofWriteChallenge && nonce != ""}, nil
	}
	admin := buildAdminServer(newLifecycleState(), func() bool { return true }, proof, recoveryDone)

	identity := httptest.NewRecorder()
	admin.ServeHTTP(identity, httptest.NewRequest(http.MethodGet, "/storage/identity", nil))
	require.Equal(t, http.StatusOK, identity.Code)
	require.JSONEq(t, `{"identity":"database-1"}`, identity.Body.String())

	challenge := httptest.NewRecorder()
	admin.ServeHTTP(challenge, httptest.NewRequest(http.MethodPost, "/storage/challenge",
		strings.NewReader(`{"operation":"write","nonce":"8aa1c262-ea39-43c2-928c-263e966cc9b4"}`)))
	require.Equal(t, http.StatusOK, challenge.Code)
	require.JSONEq(t, `{"identity":"database-1","found":true}`, challenge.Body.String())

	invalid := httptest.NewRecorder()
	admin.ServeHTTP(invalid, httptest.NewRequest(http.MethodPost, "/storage/challenge",
		strings.NewReader(`{"operation":"delete","nonce":"x"}`)))
	require.Equal(t, http.StatusBadRequest, invalid.Code)
}
