package transport

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/types"
)

func TestChallengeReceiptMissingDiffs(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	diffs := []types.Diff{{Nonce: 1, PostStateRoot: root}, {Nonce: 2, PostStateRoot: root}}
	for _, tc := range []struct {
		name        string
		nonce       uint64
		root        []byte
		queryStatus int
		firstStatus int
		empty       bool
		sizes       []int
	}{
		{"one missing", 1, root, 200, 200, false, []int{1}},
		{"caught up", 2, root, 200, 200, false, []int{0}},
		{"unknown nonce", 3, root, 200, 200, false, []int{2}},
		{"wrong root", 1, bytes.Repeat([]byte{2}, 32), 200, 200, false, []int{2}},
		{"missing root", 1, nil, 200, 200, false, []int{2}},
		{"old endpoint", 1, root, 404, 200, false, []int{2}},
		{"query error", 1, root, 500, 200, false, []int{2}},
		{"stale instance", 1, root, 200, 409, false, []int{1, 2}},
		{"empty receipt", 2, root, 200, 200, true, []int{0, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sizes []int
			queries := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					queries++
					require.NotEmpty(t, r.Header.Get(HeaderSignature))
					w.WriteHeader(tc.queryStatus)
					_ = json.NewEncoder(w).Encode(StateResponse{Nonce: tc.nonce, StateRoot: tc.root})
					return
				}
				var req ChallengeReceiptRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				sizes = append(sizes, len(req.Diffs))
				if len(sizes) == 1 {
					w.WriteHeader(tc.firstStatus)
					if tc.empty {
						_ = json.NewEncoder(w).Encode(ChallengeReceiptResponse{})
						return
					}
				}
				_ = json.NewEncoder(w).Encode(ChallengeReceiptResponse{Receipt: []byte("receipt")})
			}))
			defer server.Close()
			client := NewHTTPClient(server.URL, "escrow-1", testutil.MustGenerateKey(t))
			receipt, err := client.ChallengeReceipt(context.Background(), 1, nil, diffs)
			require.NoError(t, err)
			require.NotEmpty(t, receipt)
			require.Equal(t, tc.sizes, sizes)
			require.Equal(t, 1, queries)
		})
	}
}
