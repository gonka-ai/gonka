package types

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
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

// voteTx is a DevshardTx encoding of a timeout with n empty votes.
func voteTx(n int) []byte {
	timeout := bytes.Repeat([]byte{0x1a, 0x00}, n) // MsgTimeoutInference.votes
	return append(protowire.AppendVarint([]byte{0x22}, uint64(len(timeout))), timeout...)
}

func diffOf(txs ...[]byte) []byte {
	var b []byte
	for _, tx := range txs {
		b = protowire.AppendBytes(protowire.AppendTag(b, 2, protowire.BytesType), tx)
	}
	return b
}

func TestTxCount(t *testing.T) {
	b := append([]byte{0x08, 0x07}, emptyTxs(3)...) // nonce field, then 3 txs
	var c TxCount
	require.NoError(t, c.AddDiffContent(b))
	require.Equal(t, TxCount{Txs: 3}, c)

	c = TxCount{}
	require.NoError(t, c.AddDiffContent(diffOf(voteTx(5), voteTx(2))))
	require.Equal(t, TxCount{Txs: 2, Entries: 7}, c)

	c = TxCount{}
	require.Error(t, c.AddDiffContent([]byte{0x12, 0x05}))
	require.NotErrorIs(t, c.AddDiffContent([]byte{0x12, 0x05}), ErrDecodeLimit)
}

func TestTxCountNestedEntries(t *testing.T) {
	sync := protowire.AppendBytes([]byte{0x52}, bytes.Repeat([]byte{0x32, 0x00}, 3)) // heartbeat.sync_vector
	miss := protowire.AppendBytes([]byte{0x72}, bytes.Repeat([]byte{0x12, 0x00}, 4)) // error_miss.votes
	var c TxCount
	require.NoError(t, c.AddTx(sync))
	require.NoError(t, c.AddTx(miss))
	require.Equal(t, TxCount{Txs: 2, Entries: 7}, c)

	// One tx with too many votes, and the same votes spread over many txs.
	_, err := DecodeDiffWire(DiffWire{Nonce: 1, Txs: diffOf(voteTx(MaxDecodedTxEntries + 1))})
	require.ErrorIs(t, err, ErrDecodeLimit)
	var spread [][]byte
	for n := 0; n <= MaxDecodedTxEntries; n += MaxGroupSize {
		spread = append(spread, voteTx(MaxGroupSize))
	}
	_, err = DecodeDiffWire(DiffWire{Nonce: 1, Txs: diffOf(spread...)})
	require.ErrorIs(t, err, ErrDecodeLimit)

	var c2 TxCount
	require.NoError(t, c2.AddDiffContent(diffOf(spread[1:]...)))

	d, err := DecodeDiffWire(DiffWire{Nonce: 1, Txs: diffOf(spread[:3]...)})
	require.NoError(t, err)
	require.Len(t, d.Txs, 3)
	require.Len(t, d.Txs[0].GetTimeoutInference().Votes, MaxGroupSize)
}

func TestTxEntryFieldsMatchProto(t *testing.T) {
	tx := (&DevshardTx{}).ProtoReflect().Descriptor().Fields()
	want := map[protowire.Number]protowire.Number{}
	repeated := 0
	for i := 0; i < tx.Len(); i++ {
		m := tx.Get(i).Message()
		for j := 0; j < m.Fields().Len(); j++ {
			f := m.Fields().Get(j)
			if !f.IsList() {
				require.Nil(t, f.Message(), "%s is a message the count does not enter", f.FullName())
				continue
			}
			want[tx.Get(i).Number()] = f.Number()
			repeated++
			require.NotNil(t, f.Message(), "%s is a packed scalar list", f.FullName())
			entry := f.Message().Fields()
			for k := 0; k < entry.Len(); k++ {
				require.False(t, entry.Get(k).IsList() || entry.Get(k).Message() != nil,
					"%s holds nested fields the count does not reach", entry.Get(k).FullName())
			}
		}
	}
	require.Equal(t, len(want), repeated, "one repeated field per wrapped message")
	require.Equal(t, want, txEntryFields)
}
