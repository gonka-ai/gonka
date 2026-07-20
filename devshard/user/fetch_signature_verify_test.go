package user

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
)

// fakeFetcher implements HostClient + SignatureFetcher. GetSignatures returns
// whatever the test stages, simulating a byzantine host that serves arbitrary
// bytes from its local signature store via GET /sessions/:id/signatures.
type fakeFetcher struct {
	sigs map[uint32][]byte
}

func (f *fakeFetcher) Send(_ context.Context, _ host.HostRequest, _ io.Writer, _ func()) (*host.HostResponse, error) {
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
		MaxTokens:   50,
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
