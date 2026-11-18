package transport

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"devshard/types"
)

func TestHostTxDecodersDiscardUnknownFields(t *testing.T) {
	msg := &types.MsgFinishInference{InferenceId: 4, ExecutorSlot: 1, EscrowId: "escrow-1"}
	clean := proto.Clone(msg).(*types.MsgFinishInference)
	msg.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 1000, protowire.BytesType), make([]byte, 64)))
	tx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
	tx.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 999, protowire.VarintType), 7))
	raw, err := proto.Marshal(tx)
	require.NoError(t, err)

	txs, err := DevshardTxsFromBytes([][]byte{raw})
	require.NoError(t, err)
	require.Empty(t, txs[0].ProtoReflect().GetUnknown())
	require.True(t, proto.Equal(clean, txs[0].GetFinishInference()))

	resp, err := HostResponseFromJSON(InferenceResponse{Mempool: [][]byte{raw}})
	require.NoError(t, err)
	require.Empty(t, resp.Mempool[0].ProtoReflect().GetUnknown())
	require.True(t, proto.Equal(clean, resp.Mempool[0].GetFinishInference()))
}
