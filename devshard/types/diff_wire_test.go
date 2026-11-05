package types

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// emptyTxs is a DiffContent encoding of n empty txs, 2 bytes each.
func emptyTxs(n int) []byte {
	return bytes.Repeat([]byte{0x12, 0x00}, n)
}

func TestDecodeDiffWireTxCap(t *testing.T) {
	d, err := DecodeDiffWire(DiffWire{Nonce: 1, Txs: emptyTxs(MaxDecodedTxs)})
	require.NoError(t, err)
	require.Len(t, d.Txs, MaxDecodedTxs)

	_, err = DecodeDiffWire(DiffWire{Nonce: 1, Txs: emptyTxs(MaxDecodedTxs + 1)})
	require.ErrorIs(t, err, ErrDecodeLimit)
}

func TestCountDiffTxs(t *testing.T) {
	b := append([]byte{0x08, 0x07}, emptyTxs(3)...) // nonce field, then 3 txs
	n, err := CountDiffTxs(b, 3)
	require.NoError(t, err)
	require.Equal(t, 3, n)

	_, err = CountDiffTxs(b, 2)
	require.ErrorIs(t, err, ErrDecodeLimit)

	_, err = CountDiffTxs([]byte{0x12, 0x05}, 10)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrDecodeLimit)
}
