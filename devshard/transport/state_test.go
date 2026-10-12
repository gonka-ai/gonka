package transport

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

func TestGetStateAuth(t *testing.T) {
	env := setupServerEnv(t)
	path := testRoutePrefix + "/sessions/escrow-1/state"
	require.Equal(t, http.StatusUnauthorized, env.doGet(t, path).Code)
	for _, tc := range []struct {
		name   string
		signer signing.Signer
		escrow string
		age    int64
		status int
	}{
		{"owner", env.userSigner, "escrow-1", 0, http.StatusOK},
		{"host", env.hostSigner, "escrow-1", 0, http.StatusOK},
		{"outsider", testutil.MustGenerateKey(t), "escrow-1", 0, http.StatusForbidden},
		{"expired", env.userSigner, "escrow-1", 60, http.StatusUnauthorized},
		{"wrong escrow", env.userSigner, "other", 0, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := time.Now().Unix() - tc.age
			sig, err := SignRequest(tc.signer, tc.escrow, nil, ts)
			require.NoError(t, err)
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set(HeaderSignature, hex.EncodeToString(sig))
			req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
			rec := httptest.NewRecorder()
			env.echo.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			if tc.status == http.StatusOK {
				require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestHTTPClientGetState(t *testing.T) {
	client, _, user, _ := setupClientTestEnv(t)
	ctx := context.Background()
	initial, err := client.GetState(ctx)
	require.NoError(t, err)
	require.Zero(t, initial.Nonce)
	require.Len(t, initial.StateRoot, 32)
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{testutil.StartTx(1)})
	response, err := client.Send(ctx, host.HostRequest{Diffs: []types.Diff{diff}}, nil, nil)
	require.NoError(t, err)
	current, err := client.GetState(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), current.Nonce)
	require.Equal(t, response.StateHash, current.StateRoot)
}
