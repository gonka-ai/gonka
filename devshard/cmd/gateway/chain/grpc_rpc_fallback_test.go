package chain

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	commonchain "common/chain"
	"common/observability"
	"cosmossdk.io/math"
	abci "github.com/cometbft/cometbft/abci/types"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	rpctypes "github.com/cometbft/cometbft/rpc/jsonrpc/types"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	accountMethod            = "/cosmos.auth.v1beta1.Query/Account"
	spendableBalanceMethod   = "/cosmos.bank.v1beta1.Query/SpendableBalanceByDenom"
	getTxMethod              = "/cosmos.tx.v1beta1.Service/GetTx"
	broadcastMethod          = "/cosmos.tx.v1beta1.Service/BroadcastTx"
	abciQueryRPCMethod       = "abci_query"
	broadcastSyncRPCMethod   = "broadcast_tx_sync"
	rpcFixtureAddress        = "gonka1fixture"
	rpcFixtureDenom          = "ngonka"
	rpcFixtureAccountNumber  = 7
	rpcFixtureAccountSeq     = 3
	rpcFixtureBalance        = 5000
	rpcFixtureTxHash         = "ABCDEF"
	rpcFixtureEventType      = "devshard_escrow_created"
	rpcFixtureEventKey       = "escrow_id"
	rpcFixtureEventValue     = "42"
	rpcFixtureChainID        = "gonka-test"
	txNotFoundLog            = "tx not found: unknown hash"
	nodeFailureLog           = "internal node failure"
	rpcFixtureBroadcastBytes = "signed-tx"
)

type cometRPCDouble struct {
	mu                sync.Mutex
	abciPaths         []string
	broadcastCalls    int
	failBroadcast     bool
	abciResponseBytes map[string][]byte
	abciFailure       *abci.ResponseQuery
	broadcastHash     []byte
}

func (double *cometRPCDouble) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	var rpcRequest rpctypes.RPCRequest
	if err := json.Unmarshal(body, &rpcRequest); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	var result any
	double.mu.Lock()
	switch rpcRequest.Method {
	case abciQueryRPCMethod:
		var params struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(rpcRequest.Params, &params)
		double.abciPaths = append(double.abciPaths, params.Path)
		response := abci.ResponseQuery{Value: double.abciResponseBytes[params.Path]}
		if double.abciFailure != nil {
			response = *double.abciFailure
		}
		result = &coretypes.ResultABCIQuery{Response: response}
	case broadcastSyncRPCMethod:
		double.broadcastCalls++
		if double.failBroadcast {
			double.mu.Unlock()
			http.Error(writer, "upstream closed", http.StatusBadGateway)
			return
		}
		result = &coretypes.ResultBroadcastTx{Hash: double.broadcastHash}
	}
	double.mu.Unlock()
	encoded, err := json.Marshal(rpctypes.NewRPCSuccessResponse(rpcRequest.ID, result))
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(encoded)
}

func (double *cometRPCDouble) answerQueriesWith(response abci.ResponseQuery) {
	double.mu.Lock()
	defer double.mu.Unlock()
	double.abciFailure = &response
}

func (double *cometRPCDouble) failBroadcasts() {
	double.mu.Lock()
	defer double.mu.Unlock()
	double.failBroadcast = true
}

func (double *cometRPCDouble) recordedAbciPaths() []string {
	double.mu.Lock()
	defer double.mu.Unlock()
	return slices.Clone(double.abciPaths)
}

func (double *cometRPCDouble) recordedBroadcasts() int {
	double.mu.Lock()
	defer double.mu.Unlock()
	return double.broadcastCalls
}

func newCometRPCDouble(t *testing.T) (*cometRPCDouble, string) {
	t.Helper()
	account, err := codectypes.NewAnyWithValue(&authtypes.BaseAccount{Address: rpcFixtureAddress, AccountNumber: rpcFixtureAccountNumber, Sequence: rpcFixtureAccountSeq})
	if err != nil {
		t.Fatalf("pack account: %v", err)
	}
	responses := map[string]proto{
		accountMethod:          &authtypes.QueryAccountResponse{Account: account},
		spendableBalanceMethod: &banktypes.QuerySpendableBalanceByDenomResponse{Balance: &sdk.Coin{Denom: rpcFixtureDenom, Amount: math.NewInt(rpcFixtureBalance)}},
		getTxMethod: &txtypes.GetTxResponse{TxResponse: &sdk.TxResponse{
			TxHash: rpcFixtureTxHash,
			Events: []abci.Event{{Type: rpcFixtureEventType, Attributes: []abci.EventAttribute{{Key: rpcFixtureEventKey, Value: rpcFixtureEventValue}}}},
		}},
	}
	double := &cometRPCDouble{abciResponseBytes: map[string][]byte{}, broadcastHash: []byte{0xAB, 0xCD, 0xEF}}
	for method, message := range responses {
		encoded, err := message.Marshal()
		if err != nil {
			t.Fatalf("marshal %s: %v", method, err)
		}
		double.abciResponseBytes[method] = encoded
	}
	server := httptest.NewServer(double)
	t.Cleanup(server.Close)
	return double, server.URL
}

type proto interface{ Marshal() ([]byte, error) }

type unavailableGRPCDouble struct {
	mu      sync.Mutex
	methods []string
	address string
}

func (double *unavailableGRPCDouble) recordedMethods() []string {
	double.mu.Lock()
	defer double.mu.Unlock()
	return slices.Clone(double.methods)
}

func newUnavailableGRPCDouble(t *testing.T) *unavailableGRPCDouble {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	double := &unavailableGRPCDouble{address: listener.Addr().String()}
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(stream)
		double.mu.Lock()
		double.methods = append(double.methods, method)
		double.mu.Unlock()
		return status.Error(codes.Unavailable, "chain gRPC is down")
	}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return double
}

func unreachableGRPCAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return address
}

func closeDirectConnection(t *testing.T, client *commonchain.Client) {
	t.Helper()
	observed, isObserved := client.Conn().(observability.ObservedConn)
	if !isObserved {
		t.Errorf("client connection is %T, want an observed connection", client.Conn())
		return
	}
	closer, isCloser := observed.ClientConnInterface.(io.Closer)
	if !isCloser {
		t.Errorf("direct connection is %T, want one that closes", observed.ClientConnInterface)
		return
	}
	if err := closer.Close(); err != nil {
		t.Errorf("close direct connection: %v", err)
	}
}

func newChainOver(t *testing.T, grpcAddress, rpcURL string) *GRPCChain {
	t.Helper()
	client, err := commonchain.NewWithRPCFallback(grpcAddress, rpcURL)
	if err != nil {
		t.Fatalf("NewWithRPCFallback: %v", err)
	}
	t.Cleanup(func() { closeDirectConnection(t, client) })
	return NewGRPCChain(client, rpcFixtureChainID)
}

// Test flow:
//  1. Build a GRPCChain over a client whose direct gRPC address refuses connections and whose CometBFT RPC answers account, balance and tx queries and a sync broadcast.
//  2. Read the account, read the spendable balance, broadcast a signed body and look the transaction up.
//  3. Assert every call succeeded with the values the RPC served, the three queries reached the RPC as ABCI queries and the broadcast reached it once.
func TestTransactionsRunOverCometRPCWhenGRPCIsUnreachable(t *testing.T) {
	rpc, rpcURL := newCometRPCDouble(t)
	grpcChain := newChainOver(t, unreachableGRPCAddress(t), rpcURL)
	ctx := context.Background()

	account, err := grpcChain.Account(ctx, rpcFixtureAddress)
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if account != (Account{Number: rpcFixtureAccountNumber, Sequence: rpcFixtureAccountSeq}) {
		t.Fatalf("account = %+v, want number %d sequence %d", account, rpcFixtureAccountNumber, rpcFixtureAccountSeq)
	}
	balance, err := grpcChain.SpendableBalance(ctx, rpcFixtureAddress, rpcFixtureDenom)
	if err != nil {
		t.Fatalf("SpendableBalance: %v", err)
	}
	if balance != rpcFixtureBalance {
		t.Fatalf("balance = %d, want %d", balance, rpcFixtureBalance)
	}
	txHash, err := grpcChain.Broadcast(ctx, []byte(rpcFixtureBroadcastBytes))
	if err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	if txHash != rpcFixtureTxHash {
		t.Fatalf("broadcast hash = %q, want %q", txHash, rpcFixtureTxHash)
	}
	result, found, err := grpcChain.Tx(ctx, txHash)
	if err != nil || !found {
		t.Fatalf("Tx found=%v err=%v, want the transaction", found, err)
	}
	wantEvents := []TxEvent{{Type: rpcFixtureEventType, Attributes: []TxAttribute{{Key: rpcFixtureEventKey, Value: rpcFixtureEventValue}}}}
	if !slices.EqualFunc(result.Events, wantEvents, func(got, want TxEvent) bool {
		return got.Type == want.Type && slices.Equal(got.Attributes, want.Attributes)
	}) {
		t.Fatalf("tx events = %+v, want %+v", result.Events, wantEvents)
	}
	if paths := rpc.recordedAbciPaths(); !slices.Equal(paths, []string{accountMethod, spendableBalanceMethod, getTxMethod}) {
		t.Fatalf("RPC ABCI queries = %v, want account, balance and tx lookup", paths)
	}
	if broadcasts := rpc.recordedBroadcasts(); broadcasts != 1 {
		t.Fatalf("RPC broadcasts = %d, want 1", broadcasts)
	}
}

// Test flow:
//  1. Build a GRPCChain over a direct gRPC that answers Unavailable to everything and a CometBFT RPC that fails every broadcast with a bad gateway.
//  2. Read the account, which moves the client onto the RPC, then broadcast.
//  3. Assert the broadcast returned an error, reached the RPC exactly once and never reached the direct gRPC.
func TestABroadcastThatFailsOverRPCIsNotResentOverGRPC(t *testing.T) {
	rpc, rpcURL := newCometRPCDouble(t)
	rpc.failBroadcasts()
	direct := newUnavailableGRPCDouble(t)
	grpcChain := newChainOver(t, direct.address, rpcURL)
	ctx := context.Background()

	if _, err := grpcChain.Account(ctx, rpcFixtureAddress); err != nil {
		t.Fatalf("Account: %v", err)
	}
	if _, err := grpcChain.Broadcast(ctx, []byte(rpcFixtureBroadcastBytes)); err == nil {
		t.Fatal("Broadcast succeeded, want the RPC failure returned")
	}

	if broadcasts := rpc.recordedBroadcasts(); broadcasts != 1 {
		t.Fatalf("RPC broadcasts = %d, want 1", broadcasts)
	}
	if methods := direct.recordedMethods(); !slices.Equal(methods, []string{accountMethod}) {
		t.Fatalf("direct gRPC saw %v, want only the account read that moved the client onto the RPC", methods)
	}
}

// Test flow:
//  1. Build a GRPCChain over a direct gRPC that answers Unavailable to everything and a CometBFT RPC that is healthy, and make no read first so the client is still on gRPC.
//  2. Broadcast.
//  3. Assert the broadcast returned an error, reached the direct gRPC exactly once and never reached the RPC.
func TestABroadcastThatFailsOverGRPCIsNotResentOverCometRPC(t *testing.T) {
	rpc, rpcURL := newCometRPCDouble(t)
	direct := newUnavailableGRPCDouble(t)
	grpcChain := newChainOver(t, direct.address, rpcURL)

	if _, err := grpcChain.Broadcast(context.Background(), []byte(rpcFixtureBroadcastBytes)); err == nil {
		t.Fatal("Broadcast succeeded, want the gRPC failure returned")
	}

	if methods := direct.recordedMethods(); !slices.Equal(methods, []string{broadcastMethod}) {
		t.Fatalf("direct gRPC saw %v, want one broadcast", methods)
	}
	if broadcasts := rpc.recordedBroadcasts(); broadcasts != 0 {
		t.Fatalf("RPC broadcasts = %d, want 0", broadcasts)
	}
}

// Test flow:
//  1. Build a GRPCChain over an unreachable direct gRPC and a CometBFT RPC whose tx lookup answers a key-not-found code, an unknown code that says the transaction was not found, and an unknown code that says nothing of the kind.
//  2. For each answer, read the account so the client moves onto the RPC, then look the transaction up.
//  3. Assert the first two report found=false without an error and the third returns an error instead of reading the failure as absence.
func TestATxLookupOverCometRPCSeparatesAbsenceFromFailure(t *testing.T) {
	cases := []struct {
		name      string
		answer    abci.ResponseQuery
		wantError bool
	}{
		{name: "key not found code", answer: abci.ResponseQuery{Code: sdkerrors.ErrKeyNotFound.ABCICode(), Log: txNotFoundLog}},
		{name: "unknown code with the not found text", answer: abci.ResponseQuery{Code: sdkerrors.ErrPanic.ABCICode(), Log: txNotFoundLog}},
		{name: "unknown code without it", answer: abci.ResponseQuery{Code: sdkerrors.ErrPanic.ABCICode(), Log: nodeFailureLog}, wantError: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rpc, rpcURL := newCometRPCDouble(t)
			grpcChain := newChainOver(t, unreachableGRPCAddress(t), rpcURL)
			if _, err := grpcChain.Account(context.Background(), rpcFixtureAddress); err != nil {
				t.Fatalf("Account: %v", err)
			}
			rpc.answerQueriesWith(testCase.answer)

			_, found, err := grpcChain.Tx(context.Background(), rpcFixtureTxHash)

			if found {
				t.Fatal("Tx reported found, want the transaction absent")
			}
			if (err != nil) != testCase.wantError {
				t.Fatalf("Tx error = %v, want an error: %v", err, testCase.wantError)
			}
		})
	}
}
