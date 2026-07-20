package rpcface_test

import (
	"context"
	"testing"
	"time"

	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/stretchr/testify/require"

	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
)

func TestAdvanceEpoch_FastForwardsBlocksAndUpdatesNextPoc(t *testing.T) {
	st := seed.Defaults()
	svc, url, cleanup, err := rpcface.NewInProcessServer(st, rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(cleanup)

	resp, err := svc.AdvanceEpoch(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(350), resp.BlocksPublished)
	require.Equal(t, int64(150), resp.FromBlockHeight)
	require.Equal(t, int64(500), resp.ToBlockHeight)
	require.Equal(t, int64(900), resp.NextPocStartBlockHeight)
	require.Equal(t, uint64(2), resp.Epoch.Index)

	require.Equal(t, int64(500), st.GetBlockHeight())
	require.Equal(t, int64(900), st.GetNextPocStartBlockHeight())

	rpc, err := rpchttp.New(url, "/websocket")
	require.NoError(t, err)
	status, err := rpc.Status(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(500), status.SyncInfo.LatestBlockHeight)
}
