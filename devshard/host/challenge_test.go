package host

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// scriptedChallengeFetcher answers FetchChallengePayload from a script; the
// last entry repeats. It never runs a replay.
type scriptedChallengeFetcher struct {
	mu      sync.Mutex
	script  []devshard.ChallengeFetch
	calls   atomic.Int32
	gate    chan struct{}
	lastReq devshard.ValidateRequest
	// onCut is what a gated fetch reports when its context ends first, as
	// the real fetcher classifies the call the cut landed on.
	onCut *devshard.ChallengeFetch
}

func (f *scriptedChallengeFetcher) Validate(context.Context, devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	panic("a challenge must not run a replay")
}

func (f *scriptedChallengeFetcher) FetchChallengePayload(ctx context.Context, req devshard.ValidateRequest) devshard.ChallengeFetch {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			if f.onCut != nil {
				return *f.onCut
			}
			return devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchLocal, Cause: "closed"}
		}
	}
	n := int(f.calls.Add(1))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastReq = req
	if n > len(f.script) {
		n = len(f.script)
	}
	return f.script[n-1]
}

type challengeFixture struct {
	hosts []*signing.Secp256k1Signer
	user  *signing.Secp256k1Signer
	store *storage.Memory
	diffs []types.Diff
	// sealGrace is the session's InferenceSealGraceSeconds; 7200 when unset.
	sealGrace uint32
}

// bodyWithPositions is a stored body with one generated position: what an
// honest executor keeps, since its devshardd forces logprobs on every request.
var bodyWithPositions = []byte(`{"events":["data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"},\"logprobs\":{\"content\":[{\"token\":\"a\",\"logprob\":-0.1,\"top_logprobs\":[{\"token\":\"a\",\"logprob\":-0.1}]}]}}]}","data: [DONE]"]}`)

const challengeEscrow = "escrow-1"

// newChallengeFixture signs start, confirm and finish diffs for inference 1
// on three hosts; slot 1 executes it and claims 40 output tokens.
func newChallengeFixture(t *testing.T) *challengeFixture {
	t.Helper()
	return newChallengeFixtureFor(t, "llama", 40)
}

// newChallengeFixtureFor is newChallengeFixture for a model and a claimed
// output token count.
func newChallengeFixtureFor(t *testing.T, model string, outputTokens uint64) *challengeFixture {
	t.Helper()
	f := &challengeFixture{
		hosts: []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)},
		user:  testutil.MustGenerateKey(t),
	}
	group := testutil.MakeGroup(f.hosts)
	config := testutil.DefaultConfig(len(f.hosts))
	f.store = testutil.MustMemoryStore(t, challengeEscrow, f.user.Address(), config, group, 10000)
	receipt := testutil.SignExecutorReceipt(t, f.hosts[1], challengeEscrow, 1, testutil.TestPromptHash[:], model, 100, testutil.TestMaxTokens)
	finish := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
		InputTokens: 80, OutputTokens: outputTokens, ExecutorSlot: 1, EscrowId: challengeEscrow,
	}
	finish.ProposerSig = testutil.SignProposerTx(t, f.hosts[1], finish)
	start := testutil.StartTx(1)
	start.GetStartInference().Model = model
	f.diffs = []types.Diff{
		testutil.SignDiff(t, f.user, challengeEscrow, 1, []*types.DevshardTx{start}),
		testutil.SignDiff(t, f.user, challengeEscrow, 2, []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: receipt,
		}}}}),
		testutil.SignDiff(t, f.user, challengeEscrow, 3, []*types.DevshardTx{{Tx: &types.DevshardTx_FinishInference{FinishInference: finish}}}),
	}
	return f
}

// voter builds host hostIdx on the fixture's store and applies the diffs,
// with a fetch window of windowSeconds: the window is a constant capped at
// half the seal grace, so the grace is set to twice that. A second call on
// the same fixture is the same participant after a restart.
func (f *challengeFixture) voter(t *testing.T, hostIdx int, fetcher devshard.ValidationEngine, windowSeconds int64) *Host {
	t.Helper()
	group := testutil.MakeGroup(f.hosts)
	config := testutil.DefaultConfig(len(f.hosts))
	config.InferenceSealGraceSeconds = uint32(2 * windowSeconds)
	if f.sealGrace > 0 {
		config.InferenceSealGraceSeconds = f.sealGrace
	}
	sm, err := state.NewStateMachine(challengeEscrow, config, group, 10000, f.user.Address(), signing.NewSecp256k1Verifier(), f.store)
	require.NoError(t, err)
	h, err := NewHost(sm, f.hosts[hostIdx], stub.NewInferenceEngine(), challengeEscrow, group, nil, WithValidator(fetcher), WithStorage(f.store), WithEpochID(7))
	require.NoError(t, err)
	t.Cleanup(h.Close)
	h.ApplyCatchUpDiffs(f.diffs)
	rec, ok := sm.GetInference(1)
	require.True(t, ok)
	require.Equal(t, types.StatusFinished, rec.Status)
	return h
}

func withChallengeTiming(t *testing.T, retry, wait time.Duration) {
	t.Helper()
	prevRetry, prevWait := challengeFetchRetryInterval, challengeAnswerWait
	challengeFetchRetryInterval, challengeAnswerWait = retry, wait
	t.Cleanup(func() { challengeFetchRetryInterval, challengeAnswerWait = prevRetry, prevWait })
}

func requireVoteSignedBy(t *testing.T, signer *signing.Secp256k1Signer, answer ChallengeAnswer) {
	t.Helper()
	preimage, err := types.CanonicalSignedBytes(&types.ChallengeVoteContent{
		EscrowId:     challengeEscrow,
		InferenceId:  1,
		Fault:        answer.Fault,
		Check:        answer.Check,
		Deviation:    answer.Deviation,
		ResponseHash: testutil.TestResponseHash,
	})
	require.NoError(t, err)
	recovered, err := signing.NewSecp256k1Verifier().RecoverAddress(preimage, answer.Signature)
	require.NoError(t, err)
	require.Equal(t, signer.Address(), recovered)
}

func TestVerifyChallenge_Verdicts(t *testing.T) {
	withChallengeTiming(t, time.Millisecond, 5*time.Second)
	cases := map[string]struct {
		fetch     devshard.ChallengeFetch
		wantFault types.ChallengeFault
		wantCheck types.ChallengeCheck
	}{
		"hashes match": {
			fetch:     devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchFetched, Response: bodyWithPositions},
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_NONE,
		},
		"body is not the signed one": {
			fetch:     devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchMismatch},
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH,
			wantCheck: types.ChallengeCheck_CHALLENGE_CHECK_HASH,
		},
		"body gone inside the retention": {
			fetch:     devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchUnavailable},
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE,
		},
		"body gone after the retention": {
			fetch:     devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchPruned},
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_NONE,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newChallengeFixture(t)
			fetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{tc.fetch}}
			h := f.voter(t, 0, fetcher, 60)
			answer, err := h.VerifyChallenge(context.Background(), 1)
			require.NoError(t, err)
			require.False(t, answer.Pending)
			require.Empty(t, answer.RejectCause)
			require.Equal(t, tc.wantFault, answer.Fault)
			require.Equal(t, tc.wantCheck, answer.Check)
			require.Equal(t, uint32(0), answer.VoterSlot)
			requireVoteSignedBy(t, f.hosts[0], answer)

			fetcher.mu.Lock()
			req := fetcher.lastReq
			fetcher.mu.Unlock()
			require.Equal(t, testutil.TestPromptHash[:], req.PromptHash)
			require.Equal(t, testutil.TestResponseHash, req.ResponseHash)
			require.Equal(t, testutil.TestServedHash, req.ServedHash)
			require.Equal(t, f.hosts[1].Address(), req.ExecutorAddress)
			require.Equal(t, uint64(7), req.EpochID)
		})
	}
}

func TestVerifyChallenge_RetriesInsideTheWindow(t *testing.T) {
	withChallengeTiming(t, time.Millisecond, 5*time.Second)
	restart := devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchRetry, ExecutorSide: true, Cause: "executor did not attach"}
	rateLimited := devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchRetry, ExecutorSide: true, Cause: "payload fetch rate limited"}
	fetched := devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchFetched, Response: bodyWithPositions}

	t.Run("an executor restart and a 429 are retried, not faults", func(t *testing.T) {
		f := newChallengeFixture(t)
		fetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{restart, rateLimited, restart, fetched}}
		h := f.voter(t, 0, fetcher, 60)
		answer, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, answer.Fault)
		require.Equal(t, int32(4), fetcher.calls.Load())
	})

	t.Run("unreachable for the whole window", func(t *testing.T) {
		f := newChallengeFixture(t)
		fetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{restart}}
		h := f.voter(t, 0, fetcher, 1)
		answer, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, answer.Fault)
		require.Greater(t, fetcher.calls.Load(), int32(1), "the host retried before it voted")
		requireVoteSignedBy(t, f.hosts[0], answer)
	})

	t.Run("this host's own failure is no vote", func(t *testing.T) {
		f := newChallengeFixture(t)
		ownPeer := devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchRetry, Cause: "own peer not ready"}
		fetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{ownPeer}}
		h := f.voter(t, 0, fetcher, 1)
		answer, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, ChallengeRejectUnchecked, answer.RejectCause)
		require.Empty(t, answer.Signature)
	})

	t.Run("a fetch this host cannot run is no vote", func(t *testing.T) {
		f := newChallengeFixture(t)
		fetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchLocal}}}
		h := f.voter(t, 0, fetcher, 60)
		answer, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, ChallengeRejectUnchecked, answer.RejectCause)
		require.Equal(t, int32(1), fetcher.calls.Load())
	})
}

// A gateway is not trusted to challenge promptly. One that waits until the
// executor is restarting and then challenges inferences whose window is
// about to close must not get PAYLOAD_UNAVAILABLE votes against an executor
// that kept the body: the voters would have seconds to fetch it instead of
// the window. A late first request still gets a vote when the executor
// answers.
func TestVerifyChallenge_LateFirstRequestIsNoFault(t *testing.T) {
	withChallengeTiming(t, 10*time.Millisecond, 2*time.Second)
	restart := devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchRetry, ExecutorSide: true, Cause: "executor did not attach"}
	fetched := devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchFetched, Response: bodyWithPositions}
	lateVoter := func(t *testing.T, fetcher *scriptedChallengeFetcher) (*Host, *challengeFixture) {
		f := newChallengeFixture(t)
		h := f.voter(t, 0, fetcher, 60)
		h.mu.Lock()
		obs := h.finishObs[1]
		obs.at = time.Now().Add(-59 * time.Second)
		h.finishObs[1] = obs
		h.mu.Unlock()
		return h, f
	}

	t.Run("executor unreachable", func(t *testing.T) {
		fetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{restart}}
		h, _ := lateVoter(t, fetcher)
		for range 2 {
			answer, err := h.VerifyChallenge(context.Background(), 1)
			require.NoError(t, err)
			require.Empty(t, answer.Signature, "a fault vote %s after a second of retries", answer.Fault)
			require.Equal(t, ChallengeRejectLate, answer.RejectCause)
		}
	})

	t.Run("executor answers", func(t *testing.T) {
		fetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{fetched}}
		h, f := lateVoter(t, fetcher)
		answer, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, answer.Fault)
		requireVoteSignedBy(t, f.hosts[0], answer)
	})
}

func TestVerifyChallenge_OneVerdictPerInference(t *testing.T) {
	withChallengeTiming(t, time.Millisecond, 50*time.Millisecond)

	t.Run("pending while the fetch is open, then the same vote", func(t *testing.T) {
		f := newChallengeFixture(t)
		fetcher := &scriptedChallengeFetcher{
			script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchUnavailable}},
			gate:   make(chan struct{}),
		}
		h := f.voter(t, 0, fetcher, 60)
		answer, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.True(t, answer.Pending)
		require.Empty(t, answer.Signature)

		again, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.True(t, again.Pending, "a repeat joins the open fetch")

		close(fetcher.gate)
		var first ChallengeAnswer
		require.Eventually(t, func() bool {
			first, err = h.VerifyChallenge(context.Background(), 1)
			return err == nil && !first.Pending
		}, 5*time.Second, 5*time.Millisecond)
		require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, first.Fault)

		// The executor serving the body now changes nothing: the vote is kept.
		fetcher.mu.Lock()
		fetcher.script = []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchFetched, Response: bodyWithPositions}}
		fetcher.mu.Unlock()
		second, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, first, second)
		require.Equal(t, int32(1), fetcher.calls.Load())
	})

	t.Run("the vote survives a host restart", func(t *testing.T) {
		f := newChallengeFixture(t)
		fetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchMismatch}}}
		before := f.voter(t, 0, fetcher, 60)
		voted, err := before.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_HASH_MISMATCH, voted.Fault)
		before.Close()

		restartedFetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchFetched, Response: bodyWithPositions}}}
		after := f.voter(t, 0, restartedFetcher, 60)
		kept, err := after.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, voted, kept)
		require.Zero(t, restartedFetcher.calls.Load())
	})

	t.Run("a fetch cut by a restart resumes with its deadline", func(t *testing.T) {
		f := newChallengeFixture(t)
		blocked := &scriptedChallengeFetcher{
			script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchFetched, Response: bodyWithPositions}},
			gate:   make(chan struct{}),
		}
		before := f.voter(t, 0, blocked, 3600)
		answer, err := before.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.True(t, answer.Pending)
		before.Close()

		// Restarted with a one-second window and attempts 600 ms apart: the
		// executor answers on the third, after that second. The stored
		// deadline, an hour after the Finish, still governs, so the host
		// waits for it instead of voting PAYLOAD_UNAVAILABLE.
		withChallengeTiming(t, 600*time.Millisecond, 50*time.Millisecond)
		down := devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchRetry, ExecutorSide: true}
		up := devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchFetched, Response: bodyWithPositions}
		resumed := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{down, down, up}}
		after := f.voter(t, 0, resumed, 1)
		var kept ChallengeAnswer
		require.Eventually(t, func() bool {
			kept, err = after.VerifyChallenge(context.Background(), 1)
			return err == nil && !kept.Pending
		}, 10*time.Second, 20*time.Millisecond)
		require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_NONE, kept.Fault)
		require.Equal(t, int32(3), resumed.calls.Load())
	})

	t.Run("a late challenge does not reopen the window", func(t *testing.T) {
		f := newChallengeFixture(t)
		down := devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchRetry, ExecutorSide: true}
		fetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{down}}
		h := f.voter(t, 0, fetcher, 60)
		h.mu.Lock()
		obs := h.finishObs[1]
		obs.at = time.Now().Add(-2 * time.Minute)
		h.finishObs[1] = obs
		h.mu.Unlock()

		answer, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, ChallengeRejectLate, answer.RejectCause, "an executor that is down when a late challenge comes is not at fault")
		require.Equal(t, int32(1), fetcher.calls.Load(), "one attempt, no retries")
	})
}

func TestVerifyChallenge_EmptyStream(t *testing.T) {
	withChallengeTiming(t, time.Millisecond, 5*time.Second)
	// logit_bias on a byte fragment: tokens are generated, the text is empty.
	withPositions := []byte(`{"events":["data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"logprobs\":{\"content\":[{\"token\":\"\\\\xe2\",\"logprob\":-0.1,\"top_logprobs\":[{\"token\":\"\\\\xe2\",\"logprob\":-0.1}]}]}}]}","data: [DONE]"]}`)
	noPositions := []byte(`{"events":["data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"logprobs\":null}]}","data: [DONE]"]}`)
	require.Equal(t, 1, generatedPositions(withPositions))
	require.Zero(t, generatedPositions(noPositions))

	cases := map[string]struct {
		fetch     devshard.ChallengeFetch
		wantFault types.ChallengeFault
		wantCheck types.ChallengeCheck
		wantDev   int64
	}{
		"tokens generated, text empty": {
			fetch:     devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchFetched, Response: withPositions},
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_NONE,
		},
		"no generated positions": {
			fetch:     devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchFetched, Response: noPositions},
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM,
			wantCheck: types.ChallengeCheck_CHALLENGE_CHECK_POSITIONS,
			wantDev:   40,
		},
		"no body": {
			fetch:     devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchUnavailable},
			wantFault: types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newChallengeFixture(t)
			h := f.voter(t, 0, &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{tc.fetch}}, 60)
			answer, err := h.VerifyChallenge(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, tc.wantFault, answer.Fault)
			require.Equal(t, tc.wantCheck, answer.Check)
			require.Equal(t, tc.wantDev, answer.Deviation)
			requireVoteSignedBy(t, f.hosts[0], answer)
		})
	}

	t.Run("a stream challenge counts positions too", func(t *testing.T) {
		f := newChallengeFixture(t)
		h := f.voter(t, 0, &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchFetched, Response: noPositions}}}, 60)
		answer, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM, answer.Fault)
	})

	// The kept vote is one per inference, whichever reason came first: a
	// stream challenge settled on a body without positions must not hide
	// the empty body from a later empty-stream challenge.
	t.Run("an earlier stream challenge does not hide an empty body", func(t *testing.T) {
		f := newChallengeFixture(t)
		h := f.voter(t, 0, &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchFetched, Response: noPositions}}}, 60)
		_, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		answer, err := h.VerifyChallenge(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM, answer.Fault)
	})
}

func TestVerifyChallenge_Rejects(t *testing.T) {
	withChallengeTiming(t, time.Millisecond, 5*time.Second)
	f := newChallengeFixture(t)
	fetcher := &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchUnavailable}}}

	executor := f.voter(t, 1, fetcher, 60)
	answer, err := executor.VerifyChallenge(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, ChallengeRejectExecutor, answer.RejectCause)

	voter := f.voter(t, 0, fetcher, 60)
	answer, err = voter.VerifyChallenge(context.Background(), 2)
	require.NoError(t, err)
	require.Equal(t, ChallengeRejectNotLive, answer.RejectCause)

	noFetcher := f.voter(t, 2, stub.NewValidationEngine(), 60)
	answer, err = noFetcher.VerifyChallenge(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, ChallengeRejectUnsupported, answer.RejectCause)
	require.Zero(t, fetcher.calls.Load())
}

// A second ask while the fetch is open answers pending at once: the first ask
// holds for challengeAnswerWait, later ones must not hold a slot of the
// gateway's verifier queue for the wait.
func TestVerifyChallenge_RepeatAskWhileFetchingAnswersAtOnce(t *testing.T) {
	withChallengeTiming(t, time.Millisecond, 2*time.Second)
	f := newChallengeFixture(t)
	fetcher := &scriptedChallengeFetcher{
		script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchFetched, Response: bodyWithPositions}},
		gate:   make(chan struct{}),
	}
	h := f.voter(t, 0, fetcher, 60)
	first := time.Now()
	answer, err := h.VerifyChallenge(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, answer.Pending)
	require.GreaterOrEqual(t, time.Since(first), 2*time.Second, "the first ask waits for the fetch")

	again := time.Now()
	answer, err = h.VerifyChallenge(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, answer.Pending)
	require.Less(t, time.Since(again), 500*time.Millisecond, "a repeat ask does not wait")
	close(fetcher.gate)
}

// The fetch window is capped at half the seal grace, so the votes reach a
// diff before the record seals; the first-ask limit follows the window.
func TestChallengeWindow_CappedAtHalfTheSealGrace(t *testing.T) {
	f := newChallengeFixture(t)
	f.sealGrace = 600
	h := f.voter(t, 0, &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchLocal}}}, 1800)
	require.Equal(t, 5*time.Minute, h.challengeWindow())
	require.Equal(t, 150*time.Second, h.challengeFirstAsk())

	wide := newChallengeFixture(t)
	wide.sealGrace = 7200
	hw := wide.voter(t, 0, &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchLocal}}}, 1800)
	require.Equal(t, 30*time.Minute, hw.challengeWindow())
	require.Equal(t, challengeFirstAskLimit, hw.challengeFirstAsk())
}

// A fetch attempt started inside the window and still running past it is cut
// challengeFetchGrace after the deadline and settles the vote then: one
// attempt can retry inside the peer client and wait out a Retry-After for
// minutes, and the gateway stops asking two minutes after the window, so a
// vote that settles later is never collected.
func TestVerifyChallenge_AFetchPastTheWindowIsCut(t *testing.T) {
	withChallengeTiming(t, time.Millisecond, 50*time.Millisecond)
	savedGrace := challengeFetchGrace
	challengeFetchGrace = 200 * time.Millisecond
	t.Cleanup(func() { challengeFetchGrace = savedGrace })

	f := newChallengeFixture(t)
	fetcher := &scriptedChallengeFetcher{
		script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchFetched, Response: bodyWithPositions}},
		gate:   make(chan struct{}),
	}
	h := f.voter(t, 0, fetcher, 1)
	answer, err := h.VerifyChallenge(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, answer.Pending)

	// The executor never answers the attempt; the host does not wait for
	// it past the window plus the grace.
	var kept ChallengeAnswer
	require.Eventually(t, func() bool {
		kept, err = h.VerifyChallenge(context.Background(), 1)
		return err == nil && !kept.Pending
	}, 3*time.Second, 20*time.Millisecond, "the vote settles without the fetch returning")
	require.Equal(t, types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE, kept.Fault, "an attempt the executor left unanswered inside the window")
	requireVoteSignedBy(t, f.hosts[0], kept)
	close(fetcher.gate)
}

// A fetch the window cuts while this host's own lookup is still running (the
// executor's keys from this host's chain node, after the body arrived) is
// this host's failure, not the executor's. The fetcher reports the side the
// cut landed on, and the voter leaves the vote unchecked.
func TestVerifyChallenge_ACutOnThisHostsSideLeavesTheVoteUnchecked(t *testing.T) {
	withChallengeTiming(t, time.Millisecond, 50*time.Millisecond)
	savedGrace := challengeFetchGrace
	challengeFetchGrace = 200 * time.Millisecond
	t.Cleanup(func() { challengeFetchGrace = savedGrace })

	f := newChallengeFixture(t)
	fetcher := &scriptedChallengeFetcher{
		script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchFetched, Response: bodyWithPositions}},
		gate:   make(chan struct{}),
		onCut:  &devshard.ChallengeFetch{Outcome: devshard.ChallengeFetchLocal, Cause: "resolve executor pubkeys: context deadline exceeded"},
	}
	h := f.voter(t, 0, fetcher, 1)
	answer, err := h.VerifyChallenge(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, answer.Pending)

	var kept ChallengeAnswer
	require.Eventually(t, func() bool {
		kept, err = h.VerifyChallenge(context.Background(), 1)
		return err == nil && !kept.Pending
	}, 3*time.Second, 20*time.Millisecond, "the answer settles once the fetch is cut")
	require.Equal(t, ChallengeRejectUnchecked, kept.RejectCause, "a cut on this host's side is no vote against the executor")
	require.Empty(t, kept.Signature)
	close(fetcher.gate)
}

// The reasoning model whose answer can hold no content positions while it
// reports completion tokens is not voted EMPTY_STREAM for that: the gateway
// does not quarantine the executor for it and replay passes it as both
// empty, so the voter does the same. Zero claimed tokens with no positions
// is still an empty body, for that model too.
func TestVerifyChallenge_ModelBurnEmptyIsNoFault(t *testing.T) {
	withChallengeTiming(t, time.Millisecond, 5*time.Second)
	noPositions := []byte(`{"events":["data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"logprobs\":null}]}","data: [DONE]"]}`)
	for _, testCase := range []struct {
		name      string
		model     string
		output    uint64
		wantFault types.ChallengeFault
	}{
		{name: "kimi with claimed tokens", model: types.KimiK26ModelID, output: 40, wantFault: types.ChallengeFault_CHALLENGE_FAULT_NONE},
		{name: "kimi with no claimed tokens", model: types.KimiK26ModelID, output: 0, wantFault: types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM},
		{name: "another model with claimed tokens", model: "llama", output: 40, wantFault: types.ChallengeFault_CHALLENGE_FAULT_EMPTY_STREAM},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f := newChallengeFixtureFor(t, testCase.model, testCase.output)
			h := f.voter(t, 0, &scriptedChallengeFetcher{script: []devshard.ChallengeFetch{{Outcome: devshard.ChallengeFetchFetched, Response: noPositions}}}, 60)
			answer, err := h.VerifyChallenge(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, testCase.wantFault, answer.Fault)
			requireVoteSignedBy(t, f.hosts[0], answer)
		})
	}
}
