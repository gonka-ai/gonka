package harness

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/types"
)

// anchorAt is the time of the test chain's block the anchors are dated by.
var anchorAt = time.Unix(1_791_700_000, 0)

// blockHash is the test chain's hash of the block at height.
func blockHash(height uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], height)
	sum := sha256.Sum256(b[:])
	return sum[:]
}

// stampedStart is a Start stamped with the floor at height, as the gateway composes it.
func stampedStart(m *types.MsgStartInference, height uint64) *types.DevshardTx {
	m.ObservedHeight, m.ObservedBlockHash = height, blockHash(height)
	return &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: m}}
}

// heightAck is a host's acknowledgement of height, which raises the floor of the diffs after it.
func heightAck(slot uint32, height uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{
		SlotId: slot, ObservedHeight: height, ObservedBlockHash: blockHash(height)}}}
}

// confirmStart is the executor's ConfirmStart, stamped with the Start's floor as the executor signs it.
func confirmStart(id, startHeight uint64) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: id, ObservedHeight: startHeight, ObservedBlockHash: blockHash(startHeight)}}}
}

// diffRow is an exported diff row of escrow 2 at nonce holding txs.
func diffRow(t *testing.T, nonce uint64, txs ...*types.DevshardTx) map[string]string {
	t.Helper()
	raw, err := proto.Marshal(&types.DiffContent{Nonce: nonce, EscrowId: "2", Txs: txs})
	require.NoError(t, err)
	return map[string]string{"escrow_id": "2", "nonce": strconv.FormatUint(nonce, 10), "txs_proto": hex.EncodeToString(raw), "post_state_root": "aa"}
}

// writeChainHeaders writes the export's block headers at the given times.
func writeChainHeaders(t *testing.T, exportDir string, times map[uint64]time.Time) {
	t.Helper()
	var headers []ChainHeader
	for h, at := range times {
		headers = append(headers, ChainHeader{Height: h, Hash: hex.EncodeToString(blockHash(h)), TimeMs: at.UnixMilli()})
	}
	raw, err := json.Marshal(map[string]any{"headers": headers})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(exportDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(exportDir, ChainHeadersName), raw, 0o644))
}

// Test flow:
//  1. A host chain of escrow 2: Start of inference 1 stamped with floor 100 and a host's ack of 104
//     at nonce 1, an ack of 108 at nonce 2, the ConfirmStart (stamped 100, the Start's floor) and an
//     ack of 112 at nonce 3; the diffs are read out of nonce order.
//  2. Read the chain and date it by the headers.
//  3. Check the start is anchored at 100 and the confirm at 108, the floor as of its diff (the ack
//     in its own diff counts from the next one), each dated by its header.
func TestReadExportDiffsAnchorsTimeoutsAtTheFloor(t *testing.T) {
	c := hostChain("a", nil, nil)
	require.NoError(t, c.applyRows("test", []map[string]string{
		diffRow(t, 3, confirmStart(1, 100), heightAck(2, 112)),
		diffRow(t, 1, stampedStart(&types.MsgStartInference{InferenceId: 1, MaxTokens: 64}, 100), heightAck(1, 104)),
		diffRow(t, 2, heightAck(3, 108)),
	}))
	c.foldFloors()
	f := c.Inferences[ref(1)]
	require.Equal(t, uint64(100), f.StartedHeight)
	require.True(t, f.Confirmed)
	require.Equal(t, uint64(108), f.ConfirmedHeight)

	headers := ChainHeaders{}
	for h, at := range map[uint64]time.Time{100: anchorAt, 108: anchorAt.Add(5 * time.Second)} {
		headers[h] = ChainHeader{Height: h, Hash: hex.EncodeToString(blockHash(h)), TimeMs: at.UnixMilli()}
	}
	require.Zero(t, ExportDiffs{Hosts: []HostChain{c}}.DateAnchors(headers.Times))
	require.Equal(t, anchorAt.Unix(), f.StartedAt)
	require.Equal(t, anchorAt.Unix()+5, f.ConfirmedAt)
}

// Test flow:
//  1. A Start stamped with an empty floor (the timeout is not armed) and one stamped with a height
//     the headers do not hold.
//  2. Date them.
//  3. Check the empty floor has no time and is not counted as undated, and the unknown height is.
func TestDateAnchorsCountsOnlyHeightsItCannotDate(t *testing.T) {
	c := hostChain("a", map[uint64]*InferenceFacts{
		1: {Started: true},
		2: {Started: true, StartedHeight: 7},
	}, nil)
	require.Equal(t, 1, ExportDiffs{Hosts: []HostChain{c}}.DateAnchors(ChainHeaders{}.Times))
	require.Zero(t, c.Inferences[ref(1)].StartedAt)
	require.Zero(t, c.Inferences[ref(2)].StartedAt)
}

// Test flow:
//  1. Headers hold height 100.
//  2. Ask for its time with its hash, with another hash, and for a height they do not hold.
//  3. Check only the first is served; the others are pending, as for a state machine still waiting.
func TestChainHeadersServeOnlyTheBlockTheStampNames(t *testing.T) {
	h := ChainHeaders{100: {Height: 100, Hash: hex.EncodeToString(blockHash(100)), TimeMs: anchorAt.UnixMilli()}}
	ms, err := h.HeaderTimeMs(100, blockHash(100))
	require.NoError(t, err)
	require.Equal(t, anchorAt.UnixMilli(), ms)
	_, err = h.HeaderTimeMs(100, blockHash(101))
	require.ErrorIs(t, err, types.ErrHeaderPending)
	_, err = h.HeaderTimeMs(101, blockHash(101))
	require.ErrorIs(t, err, types.ErrHeaderPending)
}
