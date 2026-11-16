package scheduler

import (
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/tokensource"
)

type recordingProber struct {
	mu       sync.Mutex
	requests []ProbeRequest
	triggers []any
	profiles []*RequestProfile
	refuse   bool
	skipWith ProbeSkip
}

func (p *recordingProber) Params(request *RequestProfile, startedAt int64) (any, ProbeSkip) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var params any
	if request != nil {
		params = request.Params
	}
	p.triggers = append(p.triggers, params)
	p.profiles = append(p.profiles, request)
	if p.skipWith != ProbeSkipNone {
		return nil, p.skipWith
	}
	return [2]any{params, startedAt}, ProbeSkipNone
}

func (p *recordingProber) triggered() []any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]any(nil), p.triggers...)
}

func (p *recordingProber) Probe(request ProbeRequest) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	return !p.refuse
}

func (p *recordingProber) probed() []ProbeRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ProbeRequest(nil), p.requests...)
}

func holderOf(participants config.Participants) *config.Holder {
	configuration := config.Defaults()
	configuration.Scheduler.Participants = participants
	holder := &config.Holder{}
	holder.Swap(&configuration)
	return holder
}

func gatesFor(participants config.Participants) availability {
	scheduler := &Scheduler{settings: holderOf(participants), limiter: newFakeLimiter(), perf: &fakePerf{ejected: map[string]bool{}}}
	return scheduler.fleetGates(modelA, chain.PhaseSnapshot{})
}

func countingHold(released *atomic.Int64) func() (func(), bool) {
	return func() (func(), bool) { return func() { released.Add(1) }, true }
}

func shadowing(participants ...string) func(string) bool {
	return func(participant string) bool { return slices.Contains(participants, participant) }
}

// Test flow:
//  1. Build a harness of `hostA` and `hostB` where `hostB` is shadowed, with a recording prober.
//  2. Submit one request and await its reply.
//  3. Assert the request is served by `hostA` at nonce 2, and nonce 1, bound to `hostB`, was committed as a real probe with the prober's params rather than a ghost.
//  4. Assert the prober received that nonce for `hostB` on this escrow, and no ghost burn was recorded.
func TestShadowHostsNonceIsSpentOnAProbeNotAGhost(t *testing.T) {
	prober := &recordingProber{}
	test := newHarness(t, harnessConfig{shadowed: shadowing(hostB), prober: prober})

	queued := test.submit(t, test.clock.Now())

	wantAssignment(t, awaitReply(t, queued), hostA, 2)
	test.dispatcher.stop()

	_, _, commits := test.session.report()
	if len(commits) != 2 || commits[0].ghost || commits[0].participant != hostB || commits[0].params != any([2]any{"payload", test.clock.Now().Unix()}) {
		t.Fatalf("commits = %+v, want a probe for %s built from the queued request, then the served nonce", commits, hostB)
	}
	probes := prober.probed()
	if len(probes) != 1 || probes[0].Assignment.Host != hostB || probes[0].Assignment.Nonce.Nonce() != 1 || probes[0].Assignment.Escrow != escrowA || probes[0].Model != modelA {
		t.Fatalf("probes = %+v, want one probe of nonce 1 for %s on %s", probes, hostB, escrowA)
	}
	if burns := test.observer.burns(); len(burns) != 0 {
		t.Fatalf("ghost burns = %v, want none: the nonce went to a probe", burns)
	}
}

// Test flow:
//  1. Build a harness where `hostB` is shadowed and no prober is wired.
//  2. Submit one request and await its reply.
//  3. Assert the request is served by `hostA` and the shadowed nonce was burned as a ghost outside the trust list, so a shadow host is never routed client traffic.
func TestShadowHostWithoutAProberBurnsAsOutsideTheTrustList(t *testing.T) {
	test := newHarness(t, harnessConfig{shadowed: shadowing(hostB)})

	queued := test.submit(t, test.clock.Now())

	wantAssignment(t, awaitReply(t, queued), hostA, 2)
	test.dispatcher.stop()

	if burns := test.observer.burns(); len(burns) != 1 || burns[0] != ghostUntrusted.reason() {
		t.Fatalf("ghost burns = %v, want exactly one %q", burns, ghostUntrusted.reason())
	}
}

// Test flow:
//  1. Build a harness where `hostB` is shadowed and the prober refuses every probe, with an escrow hold that counts its releases.
//  2. Submit one request and await its reply.
//  3. Assert the refused nonce is burned as a ghost outside the trust list and the request still lands on `hostA`.
//  4. Assert every hold taken was released by the dispatcher, since the prober took over none.
func TestRefusedProbeIsBurnedAndItsHoldReleased(t *testing.T) {
	var released atomic.Int64
	prober := &recordingProber{refuse: true}
	test := newHarness(t, harnessConfig{shadowed: shadowing(hostB), prober: prober, escrowHold: countingHold(&released)})

	queued := test.submit(t, test.clock.Now())

	wantAssignment(t, awaitReply(t, queued), hostA, 2)
	result := awaitReply(t, test.submit(t, test.clock.Now()))
	test.dispatcher.stop()

	if burns := test.observer.burns(); len(burns) == 0 || burns[0] != ghostUntrusted.reason() {
		t.Fatalf("ghost burns = %v, want the refused probe burned as %q", burns, ghostUntrusted.reason())
	}
	if result.err != nil {
		t.Fatalf("second request err = %v, want an assignment", result.err)
	}
	if got := released.Load(); got == 0 {
		t.Fatalf("holds released = %d, want the refused probe's hold given back", got)
	}
}

// Test flow:
//  1. Build a harness where the prober accepts the probe, with an escrow hold that counts its releases.
//  2. Submit one request, await its reply and stop the dispatcher.
//  3. Assert the dispatcher released no hold for the probe it handed over: the assignment carries it, and the prober releases it.
func TestAcceptedProbeKeepsItsHoldForTheProber(t *testing.T) {
	var released atomic.Int64
	prober := &recordingProber{}
	test := newHarness(t, harnessConfig{shadowed: shadowing(hostB), prober: prober, escrowHold: countingHold(&released)})

	queued := test.submit(t, test.clock.Now())
	result := awaitReply(t, queued)
	test.dispatcher.stop()

	if result.err != nil {
		t.Fatalf("reply err = %v, want an assignment", result.err)
	}
	probes := prober.probed()
	if len(probes) != 1 || probes[0].Assignment.EscrowHold == nil {
		t.Fatalf("probes = %+v, want one probe carrying the escrow hold", probes)
	}
	if got := released.Load(); got != 0 {
		t.Fatalf("holds released = %d, want only the served nonce's hold to be in flight, none given back", got)
	}
}

// Test flow:
//  1. Build a harness whose only host is shadowed, with a recording prober.
//  2. Submit one request and await its reply.
//  3. Assert the reply is `ErrNoAvailableHost` and nothing was committed or probed, so a shadow host never serves a client.
func TestRequestIsNeverServedByAShadowHost(t *testing.T) {
	prober := &recordingProber{}
	test := newHarness(t, harnessConfig{slots: []string{hostA}, shadowed: shadowing(hostA), prober: prober})

	queued := test.submit(t, test.clock.Now())

	if result := awaitReply(t, queued); !errors.Is(result.err, ErrNoAvailableHost) {
		t.Fatalf("err = %v, want ErrNoAvailableHost", result.err)
	}
	test.dispatcher.stop()

	_, _, commits := test.session.report()
	if len(commits) != 0 || len(prober.probed()) != 0 {
		t.Fatalf("commits = %+v, probes = %+v, want neither", commits, prober.probed())
	}
}

// Test flow:
//  1. Build a `laddered` availability for a shadowed participant whose window is full, who is cut off and who is ejected.
//  2. Assert the reason is `blockShadow`: congestion, cut-off and ejection do not apply to a probe.
//  3. Assert `match` answers a probe decision, not a burn.
func TestShadowHostSkipsCongestionCutOffAndEjection(t *testing.T) {
	t.Parallel()
	gates := laddered(func(gates *availability) {
		gates.shadowed = always(true)
		gates.congested = func(string) blockReason { return blockCutOff }
		gates.ejected = always(true)
		gates.budgetSpent = always(true)
	})

	if reason := gates.participantBlocked(hostA); reason != blockShadow {
		t.Fatalf("reason = %v, want blockShadow", reason)
	}
	if _, probing := match(HostBinding{Participant: hostA}, nil, []string{hostA}, gates, time.Time{}, 0).(probe); !probing {
		t.Error("a shadow host's nonce must be matched to a probe, not a burn")
	}
}

// Test flow:
//  1. For each table case (absent from the escrow's session, owes proof-of-compute, escrow state diverged), close that one gate on a `laddered` availability for a shadowed participant.
//  2. Ask whether the participant is blocked.
//  3. Assert the reason is the case's own block, since a probe cannot be served there.
func TestShadowHostStillBurnsWhereAProbeCannotBeServed(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		close func(*availability)
		want  blockReason
	}{
		"absent from the session": {func(gates *availability) { gates.absent = always(true) }, blockAbsentFromChain},
		"owes proof of compute":   {func(gates *availability) { gates.pocRequired = always(true) }, blockPoCRequired},
		"escrow state diverged":   {func(gates *availability) { gates.stateBlocked = always(true) }, blockStateDiverged},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			gates := laddered(func(gates *availability) {
				gates.shadowed = always(true)
				tc.close(gates)
			})

			if reason := gates.participantBlocked(hostA); reason != tc.want {
				t.Errorf("reason = %v, want %v", reason, tc.want)
			}
		})
	}
}

// Test flow:
//  1. Build a `laddered` availability where the participant is shadowed and also outside a non-empty trust list.
//  2. Assert the reason is `blockShadow`, not `blockUntrusted`: shadow replaces the untrusted burn.
func TestShadowReplacesTheUntrustedBurn(t *testing.T) {
	t.Parallel()
	gates := laddered(func(gates *availability) {
		gates.shadowed = always(true)
		gates.untrusted = always(true)
	})

	if reason := gates.participantBlocked(hostA); reason != blockShadow {
		t.Fatalf("reason = %v, want blockShadow", reason)
	}
}

// Test flow:
//  1. Build a scheduler whose settings name `hostA` shadow and leave trust empty.
//  2. Read the fleet gates for a model.
//  3. Assert `hostA` is `blockShadow` and `hostB` is open: an empty trust list with a shadow list routes everybody but the shadow members.
func TestEmptyTrustWithShadowRoutesEveryoneElse(t *testing.T) {
	t.Parallel()
	gates := gatesFor(config.Participants{Shadow: []string{hostA}})

	if reason := gates.participantBlocked(hostA); reason != blockShadow {
		t.Errorf("shadow host: reason = %v, want blockShadow", reason)
	}
	if reason := gates.participantBlocked(hostB); reason != blockNone {
		t.Errorf("other host: reason = %v, want blockNone", reason)
	}
}

// Test flow:
//  1. Build a scheduler whose settings name `hostA` trust and `hostB` shadow.
//  2. Read the fleet gates for a model.
//  3. Assert `hostA` is open, `hostB` is `blockShadow` and a stranger is `blockUntrusted`.
func TestTrustAndShadowTogetherNarrowRoutingAndProbeTheShadowHost(t *testing.T) {
	t.Parallel()
	gates := gatesFor(config.Participants{Trust: []string{hostA}, Shadow: []string{hostB}})

	for participant, want := range map[string]blockReason{hostA: blockNone, hostB: blockShadow, "stranger": blockUntrusted} {
		if reason := gates.participantBlocked(participant); reason != want {
			t.Errorf("%s: reason = %v, want %v", participant, reason, want)
		}
	}
}

// Test flow:
//  1. Build reachability checks for an empty trust list with `hostA` shadow, and for a trust list naming `hostA` with `hostB` shadow.
//  2. Assert an escrow holding only a shadow participant is unreachable and one holding any other participant is reachable.
func TestEscrowHoldingOnlyShadowParticipantsIsUnreachable(t *testing.T) {
	t.Parallel()
	soleShadow := Escrow{Session: &fakeSession{slots: []string{hostA}, participants: []string{hostA}}}
	shadowAndOther := Escrow{Session: &fakeSession{slots: []string{hostA, hostB}, participants: []string{hostA, hostB}}}

	shadowOnly := reachableByTrust(config.Participants{Shadow: []string{hostA}})
	if shadowOnly(soleShadow) || !shadowOnly(shadowAndOther) {
		t.Errorf("empty trust: sole shadow reachable = %v, shadow and other reachable = %v, want false and true", shadowOnly(soleShadow), shadowOnly(shadowAndOther))
	}
	trusted := reachableByTrust(config.Participants{Trust: []string{hostA}, Shadow: []string{hostB}})
	if !trusted(soleShadow) || trusted(Escrow{Session: &fakeSession{slots: []string{hostB}, participants: []string{hostB}}}) {
		t.Error("with trust named, an escrow is reachable through its trusted participant alone, never through its shadow one")
	}
}

// Test flow:
//  1. Build a harness with `hostB` shadowed and a recording prober, submit one request and await its reply.
//  2. Assert the prober was handed the queued request's own params as the request to repeat.
func TestProbeIsBuiltFromTheQueuedRequest(t *testing.T) {
	prober := &recordingProber{}
	test := newHarness(t, harnessConfig{shadowed: shadowing(hostB), prober: prober})

	queued := test.submit(t, test.clock.Now())
	wantAssignment(t, awaitReply(t, queued), hostA, 2)
	test.dispatcher.stop()

	if triggers := prober.triggered(); len(triggers) != 1 || triggers[0] != any("payload") {
		t.Fatalf("triggers = %v, want the queued request's params once", triggers)
	}
}

// Test flow:
//  1. Run the same request through a harness without a shadow host and through one with `hostB` shadowed, and read the commits of each.
//  2. Assert the client's own nonce carries the request's params in both, so the probe changes nothing the client is charged for.
//  3. Assert the probe's nonce carries the prober's params and is a real dispatch rather than a ghost.
func TestProbeLeavesTheClientsCommitUnchanged(t *testing.T) {
	plain := newHarness(t, harnessConfig{})
	wantAssignment(t, awaitReply(t, plain.submit(t, plain.clock.Now())), hostB, 1)
	plain.dispatcher.stop()
	_, _, plainCommits := plain.session.report()

	shadowed := newHarness(t, harnessConfig{shadowed: shadowing(hostB), prober: &recordingProber{}})
	wantAssignment(t, awaitReply(t, shadowed.submit(t, shadowed.clock.Now())), hostA, 2)
	shadowed.dispatcher.stop()
	_, _, shadowedCommits := shadowed.session.report()

	if len(plainCommits) != 1 || len(shadowedCommits) != 2 {
		t.Fatalf("commits = %+v and %+v, want one and two", plainCommits, shadowedCommits)
	}
	if plainCommits[0].params != shadowedCommits[1].params || plainCommits[0].ghost != shadowedCommits[1].ghost {
		t.Errorf("client commit = %+v with a probe, %+v without, want them equal", shadowedCommits[1], plainCommits[0])
	}
	if shadowedCommits[0].ghost || shadowedCommits[0].params == shadowedCommits[1].params {
		t.Errorf("probe commit = %+v, want a real dispatch with params of its own", shadowedCommits[0])
	}
}

// Test flow:
//  1. For each skip a prober can answer, build a harness with `hostB` shadowed and a prober that refuses to build the probe for it.
//  2. Submit one request and await its reply.
//  3. Assert the request still lands on `hostA`, the shadow nonce is burned under the skip's own reason, and nothing is probed.
func TestSkippedProbeIsBurnedUnderItsOwnReason(t *testing.T) {
	cases := map[string]struct {
		skip   ProbeSkip
		reason string
	}{
		"grammar constrained": {ProbeSkipGrammarConstrained, GhostReasonProbeGrammarConstrained},
		"no request":          {ProbeSkipNoRequest, GhostReasonProbeNoRequest},
		"no room":             {ProbeSkipNoRoom, GhostReasonProbeNoRoom},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			prober := &recordingProber{skipWith: tc.skip}
			test := newHarness(t, harnessConfig{shadowed: shadowing(hostB), prober: prober})

			wantAssignment(t, awaitReply(t, test.submit(t, test.clock.Now())), hostA, 2)
			test.dispatcher.stop()

			if burns := test.observer.burns(); len(burns) != 1 || burns[0] != tc.reason {
				t.Errorf("ghost burns = %v, want exactly one %q", burns, tc.reason)
			}
			if probes := prober.probed(); len(probes) != 0 {
				t.Errorf("probes = %+v, want none for a skipped probe", probes)
			}
		})
	}
}

// Test flow:
//  1. Build a dispatcher whose queue holds an abandoned request followed by two live ones, with a recording prober.
//  2. Ask for the shadow decision.
//  3. Assert the prober was handed the first live request's params, not the abandoned one's nor the last one's.
func TestShadowDecisionRepeatsTheOldestLiveRequest(t *testing.T) {
	t.Parallel()
	prober := &recordingProber{}
	gone := newWaiter(RequestProfile{Params: "gone"}, time.Time{})
	gone.abandoned.Store(true)
	queue := &dispatcher{dispatcherDeps: dispatcherDeps{shadow: prober, now: time.Now}, waiting: []*waiter{gone, newWaiter(RequestProfile{Params: "oldest"}, time.Time{}), newWaiter(RequestProfile{Params: "newest"}, time.Time{})}}

	queue.shadowDecision()

	if triggers := prober.triggered(); len(triggers) != 1 || triggers[0] != any("oldest") {
		t.Errorf("triggers = %v, want the oldest live request's params once", triggers)
	}
}

// Test flow:
//  1. Build a dispatcher whose only queued request is abandoned, with a recording prober.
//  2. Ask for the shadow decision.
//  3. Assert the prober was handed no request and the decision is the burn for a probe with no request to repeat.
func TestShadowDecisionWithoutALiveRequestBurnsAsNoRequest(t *testing.T) {
	t.Parallel()
	prober := &recordingProber{skipWith: ProbeSkipNoRequest}
	gone := newWaiter(RequestProfile{Params: "payload"}, time.Time{})
	gone.abandoned.Store(true)
	queue := &dispatcher{dispatcherDeps: dispatcherDeps{shadow: prober, now: time.Now}, waiting: []*waiter{gone}}

	decision := queue.shadowDecision()

	if triggers := prober.triggered(); len(triggers) != 1 || triggers[0] != nil {
		t.Errorf("triggers = %v, want a single nil request", triggers)
	}
	if burned, isBurn := decision.(burn); !isBurn || burned.kind != ghostProbeNoRequest {
		t.Errorf("decision = %#v, want a burn of kind ghostProbeNoRequest", decision)
	}
}

// Test flow:
//  1. Build a dispatcher whose queue holds one live request with a prompt token count, a source and a model, with a recording prober.
//  2. Ask for the shadow decision.
//  3. Assert the prober was handed that request's whole profile, so it can read the prompt's size.
func TestShadowDecisionHandsTheProberTheQueuedProfile(t *testing.T) {
	t.Parallel()
	prober := &recordingProber{}
	queued := RequestProfile{Model: "model-a", InputTokens: 777, InputTokensSource: tokensource.TokenizerExact, Params: "oldest"}
	queue := &dispatcher{dispatcherDeps: dispatcherDeps{shadow: prober, now: time.Now}, waiting: []*waiter{newWaiter(queued, time.Time{})}}

	queue.shadowDecision()

	if len(prober.profiles) != 1 || prober.profiles[0] == nil || prober.profiles[0].InputTokens != 777 || prober.profiles[0].Model != "model-a" || prober.profiles[0].InputTokensSource != tokensource.TokenizerExact {
		t.Errorf("profiles = %+v, want the queued request's profile once", prober.profiles)
	}
}
