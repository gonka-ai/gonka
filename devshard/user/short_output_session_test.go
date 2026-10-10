package user

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"devshard"
	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

// shortOutputEngine is an executor engine that records the max_tokens it runs and answers with
// however few tokens the model stops at.
type shortOutputEngine struct {
	*stub.InferenceEngine
	mu        sync.Mutex
	maxTokens []uint64
}

func newShortOutputEngine(outputTokens uint64) *shortOutputEngine {
	engine := &shortOutputEngine{InferenceEngine: stub.NewInferenceEngine()}
	engine.OutputTokens = outputTokens
	return engine
}

func (e *shortOutputEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	e.mu.Lock()
	e.maxTokens = append(e.maxTokens, req.MaxTokens)
	e.mu.Unlock()
	return e.InferenceEngine.Execute(ctx, req)
}

type countingValidator struct {
	mu    sync.Mutex
	calls []devshard.ValidateRequest
}

func (v *countingValidator) Validate(_ context.Context, req devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, req)
	return &devshard.ValidateResult{Valid: true}, nil
}

func (v *countingValidator) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.calls)
}

// shortPrompt is a chat body whose own max_tokens is the short reservation it is sent under.
func shortPrompt(maxTokens uint64) []byte {
	return []byte(fmt.Sprintf(`{"model":"llama","messages":[{"role":"user","content":"call the weather tool"}],"tools":[{"type":"function","function":{"name":"weather"}}],"max_tokens":%d}`, maxTokens))
}

type shortOutputStack struct {
	session    *Session
	hostSMs    []*state.StateMachine
	engines    []devshard.InferenceEngine
	validators []*countingValidator
}

func newShortOutputStack(t *testing.T, engines func(i int) devshard.InferenceEngine) shortOutputStack {
	t.Helper()
	const numHosts = 3
	const balance = 1_000_000
	hosts := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hosts {
		hosts[i] = testutil.MustGenerateKey(t)
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{RefusalTimeout: 60, ExecutionTimeout: 1200, TokenPrice: 1, VoteThreshold: 1, ValidationRate: 20000} // every non-executor host samples every inference
	verifier := signing.NewSecp256k1Verifier()

	stack := shortOutputStack{}
	clients := make([]HostClient, numHosts)
	for i := range hosts {
		sm, err := state.NewStateMachine("escrow-short", config, group, balance, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-short", user.Address(), config, group, balance))
		require.NoError(t, err)
		validator := &countingValidator{}
		engine := engines(i)
		h, err := host.NewHost(sm, hosts[i], engine, "escrow-short", group, nil, host.WithGrace(100), host.WithValidator(validator))
		require.NoError(t, err)
		h.Start()
		t.Cleanup(h.Close)
		clients[i] = &InProcessClient{Host: h}
		stack.hostSMs = append(stack.hostSMs, sm)
		stack.engines = append(stack.engines, engine)
		stack.validators = append(stack.validators, validator)
	}

	userSM, err := state.NewStateMachine("escrow-short", config, group, balance, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-short", user.Address(), config, group, balance))
	require.NoError(t, err)
	stack.session, err = NewSession(userSM, user, "escrow-short", group, clients, verifier)
	require.NoError(t, err)
	return stack
}

func shortParams(maxTokens uint64, startedAt int64) InferenceParams {
	prompt := shortPrompt(maxTokens)
	return InferenceParams{Model: "llama", Prompt: prompt, InputLength: uint64(len(prompt)), MaxTokens: maxTokens, StartedAt: startedAt}
}

// End to end through gateway session, hosts, executor and validators: a tool-call style request
// with max_tokens below 64 is reserved at its own budget, executed without being raised to 64,
// finished with its short output and validated.
func TestShortOutputSession_ShortBudgetReservedExecutedValidated(t *testing.T) {
	const maxTokens, outputTokens = 8, 3
	stack := newShortOutputStack(t, func(int) devshard.InferenceEngine {
		return newShortOutputEngine(outputTokens)
	})
	ctx := context.Background()

	result, err := stack.session.SendInference(ctx, shortParams(maxTokens, 1000))
	require.NoError(t, err)
	require.NotNil(t, result.Receipt, "the executor signs a receipt for the short reservation")
	start := stack.session.Diffs()[0].Txs[0].GetStartInference()
	require.NotNil(t, start)
	require.EqualValues(t, maxTokens, start.MaxTokens)

	deadline := time.Now().Add(20 * time.Second)
	for {
		require.NoError(t, stack.session.SendPendingDiff(ctx))
		rec := stack.session.StateMachine().ExportAllInferenceRecords()[1]
		if rec.Status == types.StatusValidated || rec.Status == types.StatusFinished && validatorCalls(stack) > 0 {
			break
		}
		require.False(t, time.Now().After(deadline), "inference never finished and validated: status %d", rec.Status)
		time.Sleep(20 * time.Millisecond)
	}

	rec := stack.session.StateMachine().ExportAllInferenceRecords()[1]
	require.EqualValues(t, maxTokens, rec.MaxTokens)
	require.EqualValues(t, rec.InputLength+maxTokens, rec.ReservedCost, "reserved at the caller's budget, not 64")
	require.EqualValues(t, outputTokens, rec.OutputTokens, "the short answer is not padded")
	require.Positive(t, validatorCalls(stack))
	executed := 0
	for _, engine := range stack.engines {
		e := engine.(*shortOutputEngine)
		e.mu.Lock()
		for _, got := range e.maxTokens {
			require.EqualValues(t, maxTokens, got)
			executed++
		}
		e.mu.Unlock()
	}
	require.Equal(t, 1, executed)
	gatewayRoot, err := stack.session.StateMachine().ComputeStateRoot()
	require.NoError(t, err)
	for i, sm := range stack.hostSMs {
		if sm.SnapshotState().LatestNonce != stack.session.Nonce() {
			continue
		}
		root, err := sm.ComputeStateRoot()
		require.NoError(t, err)
		require.Equal(t, gatewayRoot, root, "host %d", i)
	}
}

func validatorCalls(stack shortOutputStack) int {
	calls := 0
	for _, v := range stack.validators {
		calls += v.count()
	}
	return calls
}
