package failover

import (
	"testing"
	"time"

	"common/chainoracle/blocks"

	"github.com/stretchr/testify/require"
)

func TestProofTipSelectsHigherReadyObservationBeforePublishing(t *testing.T) {
	at := time.Now()
	h := blocks.HashOnlyHeader(100, at, "gonka", []byte{100})
	next := blocks.HashOnlyHeader(101, at.Add(time.Second), "gonka", []byte{101})
	queued := make(chan alternativeResult[*blocks.Header], 1)
	queued <- alternativeResult[*blocks.Header]{value: next}
	var pending <-chan alternativeResult[*blocks.Header] = queued

	chosen, err := preferReadyAlternative(alternativeResult[*blocks.Header]{value: h}, &pending, newerProofTip)
	require.NoError(t, err)
	require.Equal(t, next, chosen.value)
	require.Nil(t, pending)

	oracle := New(nil, nil, nil)
	require.Zero(t, oracle.RecentProofTipHeight())
	require.NoError(t, oracle.checkProofTip(chosen.value))
	require.Equal(t, next.Height, oracle.RecentProofTipHeight())
}
