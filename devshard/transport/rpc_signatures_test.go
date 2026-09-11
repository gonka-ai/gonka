package transport

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestServeGetSignatures_HTTP(t *testing.T) {
	env := setupServerEnv(t)
	require.NoError(t, env.store.AppendDiff("escrow-1", types.DiffRecord{
		Diff: types.Diff{Nonce: 3},
	}))
	want := []byte{0xde, 0xad}
	require.NoError(t, env.store.AddSignature("escrow-1", 3, 0, want))

	got, err := env.server.ServeGetSignatures(3)
	require.NoError(t, err)
	require.Equal(t, want, got[0])

	rec := env.doGet(t, testRoutePrefix+"/sessions/escrow-1/signatures?nonce=3")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"0"`)
}
