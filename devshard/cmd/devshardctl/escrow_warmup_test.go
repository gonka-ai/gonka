package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"common/completionapi"
	"devshard/user"

	"github.com/stretchr/testify/require"
)

type fakeProbeSender struct {
	groupSize  int
	nonce      uint64
	failSlots  map[int]bool
	sentNonces []uint64
	sentSlots  []int
	maxTokens  []uint64
	resolved   []uint64
	caughtUpAt []int
	catchUpErr error
}

func (f *fakeProbeSender) sendProbe(_ context.Context, params user.InferenceParams, nonceCommitted func()) (uint64, int, error) {
	f.nonce++
	slot := int(f.nonce % uint64(f.groupSize))
	f.sentNonces = append(f.sentNonces, f.nonce)
	f.sentSlots = append(f.sentSlots, slot)
	f.maxTokens = append(f.maxTokens, params.MaxTokens)
	if nonceCommitted != nil {
		nonceCommitted()
	}
	if f.failSlots[slot] {
		return f.nonce, slot, errors.New("host refused")
	}
	return f.nonce, slot, nil
}

func (f *fakeProbeSender) catchUpAllHosts(context.Context) error {
	f.caughtUpAt = append(f.caughtUpAt, len(f.sentNonces))
	return f.catchUpErr
}

func (f *fakeProbeSender) resolveUnserved(_ context.Context, nonce uint64) error {
	f.resolved = append(f.resolved, nonce)
	return nil
}

func warmupTestDeps(sender probeSender, recorder sendRecorder) warmupDeps {
	return warmupDeps{sender: sender, recorder: recorder, escrowID: "escrow-1", model: "test-model"}
}

func TestWarmupSpendsExactlyOneNonce(t *testing.T) {
	sender := &fakeProbeSender{groupSize: 16}

	warmEscrowHosts(context.Background(), warmupTestDeps(sender, nil), 0)

	require.Equal(t, []uint64{1}, sender.sentNonces,
		"catch-up reaches the whole group, so the group size must not cost the group size in nonces")
}

func TestEveryHostLearnsTheEscrowAfterTheProbe(t *testing.T) {
	sender := &fakeProbeSender{groupSize: 16}

	warmEscrowHosts(context.Background(), warmupTestDeps(sender, nil), 0)

	require.Equal(t, []int{1}, sender.caughtUpAt, "once, and only after a diff exists to replay")
}

func TestTheGroupLearnsTheEscrowEvenWhenTheProbeFailed(t *testing.T) {
	sender := &fakeProbeSender{groupSize: 16, failSlots: map[int]bool{1: true}}

	warmEscrowHosts(context.Background(), warmupTestDeps(sender, nil), 0)

	require.Equal(t, []int{1}, sender.caughtUpAt,
		"the probe persists its diff before the send, so a refusal still leaves catch-up something to replay")
}

func TestAFailedProbeIsResolvedOnlyAfterTheGroupKnowsTheEscrow(t *testing.T) {
	sender := &fakeProbeSender{groupSize: 16, failSlots: map[int]bool{1: true}}

	warmEscrowHosts(context.Background(), warmupTestDeps(sender, nil), 0)

	require.Equal(t, []uint64{1}, sender.resolved, "a nonce nobody served still has to be settled")
	require.NotEmpty(t, sender.caughtUpAt, "a timeout polls the group, so the group must know the escrow first")
}

func TestAFailedCatchUpDoesNotAbortTheWarmup(t *testing.T) {
	sender := &fakeProbeSender{groupSize: 16, catchUpErr: errors.New("every host is down")}
	recorder := &spyRecorder{}

	warmEscrowHosts(context.Background(), warmupTestDeps(sender, recorder), 0)

	require.Equal(t, []uint64{1}, recorder.served, "the host that answered still did the work")
}

func TestWarmupProbeDeclaresTheTokenFloor(t *testing.T) {
	sender := &fakeProbeSender{groupSize: 16}

	warmEscrowHosts(context.Background(), warmupTestDeps(sender, nil), 0)

	require.Equal(t, []uint64{completionapi.MinTokensFloor}, sender.maxTokens,
		"a probe below the floor is rejected by the executor and warms nothing")
	var body struct {
		MaxTokens uint64 `json:"max_tokens"`
	}
	require.NoError(t, json.Unmarshal(pocProbePromptBody, &body))
	require.Equal(t, uint64(completionapi.MinTokensFloor), body.MaxTokens,
		"the prompt the host validates carries the same floor as the declared params")
}

func TestWarmupSkipsAnEscrowThatAlreadyServed(t *testing.T) {
	for _, latestNonce := range []uint64{1, 917} {
		sender := &fakeProbeSender{groupSize: 16}

		warmEscrowHosts(context.Background(), warmupTestDeps(sender, nil), latestNonce)

		require.Empty(t, sender.sentNonces, "a recovered session must not spend a nonce re-teaching its hosts")
		require.Empty(t, sender.caughtUpAt)
	}
}

func TestWarmupIgnoresAMissingSender(t *testing.T) {
	require.NotPanics(t, func() {
		warmEscrowHosts(context.Background(), warmupTestDeps(nil, nil), 0)
	})
}

type spyRecorder struct {
	nonces []uint64
	served []uint64
}

func (s *spyRecorder) RealSend(_ string, nonce uint64, _ time.Time, _ string) {
	s.nonces = append(s.nonces, nonce)
}

func (s *spyRecorder) ProbeServed(_ string, nonce uint64, _ string) {
	s.served = append(s.served, nonce)
}

func TestWarmupReportsItsNonceToAccounting(t *testing.T) {
	sender := &fakeProbeSender{groupSize: 16}
	recorder := &spyRecorder{}

	warmEscrowHosts(context.Background(), warmupTestDeps(sender, recorder), 0)

	require.Equal(t, []uint64{1}, recorder.nonces,
		"a nonce the ledger never hears about reads as unclassified")
	require.Equal(t, []uint64{1}, recorder.served, "and a served probe settles as work nobody used")
}

func TestARefusedProbeIsNotSettledAsWork(t *testing.T) {
	sender := &fakeProbeSender{groupSize: 16, failSlots: map[int]bool{1: true}}
	recorder := &spyRecorder{}

	warmEscrowHosts(context.Background(), warmupTestDeps(sender, recorder), 0)

	require.Equal(t, []uint64{1}, recorder.nonces, "the nonce is spent either way")
	require.Empty(t, recorder.served, "but a host that refused delivered nothing")
}

type spyMetrics struct{ decisions []GatewaySlotDecisionMetric }

func (s *spyMetrics) RecordGatewaySlotDecision(decision GatewaySlotDecisionMetric) {
	s.decisions = append(s.decisions, decision)
}

func TestWarmupIsVisibleToTheDashboards(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		failSlots  map[int]bool
		wantReason string
	}{
		{name: "served", wantReason: warmupOutcomeServed},
		{name: "refused", failSlots: map[int]bool{1: true}, wantReason: warmupOutcomeFailed},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			sender := &fakeProbeSender{groupSize: 16, failSlots: testCase.failSlots}
			metrics := &spyMetrics{}
			deps := warmupTestDeps(sender, nil)
			deps.metrics = metrics
			deps.participant = func(hostIdx int) string { return "participant-1" }

			warmEscrowHosts(context.Background(), deps, 0)

			require.Len(t, metrics.decisions, 1)
			require.Equal(t, warmupDecision, metrics.decisions[0].Decision)
			require.Equal(t, testCase.wantReason, metrics.decisions[0].Reason)
			require.Equal(t, "participant-1", metrics.decisions[0].ParticipantKey,
				"a panel cannot find the host without its key")
			require.Equal(t, "test-model", metrics.decisions[0].Model)
			require.Equal(t, "escrow-1", metrics.decisions[0].EscrowID)
		})
	}
}

type blockingProbeSender struct {
	started chan struct{}
	once    sync.Once
	sent    atomic.Int64
}

func (b *blockingProbeSender) sendProbe(ctx context.Context, _ user.InferenceParams, nonceCommitted func()) (uint64, int, error) {
	b.once.Do(func() { close(b.started) })
	if nonceCommitted != nil {
		nonceCommitted()
	}
	b.sent.Add(1)
	<-ctx.Done()
	return 0, 0, ctx.Err()
}

func (b *blockingProbeSender) catchUpAllHosts(context.Context) error         { return nil }
func (b *blockingProbeSender) resolveUnserved(context.Context, uint64) error { return nil }

func TestRetiringAnEscrowStopsItsWarmup(t *testing.T) {
	rt := &devshardRuntime{id: "escrow-1", stopped: make(chan struct{})}
	sender := &blockingProbeSender{started: make(chan struct{})}
	done := make(chan struct{})

	go func() {
		defer close(done)
		warmUntilStopped(warmupDeps{sender: sender, escrowID: rt.id}, 0, rt.stopped)
	}()

	<-sender.started
	require.NoError(t, rt.close())
	require.NoError(t, rt.close(), "close runs on retire and again on shutdown")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("warmup outlived the escrow it belongs to")
	}
}

// The warmup once sat in attachRuntimeSharedState, which every restart calls for every runtime: a
// restart then re-warmed 15 live escrows at once. Creation is the only moment a group is cold.
func TestWarmupIsWiredOnlyToEscrowCreation(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	require.NoError(t, err)

	callers := map[string]int{}
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		body, readErr := os.ReadFile(source)
		require.NoError(t, readErr)
		enclosing := ""
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "func ") {
				enclosing = line
			}
			if strings.Contains(line, "startEscrowWarmup(") && !strings.HasPrefix(line, "func ") {
				callers[enclosing]++
			}
		}
	}

	require.Len(t, callers, 1, "exactly one caller, or a shared attach path can re-warm live escrows: %v", callers)
	for signature, count := range callers {
		require.Contains(t, signature, "addCreatedEscrowRuntime", "warmup must hang off escrow creation")
		require.Equal(t, 1, count)
	}
}

// Answers only once the catch-up has run, so a catch-up placed after the probe never sees one.
type probeAwaitingCatchUp struct {
	caughtUp   chan struct{}
	sawCatchUp atomic.Bool
}

func (p *probeAwaitingCatchUp) sendProbe(ctx context.Context, _ user.InferenceParams, nonceCommitted func()) (uint64, int, error) {
	nonceCommitted()
	select {
	case <-p.caughtUp:
		p.sawCatchUp.Store(true)
	case <-ctx.Done():
	}
	return 7, 3, nil
}

func (p *probeAwaitingCatchUp) catchUpAllHosts(context.Context) error {
	close(p.caughtUp)
	return nil
}

func (p *probeAwaitingCatchUp) resolveUnserved(context.Context, uint64) error { return nil }

func TestCatchUpReachesHostsBeforeTheProbeIsAnswered(t *testing.T) {
	ctx, cancelWarmup := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWarmup()
	sender := &probeAwaitingCatchUp{caughtUp: make(chan struct{})}

	warmEscrowHosts(ctx, warmupTestDeps(sender, nil), 0)

	require.True(t, sender.sawCatchUp.Load(),
		"the first request lands long before the probe's answer, so the group must be taught the escrow while the probe is still streaming")
}
