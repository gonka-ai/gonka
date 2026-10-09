package user

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/types"
)

// fakeFetcher implements HostClient + SignatureFetcher. GetSignatures returns
// whatever the test stages, simulating a byzantine host that serves arbitrary
// bytes from its local signature store via GET /sessions/:id/signatures.
type fakeFetcher struct {
	sigs map[uint32][]byte
}

func (f *fakeFetcher) Send(_ context.Context, _ host.HostRequest, _ io.Writer, _ func(*host.HostResponse)) (*host.HostResponse, error) {
	return &host.HostResponse{}, nil
}

func (f *fakeFetcher) GetSignatures(_ context.Context, _ uint64) (map[uint32][]byte, error) {
	return f.sigs, nil
}

// fetchSignature must verify the returned bytes against the canonical
// StateSignatureContent preimage before storing, mirroring processResponse.
// A byzantine host that serves arbitrary bytes from its local store must not
// be able to poison the proxy's signature pool.
func TestFetchSignature_RejectsUnverifiedGarbage(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()

	// Send one inference so the proxy has a post-state-root for the nonce,
	// matching the production precondition for any nonce a session would
	// finalize over.
	params := InferenceParams{
		Model:       "llama",
		Prompt:      testutil.TestPrompt,
		InputLength: 100,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   1000,
	}
	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)

	const hostIdx = 0
	const slot = uint32(0)
	expectedAddr := session.group[hostIdx].ValidatorAddress
	require.Equal(t, expectedAddr, session.sm.SlotAddress(slot))

	// Inference 1 routes to host 1 (1 % 3), so host 0 has not contributed a
	// signature yet — perfect target for the poison attempt.
	garbage := []byte("NOT-A-REAL-SIGNATURE-JUST-65-BYTES-OF-NOTHING-AT-ALL-AAAAAAAAAAAA")
	badClient := &fakeFetcher{
		sigs: map[uint32][]byte{slot: garbage},
	}

	ok := session.fetchSignature(ctx, hostIdx, 1, badClient)
	require.False(t, ok, "fetchSignature must reject bytes that don't recover to expectedAddr")

	stored := session.Signatures()[1]
	if stored != nil {
		require.Nil(t, stored[slot], "garbage bytes must not be stored under slot 0")
	}
}

// Snapshot-only recovery leaves s.diffs empty. fetchSignature must still
// verify GET /signatures using the live SM root (same fallback as processResponse).
func TestFetchSignature_LiveStateRootWhenDiffsEmpty(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)

	// Sign over the ORIGINAL per-nonce post-state-root that a real host would
	// have signed at diff time — captured before we wipe diffs. If the live
	// ComputeStateRoot fallback verifies this, it proves the recomputed root
	// equals the original PostStateRoot (the actual invariant the fix relies on).
	originalRoot := append([]byte(nil), session.Diffs()[0].PostStateRoot...)
	require.NotEmpty(t, originalRoot)

	const hostIdx = 1 // nonce 1 % 3
	const slot = uint32(1)
	sigData, err := proto.Marshal(&types.StateSignatureContent{
		StateRoot: originalRoot, EscrowId: session.escrowID, Nonce: 1,
	})
	require.NoError(t, err)
	stateSig, err := hosts[hostIdx].Sign(sigData)
	require.NoError(t, err)

	// Simulate snapshot-only recovery: SM at final state, diffs/signatures gone.
	// s.nonce stays 1, matching the frozen SM state.
	session.mu.Lock()
	session.diffs = nil
	session.signatures = make(map[uint64]map[uint32][]byte)
	session.mu.Unlock()

	ok := session.fetchSignature(ctx, hostIdx, 1, &fakeFetcher{
		sigs: map[uint32][]byte{slot: stateSig},
	})
	require.True(t, ok, "fetchSignature must verify original PostStateRoot via live ComputeStateRoot when diffs are empty")
	require.Equal(t, stateSig, session.Signatures()[1][slot])
}

// The live-root fallback must NOT fire for a nonce other than the current
// (frozen) session nonce: we have no way to reconstruct that nonce's root.
func TestFetchSignature_NoFallbackForNonCurrentNonce(t *testing.T) {
	session, hosts, _ := setupSession(t, 3, 100000, 10)
	ctx := context.Background()

	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	_, err := session.SendInference(ctx, params)
	require.NoError(t, err)

	// A well-formed signature for nonce 1 (current), but requested at nonce 1
	// after wiping diffs while pretending the session advanced to nonce 5.
	root := append([]byte(nil), session.Diffs()[0].PostStateRoot...)
	sigData, err := proto.Marshal(&types.StateSignatureContent{
		StateRoot: root, EscrowId: session.escrowID, Nonce: 1,
	})
	require.NoError(t, err)
	stateSig, err := hosts[1].Sign(sigData)
	require.NoError(t, err)

	session.mu.Lock()
	session.diffs = nil
	session.signatures = make(map[uint64]map[uint32][]byte)
	session.nonce = 5 // requested nonce (1) != current nonce (5)
	session.mu.Unlock()

	ok := session.fetchSignature(ctx, 1, 1, &fakeFetcher{
		sigs: map[uint32][]byte{uint32(1): stateSig},
	})
	require.False(t, ok, "must not fall back to live root for a non-current nonce")
	require.Empty(t, session.Signatures()[1])
}

// processResponse already verifies — keep a parallel assertion so the symmetry
// stays under test. Same input rejected by both ingestion paths.
func TestProcessResponse_RejectsUnverifiedGarbage(t *testing.T) {
	session, _, _ := setupSession(t, 3, 100000, 10)

	const hostIdx = 0
	const nonce = uint64(42)
	garbage := []byte("NOT-A-REAL-SIGNATURE-JUST-65-BYTES-OF-NOTHING-AT-ALL-AAAAAAAAAAAA")

	resp := &host.HostResponse{
		Nonce:    nonce,
		StateSig: garbage,
	}

	err := session.ProcessResponse(hostIdx, resp, nonce)
	require.Error(t, err, "processResponse must reject garbage signatures")
}
