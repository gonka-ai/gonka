package poc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"decentralized-api/cosmosclient"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func bodyCapRecorder() *cosmosclient.MockCosmosMessageClient {
	r := &cosmosclient.MockCosmosMessageClient{}
	r.On("GetAccountAddress").Return("validator-address")
	r.On("GetSignerAddress").Return("validator-signer")
	r.On("SignBytes", mock.Anything).Return([]byte("signature"), nil)
	return r
}

// A validatee controls its own /v1/poc/proofs URL. A response larger than
// maxProofResponseBytes is rejected at decode time, so the validator cannot be
// driven to OOM by a multi-GB body served within the request timeout.
func TestFetchAndVerifyProofs_OversizedBodyRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"proofs":[{"leaf_index":0,"nonce_value":0,"vector_bytes":"`)
		chunk := strings.Repeat("A", 1<<20)
		for i := int64(0); i <= maxProofResponseBytes>>20; i++ {
			fmt.Fprint(w, chunk)
		}
		fmt.Fprint(w, `","proof":[]}]}`)
	}))
	defer srv.Close()

	client := &ProofClient{httpClient: srv.Client(), recorder: bodyCapRecorder()}
	_, err := client.FetchAndVerifyProofs(context.Background(), srv.URL, ProofRequest{
		Count: 1, TreeDepth: 12, LeafIndices: []uint32{0}, ParticipantAddress: "victim",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode response")
}

// A well-formed response under the cap still decodes; the cap only bounds the
// read, it does not change how a normal proof set is parsed or validated.
func TestFetchAndVerifyProofs_NormalBodyDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"proofs":[]}`)
	}))
	defer srv.Close()

	client := &ProofClient{httpClient: srv.Client(), recorder: bodyCapRecorder()}
	_, err := client.FetchAndVerifyProofs(context.Background(), srv.URL, ProofRequest{
		Count: 1, TreeDepth: 12, LeafIndices: []uint32{0}, ParticipantAddress: "victim",
	})
	// Decode succeeded; coverage check then rejects the empty set (1 requested, 0 returned).
	require.ErrorIs(t, err, ErrIncompleteCoverage)
}
