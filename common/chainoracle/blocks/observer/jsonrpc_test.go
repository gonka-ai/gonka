package observer

import (
	"context"
	"testing"
	"time"

	"common/chainoracle/blocks"

	"github.com/stretchr/testify/require"
)

func TestServeJSONRPC_NewTendermintTrusted(t *testing.T) {
	c := NewSignedChain(100)
	srv := c.ServeJSONRPC()
	t.Cleanup(srv.Close)

	o, err := NewTendermint(TendermintConfig{RPCURL: srv.URL, VerifyCommits: true})
	require.NoError(t, err)
	h, err := o.Trusted(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(100), h.Height)
	require.Equal(t, c.header(100).Hash().Bytes(), h.BlockHash)
}

func TestServeCometJSONRPC_RecordsQueuedCommit(t *testing.T) {
	got := make(chan int64, 4)
	srv := ServeCometJSONRPC(nil, func(h int64) { got <- h })
	t.Cleanup(srv.Close)

	o, err := NewTendermint(TendermintConfig{RPCURL: srv.URL, VerifyCommits: true})
	require.NoError(t, err)
	o.Observe(blocks.HashOnlyHeader(40, time.Unix(1_700_000_040, 0).UTC(), "gonka-test", []byte{40}))
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the stub RPC did not see the queued header's commit")
	}
}
