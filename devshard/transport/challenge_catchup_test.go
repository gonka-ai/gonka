package transport

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

func TestChallengeReceiptTrimsVerifiedTail(t *testing.T) {
	root2 := bytes.Repeat([]byte{2}, 32)
	root3 := bytes.Repeat([]byte{3}, 32)
	for _, tc := range []struct {
		name            string
		nonce           uint64
		root            []byte
		queryStatus     int
		challengeStatus int
		trimmed         bool
		remaining       int
	}{
		{"within tail", 2, root2, 200, 200, true, 1},
		{"at target", 3, root3, 200, 200, true, 0},
		{"before snapshot", 0, root2, 200, 200, false, 2},
		{"at snapshot", 1, root2, 200, 200, false, 2},
		{"beyond target", 4, root3, 200, 200, false, 2},
		{"root mismatch", 2, root3, 200, 200, false, 2},
		{"query unavailable", 2, root2, 500, 200, false, 2},
		{"query unsupported", 2, root2, 404, 200, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &types.RefusalPackage{EscrowID: "escrow-1", Version: "v4.2", N: 1, T: 3, Snapshot: []byte("opaque snapshot"), Diffs: []types.Diff{{Nonce: 2, PostStateRoot: root2}, {Nonce: 3, PostStateRoot: root3}}}
			var queries, challenges int
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					queries++
					require.NotEmpty(t, r.Header.Get(HeaderSignature))
					w.WriteHeader(tc.queryStatus)
					_ = json.NewEncoder(w).Encode(StateResponse{Nonce: tc.nonce, StateRoot: tc.root})
					return
				}
				challenges++
				var req ChallengeReceiptRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				require.NotNil(t, req.Refusal)
				require.Len(t, req.Refusal.Diffs, tc.remaining)
				if tc.trimmed {
					require.Empty(t, req.Refusal.Snapshot)
					require.Empty(t, req.Refusal.Signatures)
					require.Equal(t, tc.nonce, req.Refusal.N)
					require.Equal(t, tc.root, req.Refusal.BaseRoot)
				} else {
					require.Equal(t, p.Snapshot, req.Refusal.Snapshot)
					require.Empty(t, req.Refusal.BaseRoot)
				}
				w.WriteHeader(tc.challengeStatus)
				_ = json.NewEncoder(w).Encode(ChallengeReceiptResponse{Receipt: []byte("receipt")})
			}))
			defer ts.Close()
			c := NewHTTPClient(ts.URL, "escrow-1", testutil.MustGenerateKey(t))
			receipt, err := c.ChallengeReceipt(context.Background(), 1, nil, p)
			require.NoError(t, err)
			require.Equal(t, []byte("receipt"), receipt)
			require.Equal(t, 1, queries)
			require.Equal(t, 1, challenges)
			require.Equal(t, uint64(1), p.N)
			require.NotEmpty(t, p.Snapshot)
			require.Len(t, p.Diffs, 2)
		})
	}
}

func TestChallengeReceiptShortenedStateConflict(t *testing.T) {
	env := setupServerEnv(t)
	p := &RefusalPackageJSON{EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion, N: 2, T: 2, BaseRoot: make([]byte, 32)}
	body, err := json.Marshal(ChallengeReceiptRequest{InferenceID: 1, Refusal: p})
	require.NoError(t, err)
	resp := env.doPost(t, testRoutePrefix+"/sessions/escrow-1/challenge-receipt", body)
	require.Equal(t, http.StatusConflict, resp.Code, resp.Body.String())
}

func TestChallengeReceiptShortenedHTTP(t *testing.T) {
	env := setupServerEnv(t)
	seq, err := state.NewStateMachine("escrow-1", env.config, env.group, 100000, env.userSigner.Address(), signing.NewSecp256k1Verifier(), env.store)
	require.NoError(t, err)
	var diffs []types.Diff
	for n := uint64(1); n <= 3; n++ {
		var txs []*types.DevshardTx
		if n == 1 {
			txs = []*types.DevshardTx{testutil.StartTx(1)}
		}
		root, err := seq.ApplyLocal(n, txs)
		require.NoError(t, err)
		diffs = append(diffs, testutil.SignDiffWithRoot(t, env.userSigner, "escrow-1", n, txs, root))
	}
	env.server.host.ApplyCatchUpDiffs(diffs[:2])
	require.Equal(t, uint64(2), env.server.host.LatestNonce())
	ts := httptest.NewServer(env.echo)
	defer ts.Close()
	cfg := DefaultClientConfig()
	cfg.RoutePrefix = testRoutePrefix
	client := NewHTTPClient(ts.URL, "escrow-1", env.hostSigner, cfg)
	p := &types.RefusalPackage{EscrowID: "escrow-1", Version: testutil.RuntimeTestVersion, N: 1, T: 3, Snapshot: []byte("must not decode"), Diffs: diffs[1:]}
	payload := &host.InferencePayload{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
	receipt, err := client.ChallengeReceipt(context.Background(), 1, payload, p)
	require.NoError(t, err)
	require.NotEmpty(t, receipt)
	nonce, root, err := env.server.host.StateHead()
	require.NoError(t, err)
	require.Equal(t, uint64(3), nonce)
	require.Equal(t, diffs[2].PostStateRoot, root)
}

func TestChallengeReceiptErrorRetriesFullPackage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		firstStatus int
		status      int
		receipt     []byte
		accept      bool
	}{
		{"receipt", http.StatusConflict, http.StatusOK, []byte("receipt"), false},
		{"no receipt", http.StatusConflict, http.StatusOK, nil, true},
		{"repeated conflict", http.StatusConflict, http.StatusConflict, nil, true},
		{"executor error", http.StatusConflict, http.StatusInternalServerError, nil, true},
		{"shortened server error", http.StatusInternalServerError, http.StatusOK, []byte("receipt"), false},
		{"bad shortened response", http.StatusOK, http.StatusOK, nil, true},
		{"connection lost", 0, http.StatusOK, []byte("receipt"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := bytes.Repeat([]byte{1}, 32)
			proof := &types.RefusalPackage{EscrowID: "bench", Version: "v4.2", N: 1, T: 2, Snapshot: []byte("already verified by caller"), Diffs: []types.Diff{{Nonce: 2, PostStateRoot: root}}, Signatures: map[uint64]map[uint32][]byte{2: {0: []byte("signature")}}}
			full, err := refusalToJSON(proof)
			require.NoError(t, err)
			var queries, challenges int
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					queries++
					_ = json.NewEncoder(w).Encode(StateResponse{Nonce: 2, StateRoot: root})
					return
				}
				challenges++
				var req ChallengeReceiptRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				require.NotNil(t, req.Refusal)
				require.Equal(t, uint64(1), req.InferenceID)
				require.Equal(t, testutil.TestPrompt, req.Payload.Prompt)
				if challenges%2 == 1 {
					require.Empty(t, req.Refusal.Snapshot)
					require.Empty(t, req.Refusal.Diffs)
					require.Equal(t, root, req.Refusal.BaseRoot)
					if tc.firstStatus == 0 {
						conn, _, err := w.(http.Hijacker).Hijack()
						require.NoError(t, err)
						_ = conn.Close()
						return
					}
					w.WriteHeader(tc.firstStatus)
					if tc.firstStatus == http.StatusOK {
						_, _ = w.Write([]byte(`{"receipt":"cmVjZWlwdA==","broken":`))
					}
					return
				}
				require.Equal(t, full, req.Refusal)
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(ChallengeReceiptResponse{Receipt: tc.receipt})
			}))
			defer ts.Close()
			client := NewHTTPClient(ts.URL, "bench", testutil.MustGenerateKey(t))
			hash := testutil.TestPromptHash
			st := types.EscrowState{EscrowID: "bench", Config: types.SessionConfig{RefusalTimeout: 60}, Inferences: map[uint64]*types.InferenceRecord{1: {Status: types.StatusPending, ExecutorSlot: 1, PromptHash: hash[:], Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}}}
			payload := &host.InferencePayload{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
			for i := 1; i <= 3; i++ {
				accept, err := host.VerifyRefusedTimeout(context.Background(), st, 1, payload, proof, nil, client, st.Config, 2000)
				require.NoError(t, err)
				require.Equal(t, tc.accept, accept)
				require.Equal(t, i, queries)
				require.Equal(t, i*2, challenges)
			}
		})
	}
}
