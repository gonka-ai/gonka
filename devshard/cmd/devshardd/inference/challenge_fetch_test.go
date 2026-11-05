package inference

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"common/completionapi"
	"common/httpguard"
	"common/utils"
	commonvalidation "common/validation"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/productscience/inference/x/inference/calculations"
	chaintypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcserver"
)

type challengeURLRecorder struct {
	PayloadAuthClient
	key *secp256k1.PrivKey
}

func (challengeURLRecorder) GetAccountAddress() string { return "validator" }
func (r challengeURLRecorder) SignBytes(data []byte) (string, error) {
	if r.key == nil {
		return "signature", nil
	}
	sig, err := r.key.Sign(data)
	return base64.StdEncoding.EncodeToString(sig), err
}

func (r challengeURLRecorder) NewInferenceQueryClient() chaintypes.QueryClient {
	return challengeURLKeys{pubkey: base64.StdEncoding.EncodeToString(r.key.PubKey().Bytes())}
}

type challengeURLKeys struct {
	chaintypes.QueryClient
	pubkey string
}

func (q challengeURLKeys) GranteesByMessageType(context.Context, *chaintypes.QueryGranteesByMessageTypeRequest, ...grpc.CallOption) (*chaintypes.QueryGranteesByMessageTypeResponse, error) {
	return &chaintypes.QueryGranteesByMessageTypeResponse{}, nil
}

func (q challengeURLKeys) AccountByAddress(context.Context, *chaintypes.QueryAccountByAddressRequest, ...grpc.CallOption) (*chaintypes.QueryAccountByAddressResponse, error) {
	return &chaintypes.QueryAccountByAddressResponse{Pubkey: q.pubkey}, nil
}

type challengeURLBridge struct {
	bridge.MainnetBridge
	url string
	err error
}

func (b *challengeURLBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	return &bridge.HostInfo{Address: address, URL: b.url}, b.err
}

func TestFetchChallengePayload_ExecutorURL(t *testing.T) {
	for _, tc := range []struct {
		name         string
		url          string
		lookupErr    error
		payloadRPC   bool
		want         devshardpkg.ChallengeFetchOutcome
		executorSide bool
	}{
		{name: "empty", payloadRPC: true, want: devshardpkg.ChallengeFetchRetry, executorSide: true},
		{name: "spaces", url: "   ", payloadRPC: true, want: devshardpkg.ChallengeFetchRetry, executorSide: true},
		{name: "other whitespace", url: "\t\n\u00a0", payloadRPC: true, want: devshardpkg.ChallengeFetchRetry, executorSide: true},
		{name: "own RPC disabled", url: "http://executor.test", want: devshardpkg.ChallengeFetchLocal},
		{name: "own chain query failed", lookupErr: errors.New("chain unavailable"), payloadRPC: true, want: devshardpkg.ChallengeFetchRetry},
	} {
		for _, voteFalse := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/vote_false=%t", tc.name, voteFalse), func(t *testing.T) {
				v := newFaultTestValidator(10, voteFalse, nil, nil, nil)
				if tc.payloadRPC {
					v.SetPayloadRPC(testutil.MustGenerateKey(t), transport.ParseRPCEndpoints(transport.EndpointPayload))
				}
				t.Cleanup(v.ClosePayloadClients)
				br := &challengeURLBridge{url: tc.url, err: tc.lookupErr}
				v.fetchPayloads = func(ctx context.Context, req devshardpkg.ValidateRequest, id string, epoch uint64) ([]byte, []byte, error) {
					return fetchPayloadsFromExecutor(ctx, br, challengeURLRecorder{}, req, id, epoch, "", nil,
						func(url, address, escrow string) *transport.RPCClient {
							rpc, _ := v.payloadRPC(url, address, escrow, epoch)
							return rpc
						})
				}
				req := faultReq(10)
				req.EscrowID = "escrow-1"
				got := v.FetchChallengePayload(context.Background(), req)
				require.Equal(t, tc.want, got.Outcome, "cause: %s", got.Cause)
				require.Equal(t, tc.executorSide && voteFalse, got.ExecutorSide)
				verdict, err := v.Validate(context.Background(), req)
				require.Error(t, err, "replay still cannot fetch this payload")
				require.Nil(t, verdict, "replay must not start blaming an empty URL")
			})
		}
	}
}

func TestFetchChallengePayload_ExecutorURLRecovery(t *testing.T) {
	httpguard.SetAllowPrivate(true)
	prompt := []byte(`{"model":"test","messages":[]}`)
	body := []byte(`{"events":["data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"},\"logprobs\":{\"content\":[{\"token\":\"a\",\"logprob\":-0.1,\"top_logprobs\":[{\"token\":\"a\",\"logprob\":-0.1}]}]}}]}","data: [DONE]"]}`)
	served, err := completionapi.StripForGateway(body)
	require.NoError(t, err)
	promptHash, responseHash, servedHash := sha256.Sum256(prompt), sha256.Sum256(body), sha256.Sum256(served)
	req := faultReq(10)
	req.InferenceID, req.EscrowID = 42, "escrow-1"
	req.PromptHash, req.ResponseHash, req.ServedHash = promptHash[:], responseHash[:], servedHash[:]
	recorder := challengeURLRecorder{key: secp256k1.GenPrivKey()}
	sig, err := calculations.Sign(recorder, calculations.SignatureComponents{
		Payload:         "42" + utils.GenerateSHA256HashBytes(prompt) + utils.GenerateSHA256HashBytes(body),
		TransferAddress: req.ExecutorAddress,
	}, calculations.Developer)
	require.NoError(t, err)
	var calls atomic.Int32
	rpc := newPayloadRPCClient(t, func(_ context.Context, _ rpcserver.SessionCore, _ string, request *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		calls.Add(1)
		return &rpcpb.GetPayloadResponse{InferenceId: request.GetInferenceId(), PromptPayload: prompt, ResponsePayload: body, ExecutorSignature: sig}, nil
	})
	br := &challengeURLBridge{url: "   "}
	v := newFaultTestValidator(10, true, nil, nil, nil)
	v.fetchPayloads = func(ctx context.Context, req devshardpkg.ValidateRequest, id string, epoch uint64) ([]byte, []byte, error) {
		return fetchPayloadsFromExecutor(ctx, br, recorder, req, id, epoch, "", nil,
			func(url, _, _ string) *transport.RPCClient {
				if strings.TrimSpace(url) == "" {
					return nil
				}
				return rpc
			})
	}
	first := v.FetchChallengePayload(context.Background(), req)
	require.Equal(t, devshardpkg.ChallengeFetchRetry, first.Outcome)
	require.True(t, first.ExecutorSide)
	require.Zero(t, calls.Load())
	br.url = "http://executor.test"
	restored := v.FetchChallengePayload(context.Background(), req)
	require.Equal(t, devshardpkg.ChallengeFetchFetched, restored.Outcome, "cause: %s", restored.Cause)
	require.Equal(t, body, restored.Response, "the restored endpoint serves the signed body and all three hashes match")
	require.Equal(t, int32(1), calls.Load())
}

func TestFetchChallengePayload_Classification(t *testing.T) {
	ctx := context.Background()
	hashMismatch := verifyFetchedPayloadHashes(devshardpkg.ValidateRequest{PromptHash: []byte("not the prompt hash")}, []byte("prompt"), []byte("body"))
	require.Error(t, hashMismatch)

	tests := []struct {
		name         string
		err          error
		phaseEpoch   uint64
		want         devshardpkg.ChallengeFetchOutcome
		executorSide bool
	}{
		{name: "executor did not attach (restart)", err: classifyPayloadFetchError(ctx, fmt.Errorf("wait ready: %w", transport.ErrPeerPayloadUnanswered)), phaseEpoch: 10, want: devshardpkg.ChallengeFetchRetry, executorSide: true},
		{name: "429 after replay's one retry", err: classifyPayloadFetchError(ctx, fmt.Errorf("%w: %w", errPayloadRateLimitRetried, ErrPayloadRateLimited)), phaseEpoch: 10, want: devshardpkg.ChallengeFetchRetry, executorSide: true},
		{name: "connection refused", err: classifyPayloadFetchError(ctx, errors.New("dial: connection refused")), phaseEpoch: 10, want: devshardpkg.ChallengeFetchRetry, executorSide: true},
		{name: "own peer not ready", err: classifyPayloadFetchError(ctx, transport.ErrPeerNotReady), phaseEpoch: 10, want: devshardpkg.ChallengeFetchRetry},
		{name: "executor refused attach on every shared escrow", err: classifyPayloadFetchError(ctx, fmt.Errorf("wait ready: %w: %w", transport.ErrPeerNotReady, transport.ErrPeerNoAttachDoor)), phaseEpoch: 10, want: devshardpkg.ChallengeFetchRetry, executorSide: true},
		{name: "chain lookup of the executor keys", err: fmt.Errorf("resolve executor pubkeys: %w", errors.New("rpc down")), phaseEpoch: 10, want: devshardpkg.ChallengeFetchRetry},
		{name: "payload transport off", err: errPayloadRPCUnavailable, phaseEpoch: 10, want: devshardpkg.ChallengeFetchLocal},
		{name: "gone inside the retention", err: fmt.Errorf("payload not found on executor: %w", commonvalidation.ErrPayloadGone), phaseEpoch: 11, want: devshardpkg.ChallengeFetchUnavailable},
		{name: "gone after the retention", err: fmt.Errorf("payload not found on executor: %w", commonvalidation.ErrPayloadGone), phaseEpoch: 12, want: devshardpkg.ChallengeFetchPruned},
		{name: "too large", err: classifyPayloadFetchError(ctx, fmt.Errorf("%w: rpc read cap", commonvalidation.ErrPayloadTooLarge)), phaseEpoch: 10, want: devshardpkg.ChallengeFetchUnavailable},
		{name: "hash mismatch", err: hashMismatch, phaseEpoch: 10, want: devshardpkg.ChallengeFetchMismatch},
		{name: "bad executor signature", err: tagExecutorPayloadFault(fmt.Errorf("%w: %w", errExecutorPayloadSignature, errors.New("bad sig"))), phaseEpoch: 10, want: devshardpkg.ChallengeFetchMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newFaultTestValidator(tt.phaseEpoch, true, taggedFetch(tt.err), func(context.Context, string, string, []byte) (*http.Response, error) {
				t.Fatal("a challenge fetch must not call a model")
				return nil, nil
			}, nil)
			got := v.FetchChallengePayload(ctx, devshardpkg.ValidateRequest{InferenceID: 1, EpochID: 10})
			require.Equal(t, tt.want, got.Outcome, "cause: %s", got.Cause)
			require.Equal(t, tt.executorSide, got.ExecutorSide)
			require.Nil(t, got.Response)
		})
	}
}

// With DEVSHARD_VALIDATION_VOTE_FALSE_ON_FETCH_FAILURE off, replay leaves a
// fetch the executor failed unjudged instead of voting false; a challenge
// voter reads the same switch and leaves such a fetch unchecked at the
// deadline instead of voting PAYLOAD_UNAVAILABLE.
func TestFetchChallengePayload_VoteFalseSwitchOff(t *testing.T) {
	ctx := context.Background()
	executorDown := classifyPayloadFetchError(ctx, errors.New("dial: connection refused"))
	for _, testCase := range []struct {
		name         string
		voteFalse    bool
		executorSide bool
	}{
		{name: "switch on blames the executor", voteFalse: true, executorSide: true},
		{name: "switch off leaves the fetch unchecked", voteFalse: false, executorSide: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			v := newFaultTestValidator(10, testCase.voteFalse, taggedFetch(executorDown), func(context.Context, string, string, []byte) (*http.Response, error) {
				t.Fatal("a challenge fetch must not call a model")
				return nil, nil
			}, nil)
			got := v.FetchChallengePayload(ctx, devshardpkg.ValidateRequest{InferenceID: 1, EpochID: 10})
			require.Equal(t, devshardpkg.ChallengeFetchRetry, got.Outcome)
			require.Equal(t, testCase.executorSide, got.ExecutorSide)
		})
	}
}

// With the switch off, replay casts no vote on a payload the executor failed
// to keep inside the retention, handed over too large, or signed over a body
// that does not hash to its Finish: executorFaultVerdict returns nil and
// Validate returns an error. The challenge voter follows it and leaves such a
// fetch unchecked instead of settling a fault vote on it.
func TestFetchChallengePayload_VoteFalseSwitchOffWithholdsImmediateFaults(t *testing.T) {
	ctx := context.Background()
	gone := fmt.Errorf("payload not found on executor: %w", commonvalidation.ErrPayloadGone)
	hashMismatch := tagExecutorPayloadFault(fmt.Errorf("%w: response", commonvalidation.ErrHashMismatch))
	for _, testCase := range []struct {
		name      string
		err       error
		voteFalse bool
		want      devshardpkg.ChallengeFetchOutcome
	}{
		{name: "gone inside the retention, switch on", err: gone, voteFalse: true, want: devshardpkg.ChallengeFetchUnavailable},
		{name: "gone inside the retention, switch off", err: gone, voteFalse: false, want: devshardpkg.ChallengeFetchLocal},
		{name: "too large, switch off", err: classifyPayloadFetchError(ctx, fmt.Errorf("%w: rpc read cap", commonvalidation.ErrPayloadTooLarge)), voteFalse: false, want: devshardpkg.ChallengeFetchLocal},
		{name: "hash mismatch, switch on", err: hashMismatch, voteFalse: true, want: devshardpkg.ChallengeFetchMismatch},
		{name: "hash mismatch, switch off", err: hashMismatch, voteFalse: false, want: devshardpkg.ChallengeFetchLocal},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			v := newFaultTestValidator(11, testCase.voteFalse, taggedFetch(testCase.err), func(context.Context, string, string, []byte) (*http.Response, error) {
				t.Fatal("a challenge fetch must not call a model")
				return nil, nil
			}, nil)
			got := v.FetchChallengePayload(ctx, devshardpkg.ValidateRequest{InferenceID: 1, EpochID: 10})
			require.Equal(t, testCase.want, got.Outcome)
			require.False(t, got.ExecutorSide)
		})
	}
}

// A deadline that ends during the executor's payload call is the executor
// not answering; one that ends during this host's own lookups after the body
// arrived is this host's. The voter's deadline cut is classified here, by the
// call it landed on, so the host does not have to guess from the clock.
func TestFetchChallengePayload_ACutIsTheSideItLandedOn(t *testing.T) {
	cut, cancel := context.WithCancel(context.Background())
	cancel()
	for _, testCase := range []struct {
		name         string
		err          error
		voteFalse    bool
		want         devshardpkg.ChallengeFetchOutcome
		executorSide bool
	}{
		{name: "payload call cut", err: fmt.Errorf("%w: %w", errExecutorFetchCut, context.DeadlineExceeded), voteFalse: true, want: devshardpkg.ChallengeFetchRetry, executorSide: true},
		{name: "payload call cut, switch off", err: fmt.Errorf("%w: %w", errExecutorFetchCut, context.DeadlineExceeded), voteFalse: false, want: devshardpkg.ChallengeFetchRetry, executorSide: false},
		{name: "own key lookup cut", err: fmt.Errorf("resolve executor pubkeys: %w", context.DeadlineExceeded), voteFalse: true, want: devshardpkg.ChallengeFetchLocal, executorSide: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			v := newFaultTestValidator(10, testCase.voteFalse, taggedFetch(testCase.err), func(context.Context, string, string, []byte) (*http.Response, error) {
				t.Fatal("a challenge fetch must not call a model")
				return nil, nil
			}, nil)
			got := v.FetchChallengePayload(cut, devshardpkg.ValidateRequest{InferenceID: 1, EpochID: 10})
			require.Equal(t, testCase.want, got.Outcome)
			require.Equal(t, testCase.executorSide, got.ExecutorSide)
		})
	}
}

func TestFetchChallengePayload_EstablishedFaultSurvivesFetchDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	hashMismatch := verifyFetchedPayloadHashes(devshardpkg.ValidateRequest{PromptHash: []byte("different prompt")}, []byte("prompt"), []byte("body"))
	require.ErrorIs(t, hashMismatch, commonvalidation.ErrHashMismatch)
	badSignature := tagExecutorPayloadFault(fmt.Errorf("%w: bad signature", errExecutorPayloadSignature))
	wrapCut := func(err error) error { return fmt.Errorf("%w: %w", errExecutorFetchCut, err) }
	for _, tc := range []struct {
		name       string
		err        error
		phaseEpoch uint64
		want       devshardpkg.ChallengeFetchOutcome
	}{
		{name: "verified hash mismatch", err: hashMismatch, phaseEpoch: 10, want: devshardpkg.ChallengeFetchMismatch},
		{name: "invalid executor signature", err: badSignature, phaseEpoch: 10, want: devshardpkg.ChallengeFetchMismatch},
		{name: "gone inside retention", err: commonvalidation.ErrPayloadGone, phaseEpoch: 11, want: devshardpkg.ChallengeFetchUnavailable},
		{name: "gone after retention", err: commonvalidation.ErrPayloadGone, phaseEpoch: 12, want: devshardpkg.ChallengeFetchPruned},
		{name: "too large", err: commonvalidation.ErrPayloadTooLarge, phaseEpoch: 10, want: devshardpkg.ChallengeFetchUnavailable},
		{name: "gone inside retention wrapped by cut", err: wrapCut(commonvalidation.ErrPayloadGone), phaseEpoch: 11, want: devshardpkg.ChallengeFetchUnavailable},
		{name: "gone after retention wrapped by cut", err: wrapCut(commonvalidation.ErrPayloadGone), phaseEpoch: 12, want: devshardpkg.ChallengeFetchPruned},
		{name: "too large wrapped by cut", err: wrapCut(commonvalidation.ErrPayloadTooLarge), phaseEpoch: 10, want: devshardpkg.ChallengeFetchUnavailable},
	} {
		for _, voteFalse := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/vote_false=%t", tc.name, voteFalse), func(t *testing.T) {
				v := newFaultTestValidator(tc.phaseEpoch, voteFalse, taggedFetch(tc.err), nil, nil)
				got := v.FetchChallengePayload(ctx, devshardpkg.ValidateRequest{InferenceID: 1, EpochID: 10})
				want := tc.want
				if !voteFalse && (want == devshardpkg.ChallengeFetchMismatch || want == devshardpkg.ChallengeFetchUnavailable) {
					want = devshardpkg.ChallengeFetchLocal
				}
				require.Equal(t, want, got.Outcome, "cause: %s", got.Cause)
				require.False(t, got.ExecutorSide, "the explicit outcome must not fall through to a retry")
			})
		}
	}
}

func TestFetchChallengePayload_LocalFailureStaysLocalAfterFetchDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	for _, err := range []error{errPayloadRPCUnavailable, fmt.Errorf("resolve executor pubkeys: %w", ctx.Err())} {
		v := newFaultTestValidator(10, true, taggedFetch(err), nil, nil)
		got := v.FetchChallengePayload(ctx, devshardpkg.ValidateRequest{InferenceID: 1, EpochID: 10})
		require.Equal(t, devshardpkg.ChallengeFetchLocal, got.Outcome)
		require.False(t, got.ExecutorSide)
	}
}

func TestFetchChallengePayload_ReleasedPeersDoNotVoteOnEstablishedFault(t *testing.T) {
	t.Cleanup(transport.ResetOutboundPeerReleaseForTest)
	transport.ReleaseOutboundPeerConns()
	hashMismatch := verifyFetchedPayloadHashes(devshardpkg.ValidateRequest{PromptHash: []byte("different prompt")}, []byte("prompt"), []byte("body"))
	v := newFaultTestValidator(10, true, taggedFetch(hashMismatch), nil, nil)
	got := v.FetchChallengePayload(context.Background(), devshardpkg.ValidateRequest{InferenceID: 1, EpochID: 10})
	require.Equal(t, devshardpkg.ChallengeFetchLocal, got.Outcome)
	require.False(t, got.ExecutorSide)
}

func TestFetchChallengePayload_FetchedBodyAndLease(t *testing.T) {
	body := []byte(`{"events":[]}`)
	inner := newFaultTestValidator(10, true, func(context.Context, devshardpkg.ValidateRequest, string, uint64) ([]byte, []byte, error) {
		return []byte("prompt"), body, nil
	}, nil, nil)
	got := inner.FetchChallengePayload(context.Background(), devshardpkg.ValidateRequest{InferenceID: 1, EpochID: 10})
	require.Equal(t, devshardpkg.ChallengeFetchFetched, got.Outcome)
	require.Equal(t, body, got.Response)

	leased := NewLeaseValidator(inner, inner.phase, nil, testLeaseOwner(), 0)
	got = leased.FetchChallengePayload(context.Background(), devshardpkg.ValidateRequest{InferenceID: 1, EpochID: 10})
	require.Equal(t, devshardpkg.ChallengeFetchFetched, got.Outcome, "a challenge fetch takes no validation lease")
}
