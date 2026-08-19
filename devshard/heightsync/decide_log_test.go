package heightsync_test

import (
	"context"
	"testing"

	"devshard/chainoracle/blocks"
	"devshard/heightsync"

	"github.com/stretchr/testify/require"
)

func TestDecide_LogStaleSyncTurn(t *testing.T) {
	or := &fakeStaleOracle{
		fakeOracle: fakeOracle{hdr: &blocks.Header{Height: 100, ChainID: "gonka", BlockHash: []byte{0xab}}},
		stale:      true,
	}
	s := heightsync.MustNewAnchorScheduler(8, 4, heightsync.NewLocalOracleSource(or))
	got, err, miss := s.Decide(context.Background(), heightsync.DecideHints{
		Nonce:     2,
		Direction: "response",
	})
	require.NoError(t, err)
	require.False(t, miss)
	require.NotNil(t, got)
	require.Positive(t, got.TipStaleAfterMs)
}
