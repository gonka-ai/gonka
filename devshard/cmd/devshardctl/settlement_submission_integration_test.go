//go:build settlementintegration

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"common/chain"
	"common/chainoracle/blocks"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/types"
	"devshard/user"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdktx "github.com/cosmos/cosmos-sdk/types/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/productscience/inference/x/inference/keeper"
	chainTypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type replacementSettlementHost struct {
	nonce     uint64
	root      []byte
	old, next []byte
	mu        sync.Mutex
	attested  bool
}

func (h *replacementSettlementHost) Send(_ context.Context, req host.HostRequest, _ io.Writer, _ func(*host.HostResponse)) (*host.HostResponse, error) {
	if req.Nonce != h.nonce {
		return nil, fmt.Errorf("host received unexpected final nonce %d", req.Nonce)
	}
	h.mu.Lock()
	h.attested = true
	h.mu.Unlock()
	return &host.HostResponse{Nonce: h.nonce, StateHash: h.root, StateSig: h.next}, nil
}

func (h *replacementSettlementHost) GetSignatures(_ context.Context, nonce uint64) (map[uint32][]byte, error) {
	if nonce != h.nonce {
		return nil, fmt.Errorf("host received unexpected signature nonce %d", nonce)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.attested && h.next != nil {
		return map[uint32][]byte{0: h.next}, nil
	}
	return map[uint32][]byte{0: h.old}, nil
}

type settlementTestChain struct {
	chainTypes.UnimplementedQueryServer
	sdktx.UnimplementedServiceServer

	mu            sync.Mutex
	escrow        chainTypes.DevshardEscrow
	grants        map[string]bool
	offline       bool
	revokeOnSend  bool
	revokeOnCheck bool
	grantQueries  int
	old           string
	attempts      []*chainTypes.MsgSettleDevshardEscrow
	deliver       map[string]*sdk.TxResponse
}

func (c *settlementTestChain) GranteesByMessageType(_ context.Context, req *chainTypes.QueryGranteesByMessageTypeRequest) (*chainTypes.QueryGranteesByMessageTypeResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.offline {
		return nil, status.Error(codes.Unavailable, "current grant information temporarily unavailable")
	}
	if req.GranterAddress != c.escrow.Slots[0] || req.MessageTypeUrl != chainTypes.WarmKeyGrantMarkerTypeURL {
		return nil, status.Error(codes.InvalidArgument, "unexpected grant query")
	}
	c.grantQueries++
	if c.revokeOnCheck && c.grantQueries == 2 {
		delete(c.grants, c.old)
	}
	out := &chainTypes.QueryGranteesByMessageTypeResponse{}
	for addr := range c.grants {
		out.Grantees = append(out.Grantees, &chainTypes.Grantee{Address: addr})
	}
	return out, nil
}

type settlementAccountQuery struct {
	authtypes.UnimplementedQueryServer
}

func (settlementAccountQuery) Account(_ context.Context, req *authtypes.QueryAccountRequest) (*authtypes.QueryAccountResponse, error) {
	a, err := codectypes.NewAnyWithValue(&authtypes.BaseAccount{Address: req.Address, AccountNumber: 1})
	return &authtypes.QueryAccountResponse{Account: a}, err
}

func (c *settlementTestChain) DevshardEscrow(context.Context, *chainTypes.QueryGetDevshardEscrowRequest) (*chainTypes.QueryGetDevshardEscrowResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &chainTypes.QueryGetDevshardEscrowResponse{Escrow: &c.escrow, Found: true}, nil
}

func (c *settlementTestChain) BroadcastTx(_ context.Context, req *sdktx.BroadcastTxRequest) (*sdktx.BroadcastTxResponse, error) {
	var raw sdktx.TxRaw
	var body sdktx.TxBody
	var msg chainTypes.MsgSettleDevshardEscrow
	if err := raw.Unmarshal(req.TxBytes); err != nil {
		return nil, err
	}
	if err := body.Unmarshal(raw.BodyBytes); err != nil {
		return nil, err
	}
	if len(body.Messages) != 1 {
		return nil, status.Error(codes.InvalidArgument, "expected one settlement message")
	}
	if err := msg.Unmarshal(body.Messages[0].Value); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts = append(c.attempts, &msg)
	if c.revokeOnSend {
		delete(c.grants, c.old)
		c.revokeOnSend = false
	}
	hash := fmt.Sprintf("%064x", len(c.attempts))
	deliver := &sdk.TxResponse{TxHash: hash}
	if err := keeper.VerifyDevshardSettlement(c.escrow, &msg, &chainTypes.DevshardEscrowParams{MaxNonce: chainTypes.DefaultDevshardMaxNonce}, nil, func(cold, warm string) bool {
		return cold == c.escrow.Slots[0] && c.grants[warm]
	}); err != nil {
		deliver.Code, deliver.RawLog = 1, err.Error()
	} else {
		c.escrow.Settled = true
	}
	c.deliver[hash] = deliver
	return &sdktx.BroadcastTxResponse{TxResponse: &sdk.TxResponse{TxHash: hash}}, nil
}

func (c *settlementTestChain) GetTx(_ context.Context, req *sdktx.GetTxRequest) (*sdktx.GetTxResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return &sdktx.GetTxResponse{TxResponse: c.deliver[req.Hash]}, nil
}

func dialSettlementTestChain(t *testing.T, chainServer *settlementTestChain) *chain.Client {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	chainTypes.RegisterQueryServer(server, chainServer)
	authtypes.RegisterQueryServer(server, &settlementAccountQuery{})
	sdktx.RegisterServiceServer(server, chainServer)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///settlement-test-chain",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return chain.NewFromConn(conn)
}

func TestV7GatewaySettlementRebuildsRevokedQuorum(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	for _, tc := range []struct {
		name                  string
		revoked, after, check bool
		replacement           string
		offline               bool
		wantSends             int
		wantWeight            string
	}{
		{name: "revoked before submission uses cold", revoked: true, replacement: "cold", wantSends: 1},
		{name: "revoked during preflight uses cold", check: true, replacement: "cold", wantSends: 1},
		{name: "revoked after preflight uses different bound warm key", after: true, replacement: "warm", wantSends: 2},
		{name: "revoked after preflight with only old signature", after: true, wantSends: 1, wantWeight: "warm grant ended"},
		{name: "unavailable grants preserve historical quorum", offline: true, wantWeight: "current grant unavailable"},
		{name: "current grant without a frozen binding has no weight", revoked: true, replacement: "unbound", wantWeight: "warm grant ended"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			owner, cold, warm, alternate := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
			addr, err := blocks.AddressBytes(cold.CompressedPublicKeyBytes())
			require.NoError(t, err)
			oracle, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "settlement-test",
				Validators: []mockoracle.MockValidator{{Signer: cold, Address: addr, Power: 10}}, InitialHeight: 100,
				Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
			require.NoError(t, err)
			anchor, err := oracle.AdvanceOne()
			require.NoError(t, err)
			config := testutil.DefaultConfig(1)
			config.MainnetAnchorHeight, config.MainnetAnchorHash = anchor.Height, anchor.BlockHash
			group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold})
			const escrowID, balance = "42", 1_000_000
			localStore := storage.NewMemory()
			require.NoError(t, localStore.CreateSession(storage.CreateSessionParams{EscrowID: escrowID, Version: "v7",
				CreatorAddr: owner.Address(), Config: config, Group: group, InitialBalance: balance}))
			checkOldGrants := state.WithWarmKeyHeightChecker(func(_, addr string, id uint64, height int64, _ time.Time) (bool, error) {
				return height == anchor.Height && (id == 1 && addr == warm.Address() || id == 2 && addr == alternate.Address()), nil
			})
			verifier := signing.NewSecp256k1Verifier()
			sm, err := state.NewStateMachine(escrowID, config, group, balance, owner.Address(), verifier, localStore,
				state.WithVersion("v7"), state.WithMainnetHeights(oracle), checkOldGrants)
			require.NoError(t, err)
			var root []byte
			var nonce uint64
			for sm.Phase() != types.PhaseSettlement && nonce < 4 {
				nonce++
				proof, err := sm.BuildMainnetHeightTx(ctx)
				require.NoError(t, err)
				txs := []*types.DevshardTx{proof}
				if nonce == 1 {
					txs = append(txs, testutil.WarmKeyBindTx(0, warm.Address(), 1))
					if tc.replacement != "unbound" {
						txs = append(txs, testutil.WarmKeyBindTx(0, alternate.Address(), 2))
					}
					txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}})
				}
				root, err = sm.ApplyLocal(nonce, txs)
				require.NoError(t, err)
				diff := testutil.SignDiffWithRoot(t, owner, escrowID, nonce, txs, root)
				require.NoError(t, localStore.AppendDiff(escrowID, types.DiffRecord{Diff: diff, StateHash: root}))
			}
			require.Equal(t, types.PhaseSettlement, sm.Phase())
			sigBytes, err := proto.Marshal(&types.StateSignatureContent{EscrowId: escrowID, Nonce: nonce, StateRoot: root})
			require.NoError(t, err)
			oldSignature, err := warm.Sign(sigBytes)
			require.NoError(t, err)
			require.NoError(t, localStore.AddSignature(escrowID, nonce, 0, oldSignature))
			replacement := &replacementSettlementHost{nonce: nonce, root: root, old: oldSignature}
			switch tc.replacement {
			case "cold":
				replacement.next, err = cold.Sign(sigBytes)
			case "warm", "unbound":
				replacement.next, err = alternate.Sign(sigBytes)
			}
			require.NoError(t, err)
			session, recovered, err := user.RecoverSession(localStore, owner, verifier, escrowID, "v7", group, []user.HostClient{replacement},
				state.WithMainnetHeights(oracle), checkOldGrants)
			require.NoError(t, err)
			defer session.Close()
			require.True(t, session.HasQuorumAt(nonce))
			chainServer := &settlementTestChain{
				escrow: chainTypes.DevshardEscrow{Id: 42, Creator: owner.Address(), Amount: balance, Slots: []string{cold.Address()}},
				grants: map[string]bool{warm.Address(): true, alternate.Address(): true}, old: warm.Address(),
				offline: tc.offline, revokeOnSend: tc.after, revokeOnCheck: tc.check, deliver: make(map[string]*sdk.TxResponse),
			}
			if tc.revoked {
				delete(chainServer.grants, warm.Address())
			}
			store, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
			require.NoError(t, err)
			defer store.Close()
			runtimeConfig := RuntimeConfig{ID: escrowID, Model: "llama"}
			require.NoError(t, store.Initialize(ctx, GatewaySettings{DefaultModel: "llama"}, []GatewayDevshardState{{RuntimeConfig: runtimeConfig, Active: true}}))
			rt := &devshardRuntime{id: escrowID, session: session, proxy: &Proxy{sm: recovered, session: session, escrowID: escrowID}}
			gateway := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "llama")
			gateway.store, gateway.chainClient = store, dialSettlementTestChain(t, chainServer)
			result, err := gateway.settleDevshardOnChain(ctx, escrowID, adminSettleEscrowRequest{PrivateKey: owner.PrivateKeyHex(), ChainID: "settlement-test"})
			if tc.wantWeight != "" {
				require.ErrorContains(t, err, "0/1 submit-eligible weight")
				require.ErrorContains(t, err, cold.Address())
				require.ErrorContains(t, err, tc.wantWeight)
				require.Nil(t, result)
				require.Equal(t, oldSignature, session.Signatures()[nonce][0])
			} else {
				require.NoError(t, err)
				require.Equal(t, uint64(42), result.EscrowID)
			}
			chainServer.mu.Lock()
			defer chainServer.mu.Unlock()
			require.Len(t, chainServer.attempts, tc.wantSends)
			require.Equal(t, tc.wantWeight == "", chainServer.escrow.Settled)
			for _, sent := range chainServer.attempts {
				require.Equal(t, uint64(42), sent.EscrowId)
				require.Equal(t, nonce, sent.Nonce)
				require.Equal(t, root, sent.StateRoot)
			}
			if tc.wantWeight == "" {
				last := chainServer.attempts[len(chainServer.attempts)-1]
				require.Equal(t, replacement.next, last.Signatures[0].Signature)
			}
		})
	}
}
