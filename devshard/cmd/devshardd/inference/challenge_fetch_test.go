package inference

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	commonvalidation "common/validation"

	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
	"devshard/transport"
)

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
