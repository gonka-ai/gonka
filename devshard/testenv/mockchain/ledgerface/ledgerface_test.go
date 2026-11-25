package ledgerface_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chaintx "common/chain/tx"
	"devshard/signing"
	"devshard/testenv/mockchain/adminface"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/ledgerface"
	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/txledger"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Test flow:
// 1. Start mock-chain gRPC with a ledger and mount ledgerface on the admin server.
// 2. Broadcast a create escrow tx and then the same settle twice with different fees.
// 3. GET /testenv/txs?type=settle and check every settle field decoded from the stored bytes.
// 4. GET /testenv/txs?type=create, /testenv/escrows and a bad type filter.
func TestLedgerface_SettleTxRoundTrip(t *testing.T) {
	st := seed.Defaults()
	rpcSvc, err := rpcface.NewService(st, rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)
	ledger := txledger.New()
	srv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{Store: st, RPC: rpcSvc, Ledger: ledger})
	require.NoError(t, err)
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	admin := adminface.NewServer(st, nil, nil)
	ledgerface.Mount(admin.Group(), st, ledger)
	ts := httptest.NewServer(admin.Handler())
	t.Cleanup(ts.Close)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	txMgr, err := chaintx.New(conn, chaintx.Config{
		ChainID: st.GetChainID(), FeeAmount: 1_000, GasLimit: 200_000,
		PollInterval: time.Millisecond, PollTimeout: 2 * time.Second,
	})
	require.NoError(t, err)
	signer, err := signing.GenerateKey()
	require.NoError(t, err)

	created, err := txMgr.CreateDevshardEscrow(context.Background(), signer, 500_000, "model-a")
	require.NoError(t, err)
	settle := chaintx.SettleParams{
		EscrowID:  created.EscrowID,
		StateRoot: []byte{0xaa, 0xbb},
		Nonce:     42,
		RestHash:  []byte{0x01},
		HostStats: []chaintx.HostStats{
			{SlotID: 0, Cost: 700, RequiredValidations: 3, CompletedValidations: 3},
			{SlotID: 1, Missed: 2, Invalid: 1, Cost: 300},
		},
		Signatures:                  []chaintx.SlotSignature{{SlotID: 0, Signature: []byte{1}}, {SlotID: 1, Signature: []byte{2}}},
		Fees:                        55,
		StateRootAndProtocolVersion: []byte("aabb:v6"),
	}
	_, err = txMgr.SettleDevshardEscrow(context.Background(), signer, settle)
	require.NoError(t, err)
	settle.Fees = 56
	_, err = txMgr.SettleDevshardEscrow(context.Background(), signer, settle)
	require.NoError(t, err)

	var settles struct {
		Txs []ledgerface.TxView `json:"txs"`
	}
	getJSON(t, ts.URL+"/testenv/txs?type=settle", http.StatusOK, &settles)
	require.Len(t, settles.Txs, 2)
	first := settles.Txs[0]
	require.Equal(t, "settle", first.Type)
	require.Empty(t, first.Error)
	require.Less(t, first.Seq, settles.Txs[1].Seq)
	require.Equal(t, &ledgerface.SettleTxView{
		Settler:                     signer.Address(),
		EscrowID:                    created.EscrowID,
		Nonce:                       42,
		StateRoot:                   hex.EncodeToString([]byte{0xaa, 0xbb}),
		StateRootAndProtocolVersion: "aabb:v6",
		RestHash:                    "01",
		Fees:                        55,
		HostStats: []ledgerface.SettleHostStatView{
			{SlotID: 0, Cost: 700, RequiredValidations: 3, CompletedValidations: 3},
			{SlotID: 1, Missed: 2, Invalid: 1, Cost: 300},
		},
		SignatureSlots: []uint32{0, 1},
	}, first.Settle)
	require.Equal(t, uint64(56), settles.Txs[1].Settle.Fees)

	var creates struct {
		Txs []ledgerface.TxView `json:"txs"`
	}
	getJSON(t, ts.URL+"/testenv/txs?type=create", http.StatusOK, &creates)
	require.Len(t, creates.Txs, 1)
	require.Equal(t, &ledgerface.CreateTxView{Creator: signer.Address(), Amount: 500_000, ModelID: "model-a"}, creates.Txs[0].Create)

	var escrows struct {
		Escrows []ledgerface.EscrowView `json:"escrows"`
	}
	getJSON(t, ts.URL+"/testenv/escrows", http.StatusOK, &escrows)
	var found *ledgerface.EscrowView
	for i := range escrows.Escrows {
		if escrows.Escrows[i].ID == created.EscrowID {
			found = &escrows.Escrows[i]
		}
	}
	require.NotNil(t, found)
	require.Equal(t, "model-a", found.ModelID)
	require.Equal(t, uint64(500_000), found.Amount)
	require.True(t, found.Settled)

	getJSON(t, ts.URL+"/testenv/txs?type=vote", http.StatusBadRequest, nil)
}

func getJSON(t *testing.T, url string, status int, out any) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, status, resp.StatusCode)
	if out != nil {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out))
	}
}
