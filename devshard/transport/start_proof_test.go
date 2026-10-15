package transport

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/types"
)

func TestPeekStartProof(t *testing.T) {
	diffs, ver, err := PeekStartProof(nil)
	require.NoError(t, err)
	require.Empty(t, diffs)
	require.Empty(t, ver)

	_, _, err = PeekStartProof([]byte(`{`))
	require.Error(t, err)

	user := testutil.MustGenerateKey(t)
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTxVersioned(1, testutil.RuntimeTestVersion)})
	dj, err := DiffToJSON(diff)
	require.NoError(t, err)
	body, err := json.Marshal(ChallengeReceiptRequest{
		InferenceID:     1,
		ProtocolVersion: testutil.RuntimeTestVersion,
		Diffs:           []DiffJSON{dj},
	})
	require.NoError(t, err)
	got, ver, err := PeekStartProof(body)
	require.NoError(t, err)
	require.Equal(t, testutil.RuntimeTestVersion, ver)
	require.Len(t, got, 1)
	require.Equal(t, diff.Nonce, got[0].Nonce)
}
