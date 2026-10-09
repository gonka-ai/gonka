package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/types"
)

type refusalDiffStore struct {
	storage.Storage
	diffs  []types.Diff
	ranges [][2]uint64
	fail   bool
}

func (s *refusalDiffStore) GetDiffs(_ string, from, to uint64) ([]types.DiffRecord, error) {
	s.ranges = append(s.ranges, [2]uint64{from, to})
	if s.fail {
		return nil, errors.New("storage unavailable")
	}
	var records []types.DiffRecord
	for _, d := range s.diffs {
		if d.Nonce >= from && d.Nonce <= to {
			records = append(records, types.DiffRecord{Diff: d, StateHash: d.PostStateRoot})
		}
	}
	return records, nil
}

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
		accept      bool
		queries     int
		sizes       []int
		ranges      [][2]uint64
	}{
		{"one missing", 1, root, 200, 200, false, false, 1, []int{1}, [][2]uint64{{1, 1}, {2, 2}}},
		{"caught up", 2, root, 200, 200, false, false, 1, []int{0}, [][2]uint64{{2, 2}}},
		{"unknown nonce", 3, root, 200, 200, false, false, 1, []int{2}, [][2]uint64{{1, 2}}},
		{"wrong root", 1, bytes.Repeat([]byte{2}, 32), 200, 200, false, false, 1, []int{2}, [][2]uint64{{1, 1}, {1, 2}}},
		{"missing root", 1, nil, 200, 200, false, false, 1, []int{2}, [][2]uint64{{1, 2}}},
		{"old endpoint", 1, root, 404, 200, false, false, 1, []int{2}, [][2]uint64{{1, 2}}},
		{"query error", 1, root, 500, 200, false, false, 1, []int{2}, [][2]uint64{{1, 2}}},
		{"stale instance", 1, root, 200, 409, false, true, 2, []int{1}, [][2]uint64{{1, 1}, {2, 2}}},
		{"server error", 1, root, 200, 500, false, true, 2, []int{1}, [][2]uint64{{1, 1}, {2, 2}}},
		{"empty receipt", 2, root, 200, 200, true, true, 2, []int{0}, [][2]uint64{{2, 2}}},
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
			env := setupServerEnv(t)
			store := &refusalDiffStore{diffs: diffs}
			env.server.store = store
			st, payload := refusalTestState()
			accept, err := env.server.verifyRefusedTimeout(context.Background(), st, 1, payload, nil, client, 2000)
			require.NoError(t, err)
			require.Equal(t, tc.accept, accept)
			require.Equal(t, tc.sizes, sizes)
			require.Equal(t, tc.queries, queries)
			require.Equal(t, tc.ranges, store.ranges)
		})
	}
}

func refusalTestState() (types.EscrowState, *host.InferencePayload) {
	hash := testutil.TestPromptHash
	return types.EscrowState{LatestNonce: 2, Config: types.SessionConfig{RefusalTimeout: 60}, Inferences: map[uint64]*types.InferenceRecord{1: {
		Status: types.StatusPending, StartedAt: 1000, PromptHash: hash[:], Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens,
	}}}, &host.InferencePayload{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
}

func TestRefusalRetryDeadline(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	for _, cancelRequest := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRequest), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mu sync.Mutex
			var sizes []int
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_ = json.NewEncoder(w).Encode(StateResponse{Nonce: 1, StateRoot: root})
					return
				}
				var req ChallengeReceiptRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				sizes = append(sizes, len(req.Diffs))
				mu.Unlock()
				if len(req.Diffs) == 1 {
					if cancelRequest {
						cancel()
					}
					<-r.Context().Done()
					return
				}
				_ = json.NewEncoder(w).Encode(ChallengeReceiptResponse{Receipt: []byte("receipt")})
			}))
			defer remote.Close()
			cfg := DefaultClientConfig()
			cfg.VerifyTimeout = time.Second
			client := NewHTTPClient(remote.URL, "escrow-1", testutil.MustGenerateKey(t), cfg)
			env := setupServerEnv(t)
			env.server.store = &refusalDiffStore{diffs: []types.Diff{{Nonce: 1, PostStateRoot: root}, {Nonce: 2}}}
			st, payload := refusalTestState()
			accept, err := env.server.verifyRefusedTimeout(ctx, st, 1, payload, nil, client, 2000)
			mu.Lock()
			defer mu.Unlock()
			if cancelRequest {
				require.ErrorIs(t, err, context.Canceled)
				require.False(t, accept)
				require.Equal(t, []int{1}, sizes)
			} else {
				require.NoError(t, err)
				require.True(t, accept, "a challenge that never answers must vote without the full history")
				require.Equal(t, []int{1}, sizes)
			}
		})
	}
}

func TestRefusalRecheckRetriesAdvancedNonce(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	for _, match := range []bool{true, false} {
		name := "root mismatch"
		if match {
			name = "matching root"
		}
		t.Run(name, func(t *testing.T) {
			var gets int
			var sizes []int
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					gets++
					nonce := uint64(1)
					stateRoot := root
					if gets > 1 {
						nonce = 2
						if !match {
							stateRoot = bytes.Repeat([]byte{9}, 32)
						}
					}
					_ = json.NewEncoder(w).Encode(StateResponse{Nonce: nonce, StateRoot: stateRoot})
					return
				}
				var req ChallengeReceiptRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				sizes = append(sizes, len(req.Diffs))
				if len(sizes) == 1 {
					w.WriteHeader(http.StatusConflict)
					return
				}
				_ = json.NewEncoder(w).Encode(ChallengeReceiptResponse{Receipt: []byte("receipt")})
			}))
			defer remote.Close()
			env := setupServerEnv(t)
			store := &refusalDiffStore{diffs: []types.Diff{
				{Nonce: 1, PostStateRoot: root},
				{Nonce: 2, PostStateRoot: root},
				{Nonce: 3, PostStateRoot: root},
			}}
			env.server.store = store
			st, payload := refusalTestState()
			st.LatestNonce = 3
			client := NewHTTPClient(remote.URL, "escrow-1", env.hostSigner)
			accept, err := env.server.verifyRefusedTimeout(context.Background(), st, 1, payload, nil, client, 2000)
			require.NoError(t, err)
			require.Equal(t, 2, gets)
			if match {
				require.False(t, accept)
				require.Equal(t, []int{2, 1}, sizes)
				require.Equal(t, [][2]uint64{{1, 1}, {2, 3}, {2, 2}, {3, 3}}, store.ranges)
			} else {
				require.True(t, accept)
				require.Equal(t, []int{2}, sizes)
				require.Equal(t, [][2]uint64{{1, 1}, {2, 3}, {2, 2}}, store.ranges)
			}
		})
	}
}

func TestRefusalUnreachableSkipsFullDiffs(t *testing.T) {
	env := setupServerEnv(t)
	store := &refusalDiffStore{diffs: []types.Diff{{Nonce: 1}, {Nonce: 2}}}
	env.server.store = store
	remote := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := NewHTTPClient(remote.URL, "escrow-1", env.hostSigner)
	remote.Close()
	st, payload := refusalTestState()
	accept, err := env.server.verifyRefusedTimeout(context.Background(), st, 1, payload, nil, client, 2000)
	require.NoError(t, err)
	require.True(t, accept)
	require.Empty(t, store.ranges)
}

func TestRefusalStorageFailure(t *testing.T) {
	root := bytes.Repeat([]byte{1}, 32)
	for _, tc := range []struct {
		name  string
		nonce uint64
		fail  bool
		diffs []types.Diff
	}{
		{"anchor read", 1, true, nil},
		{"full read", 0, true, nil},
		{"missing tail", 1, false, []types.Diff{{Nonce: 1, PostStateRoot: root}}},
		{"missing history", 0, false, []types.Diff{{Nonce: 2}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("unexpected challenge")
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(StateResponse{Nonce: tc.nonce, StateRoot: root})
			}))
			defer remote.Close()
			env := setupServerEnv(t)
			env.server.store = &refusalDiffStore{diffs: tc.diffs, fail: tc.fail}
			client := NewHTTPClient(remote.URL, "escrow-1", env.hostSigner)
			st, payload := refusalTestState()
			accept, err := env.server.verifyRefusedTimeout(context.Background(), st, 1, payload, nil, client, 2000)
			require.Error(t, err)
			require.False(t, accept)
		})
	}
}

func TestRefusalFullChallengeOutcomes(t *testing.T) {
	for _, outcome := range []string{"empty", "error", "timeout", "request deadline"} {
		t.Run(outcome, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.WriteHeader(404)
					return
				}
				if outcome == "timeout" || outcome == "request deadline" {
					_, _ = io.Copy(io.Discard, r.Body)
					<-r.Context().Done()
					return
				}
				if outcome == "error" {
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(ChallengeReceiptResponse{})
			}))
			defer remote.Close()
			env := setupServerEnv(t)
			env.server.store = &refusalDiffStore{diffs: []types.Diff{{Nonce: 1}, {Nonce: 2}}}
			cfg := DefaultClientConfig()
			cfg.VerifyTimeout = 300 * time.Millisecond
			client := NewHTTPClient(remote.URL, "escrow-1", env.hostSigner, cfg)
			st, payload := refusalTestState()
			ctx := context.Background()
			if outcome == "request deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
				defer cancel()
			}
			accept, err := env.server.verifyRefusedTimeout(ctx, st, 1, payload, nil, client, 2000)
			if outcome == "request deadline" {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.False(t, accept)
			} else {
				require.NoError(t, err)
				require.True(t, accept)
			}
		})
	}
}

func TestRefusalCanceledWithoutPeer(t *testing.T) {
	env := setupServerEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	st, payload := refusalTestState()
	accept, err := env.server.verifyRefusedTimeout(ctx, st, 1, payload, nil, nil, 2000)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, accept)
}
