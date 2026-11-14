package inference

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"common/httpguard"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	chaintypes "github.com/productscience/inference/x/inference/types"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcserver"
)

type oogQueryClient struct {
	chaintypes.QueryClient
	granteesErr error
}

func (q oogQueryClient) GranteesByMessageType(context.Context, *chaintypes.QueryGranteesByMessageTypeRequest, ...grpc.CallOption) (*chaintypes.QueryGranteesByMessageTypeResponse, error) {
	if q.granteesErr != nil {
		return nil, q.granteesErr
	}
	return &chaintypes.QueryGranteesByMessageTypeResponse{}, nil
}

func (q oogQueryClient) AccountByAddress(context.Context, *chaintypes.QueryAccountByAddressRequest, ...grpc.CallOption) (*chaintypes.QueryAccountByAddressResponse, error) {
	return &chaintypes.QueryAccountByAddressResponse{}, nil
}

type oogRecorder struct {
	PayloadAuthClient
	qc oogQueryClient
}

func (r oogRecorder) NewInferenceQueryClient() chaintypes.QueryClient { return r.qc }
func (oogRecorder) GetAccountAddress() string                         { return "validator" }
func (oogRecorder) GetSignerAddress() string                          { return "validator" }
func (oogRecorder) SignBytes([]byte) (string, error)                  { return "c2ln", nil }

// The payload is served with a bogus executor signature in both cases.
func fetchWithGranteesErr(t *testing.T, granteesErr error) error {
	httpguard.SetAllowPrivate(true)
	rpc := newPayloadRPCClient(t, func(_ context.Context, _ rpcserver.SessionCore, _ string, req *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		return &rpcpb.GetPayloadResponse{InferenceId: req.GetInferenceId(), PromptPayload: []byte("prompt"), ResponsePayload: []byte("fake response")}, nil
	})
	req := devshardpkg.ValidateRequest{EscrowID: "escrow-1", ExecutorAddress: "executor", InferenceID: 42}
	_, _, err := fetchPayloadsFromExecutor(context.Background(), hostInfoBridge{url: "http://unused"},
		oogRecorder{qc: oogQueryClient{granteesErr: granteesErr}}, req, "42", 1, "path", nil,
		func(string, string, string) *transport.RPCClient { return rpc })
	return err
}

func TestFetchPayloads_GranteesOutOfGasIsExecutorFault(t *testing.T) {
	req := devshardpkg.ValidateRequest{EscrowID: "escrow-1", ExecutorAddress: "executor", InferenceID: 42, EpochID: 1}

	// The executor's own grants exhaust the node's query gas: Valid:false, not an abstain.
	oog := status.Error(codes.Unknown, "codespace sdk code 11: out of gas: Query gas limit exceeded: 10000000, out of gas in location: ReadFlat")
	err := fetchWithGranteesErr(t, oog)
	require.ErrorIs(t, err, errExecutorPayloadFault)
	v := executorFaultVerdict(context.Background(), nil, req, 1, err, true)
	require.NotNil(t, v, "OOG on executor grants must yield a verdict: %v", err)
	require.False(t, v.Valid)
	t.Logf("OOG path: err=%v -> Valid=%v", err, v.Valid)

	// Same out-of-gas over the ABCI fallback, where the SDK reports it as a recovered panic.
	err = fetchWithGranteesErr(t, status.Error(codes.Unknown, "{ValuePerByte}: panic"))
	require.ErrorIs(t, err, errExecutorPayloadFault)

	// A node that is merely unavailable says nothing about the executor: still no verdict.
	err = fetchWithGranteesErr(t, status.Error(codes.Unavailable, "connection refused"))
	require.False(t, errors.Is(err, errExecutorPayloadFault), "transient chain error must not blame the executor: %v", err)
	require.Nil(t, executorFaultVerdict(context.Background(), nil, req, 1, err, true))

	// Control: grantees resolve, the bogus signature is the executor's fault.
	err = fetchWithGranteesErr(t, nil)
	require.ErrorIs(t, err, errExecutorPayloadFault)
	v = executorFaultVerdict(context.Background(), nil, req, 1, err, true)
	require.NotNil(t, v)
	require.False(t, v.Valid)
}

type hostInfoErrBridge struct {
	bridge.MainnetBridge
	err error
}

func (b hostInfoErrBridge) GetHostInfo(string) (*bridge.HostInfo, error) { return nil, b.err }

func TestFetchPayloads_ParticipantOutOfGasIsExecutorFault(t *testing.T) {
	req := devshardpkg.ValidateRequest{EscrowID: "escrow-1", ExecutorAddress: "executor", InferenceID: 42, EpochID: 1}
	fetch := func(hostErr error) error {
		_, _, err := fetchPayloadsFromExecutor(context.Background(), hostInfoErrBridge{err: hostErr},
			oogRecorder{}, req, "42", 1, "path", nil, nil)
		return err
	}

	// The executor's own multi-megabyte InferenceUrl exhausts the node's query gas.
	oog := fmt.Errorf("%w: Participant executor: %w", bridge.ErrChainUnavailable,
		status.Error(codes.Unknown, "codespace sdk code 11: out of gas: Query gas limit exceeded: 10000000, out of gas in location: ReadPerByte"))
	err := fetch(oog)
	require.ErrorIs(t, err, errExecutorPayloadFault)
	v := executorFaultVerdict(context.Background(), nil, req, 1, err, true)
	require.NotNil(t, v, "OOG on the executor's record must yield a verdict: %v", err)
	require.False(t, v.Valid)

	// A node that is merely unavailable says nothing about the executor.
	err = fetch(fmt.Errorf("%w: %w", bridge.ErrChainUnavailable, status.Error(codes.Unavailable, "connection refused")))
	require.False(t, errors.Is(err, errExecutorPayloadFault), "transient chain error must not blame the executor: %v", err)
	require.Nil(t, executorFaultVerdict(context.Background(), nil, req, 1, err, true))
}
