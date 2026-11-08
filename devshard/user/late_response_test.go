package user

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/storage"
	"devshard/types"
)

// Hold a genuine response while ordinary requests advance all host cursors.
// The only difference between the controls is the gateway diff store.
func TestSession_LatePreparedResponseRootLifetime(t *testing.T) {
	for _, stored := range []bool{false, true} {
		name := "without_gateway_store"
		if stored {
			name = "with_gateway_store"
		}
		t.Run(name, func(t *testing.T) {
			var session *Session
			if stored {
				session = setupStoredSession(t, storage.NewMemory())
			} else {
				session, _, _ = setupSession(t, 3, 100000, 10)
			}
			t.Cleanup(func() { _ = session.Close() })
			ctx := context.Background()
			params := InferenceParams{
				Model: "llama", Prompt: testutil.TestPrompt,
				InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
			}
			slow, err := session.PrepareInference(ctx, params)
			require.NoError(t, err)
			response, err := session.SendOnly(ctx, slow, nil, nil)
			require.NoError(t, err)
			require.Equal(t, uint64(1), response.Nonce)
			require.Len(t, response.StateHash, 32)
			require.NotEmpty(t, response.StateSig)
			require.NoError(t, session.verifyStateSignature(response.Nonce, response.StateHash,
				response.StateSig, session.group[slow.hostIdx].ValidatorAddress))
			t.Logf("held valid signed response: nonce=%d gateway=%d", response.Nonce, session.Nonce())
			for range 3 {
				_, err = session.SendInference(ctx, params)
				require.NoError(t, err)
			}
			session.mu.Lock()
			first := session.firstRetainedNonceLocked()
			current := session.nonce
			deliveryActive := session.validationProgress != nil
			cursors := make(map[int]uint64, len(session.hostSyncNonce))
			for idx, nonce := range session.hostSyncNonce {
				cursors[idx] = nonce
			}
			root, found, readErr := session.postStateRootForNonce(response.Nonce)
			session.mu.Unlock()
			require.NoError(t, readErr)
			require.False(t, deliveryActive, "the new validation delivery loop was never started")
			require.Greater(t, first, response.Nonce)
			t.Logf("before releasing held response: gateway=%d retained_from=%d host_cursors=%v old_root_found=%t", current, first, cursors, found)
			if stored {
				require.True(t, found)
				require.Equal(t, response.StateHash, root)
			} else {
				require.False(t, found)
			}
			require.NoError(t, session.ProcessPreparedResponse(slow, response),
				"an authentic delayed response must remain verifiable after another request advances the host cursor")
		})
	}
}

func TestSession_LatePreparedResponseRejectsInvalidEvidence(t *testing.T) {
	for _, name := range []string{"host_root", "sent_root", "sent_signature", "sent_nonce", "foreign_diff"} {
		t.Run(name, func(t *testing.T) {
			session, _, _ := setupSession(t, 3, 100000, 10)
			t.Cleanup(func() { _ = session.Close() })
			ctx := context.Background()
			params := InferenceParams{
				Model: "llama", Prompt: testutil.TestPrompt,
				InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
			}
			slow, err := session.PrepareInference(ctx, params)
			require.NoError(t, err)
			response, err := session.SendOnly(ctx, slow, nil, nil)
			require.NoError(t, err)
			for range 3 {
				_, err = session.SendInference(ctx, params)
				require.NoError(t, err)
			}
			prepared := *slow
			answer := *response
			switch name {
			case "host_root":
				answer.StateHash = append([]byte(nil), response.StateHash...)
				answer.StateHash[0] ^= 0xff
			case "sent_root":
				prepared.diff.PostStateRoot = append([]byte(nil), slow.diff.PostStateRoot...)
				prepared.diff.PostStateRoot[0] ^= 0xff
			case "sent_signature":
				prepared.diff.UserSig = append([]byte(nil), slow.diff.UserSig...)
				prepared.diff.UserSig[0] ^= 0xff
			case "sent_nonce":
				prepared.diff.Nonce++
			case "foreign_diff":
				other, _, _ := setupSession(t, 3, 100000, 10)
				t.Cleanup(func() { _ = other.Close() })
				foreign, err := other.PrepareInference(ctx, params)
				require.NoError(t, err)
				prepared.diff = foreign.diff
			}
			session.mu.Lock()
			cursor := session.hostSyncNonce[slow.hostIdx]
			first := session.firstRetainedNonceLocked()
			session.mu.Unlock()
			require.Greater(t, first, response.Nonce)
			require.ErrorIs(t, session.ProcessPreparedResponse(&prepared, &answer), types.ErrStateHashMismatch)
			session.mu.Lock()
			after := session.hostSyncNonce[slow.hostIdx]
			session.mu.Unlock()
			require.Equal(t, cursor, after, "a rejected response cannot advance the host cursor")
		})
	}
}

func TestSession_LatePreparedResponsePreservesStoreFailure(t *testing.T) {
	store := &failRootReadStore{Storage: storage.NewMemory()}
	session := setupStoredSession(t, store)
	t.Cleanup(func() { _ = session.Close() })
	ctx := context.Background()
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	prepared, err := session.PrepareInference(ctx, params)
	require.NoError(t, err)
	response, err := session.SendOnly(ctx, prepared, nil, nil)
	require.NoError(t, err)
	for range 3 {
		_, err = session.SendInference(ctx, params)
		require.NoError(t, err)
	}
	store.nonce = response.Nonce
	err = session.ProcessPreparedResponse(prepared, response)
	require.ErrorIs(t, err, ErrLocalRootUnavailable)
	require.ErrorIs(t, err, errRootRead)
	require.NotErrorIs(t, err, types.ErrStateHashMismatch)
}
