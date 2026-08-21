package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"devshard/accounting"
	"devshard/host"
	"devshard/transport"
	"devshard/types"
	"devshard/user"
)

// Both refusals arrive as the same status with the same two words. One is the host's build and waiting
// cannot fix it; the other is an escrow being torn down, which waiting does fix. Confusing them keeps a
// healthy host out of routing for the life of the process.
func TestOnlyAVersionRefusalIsPermanent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "the host's build is too old", body: `version "v3" not found`, want: true},
		{name: "the refusal as the host sends it, newline and all", body: "version \"v3\" not found\n", want: true},
		{name: "an escrow torn down mid-flight", body: `{"message":"get escrow: escrow not found"}`},
		{name: "some other quoted thing missing", body: `model "kimi" not found`},
		{name: "a quoted escrow missing", body: `escrow "49247" not found`},
		{name: "a plain missing route", body: `404 page not found`},
		{name: "both halves present but describing different things", body: `version "v4" active; model "kimi" not found`},
		{name: "the word version alone", body: `unsupported version`},
		{name: "nothing at all", body: ""},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := isVersionRefusal(testCase.body); got != testCase.want {
				t.Fatalf("isVersionRefusal(%q) = %v, want %v", testCase.body, got, testCase.want)
			}
		})
	}
}

// The nonce that met the refusal is charged as a miss through the ordinary timeout path, so the host
// keeps its place in the rota. Holding the refusal against later nonces charged a host thousands of
// misses for a build it had already replaced, since nothing ever re-tested it.
func TestARecordedVersionRefusalCountsWithoutWithholdingTheHost(t *testing.T) {
	t.Parallel()
	tracker := NewPerfTracker(nil)

	tracker.RecordVersionUnsupported("host-0")
	tracker.RecordVersionUnsupported("host-0")

	refusals, _, _, _ := tracker.CapabilityRefusals("host-0", "m")
	if refusals != 2 {
		t.Fatalf("version refusals = %d, want 2", refusals)
	}
	if other, _, _, _ := tracker.CapabilityRefusals("host-1", "m"); other != 0 {
		t.Error("the refusal of one host was counted against another")
	}
}

// The refusal only ever arrives on the dispatch error, so this is the one path by which a permanent
// incompatibility can reach the tracker that blocks routing.
func TestTheDispatchErrorIsWhatRecordsAVersionRefusal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "the host's build is too old",
			err:  &transport.UpstreamStatusError{Path: "/v1/chat/completions", StatusCode: 404, Body: `version "v3" not found`},
			want: true,
		},
		{
			name: "an escrow torn down mid-flight",
			err:  &transport.UpstreamStatusError{Path: "/v1/chat/completions", StatusCode: 500, Body: `{"message":"get escrow: escrow not found"}`},
		},
		{name: "a plain transport failure", err: errors.New("dial tcp: connection refused")},
		{name: "no failure at all"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			redundancy := &Redundancy{perf: NewPerfTracker(nil)}

			redundancy.maybeRecordVersionRefusal(&inflight{hostIdx: 3, err: testCase.err})

			blocked := false
			if refusals, _, _, _ := redundancy.perf.CapabilityRefusals(legacyHostPerfKey(3), ""); refusals > 0 {
				blocked = true
			}
			if blocked != testCase.want {
				t.Fatalf("host blocked = %v, want %v", blocked, testCase.want)
			}
		})
	}
}

// A probe is the gateway's own traffic, not a request, so its refusal says nothing about serving.
func TestAProbeRefusalIsNotRecorded(t *testing.T) {
	t.Parallel()
	redundancy := &Redundancy{perf: NewPerfTracker(nil)}

	redundancy.maybeRecordVersionRefusal(&inflight{
		hostIdx: 3, probe: true,
		err: &transport.UpstreamStatusError{StatusCode: 404, Body: `version "v3" not found`},
	})

	if refusals, _, _, _ := redundancy.perf.CapabilityRefusals(legacyHostPerfKey(3), ""); refusals > 0 {
		t.Fatal("a probe's refusal blocked the host")
	}
}

// The race outlives the client so its committed nonce can still be settled, so a winner can be crowned
// for a request nobody is left to receive. The work is the host's; calling the delivery real is not.
func TestAWinnerCrownedAfterTheClientLeftIsNamedAsUndelivered(t *testing.T) {
	t.Parallel()
	gone := newCancelFlag()
	gone.Trigger()

	cases := []struct {
		name       string
		nonce      uint64
		clientGone *cancelFlag
		want       string
	}{
		{name: "the winner, with the client still waiting", nonce: 7, clientGone: newCancelFlag()},
		{name: "the winner, with the client gone", nonce: 7, clientGone: gone, want: accounting.DeliveryClientGone},
		{name: "a loser, with the client gone", nonce: 8, clientGone: gone},
		{name: "no flag at all", nonce: 7},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := deliveryReasonFor(&inflight{nonce: testCase.nonce}, nil, 7, true, testCase.clientGone)

			if got != testCase.want {
				t.Fatalf("deliveryReasonFor() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// A vote can fail because the network blinked or because the hosts no longer hold the escrow. Only the
// second is permanent, and it is the one that leaves the nonce paying its full reserve at settlement.
func TestAGoneEscrowIsNamedApartFromACollectionFailure(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		result     user.TimeoutResult
		escrowGone bool
		wantAction string
		wantReason string
	}{
		{
			name:       "the hosts still hold the escrow",
			result:     user.TimeoutResult{Outcome: "vote_collection_failed"},
			wantAction: "failed", wantReason: "vote_collection_failed",
		},
		{
			name:       "the hosts have dropped the escrow",
			result:     user.TimeoutResult{Outcome: "vote_collection_failed"},
			escrowGone: true,
			wantAction: "failed", wantReason: string(accounting.TimeoutEscrowGone),
		},
		{
			name:       "a vote that reached the chain is untouched",
			result:     user.TimeoutResult{Applied: true, DetailReason: "none"},
			escrowGone: true,
			wantAction: "completed", wantReason: "none",
		},
		{
			name:       "a skip is untouched",
			result:     user.TimeoutResult{Outcome: "skipped", DetailReason: "nonce_already_finished"},
			escrowGone: true,
			wantAction: "skipped", wantReason: "nonce_already_finished",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			action, reason := gatewayTimeoutFailureAction(testCase.result, testCase.escrowGone)

			if action != testCase.wantAction || reason != testCase.wantReason {
				t.Fatalf("gatewayTimeoutFailureAction() = (%q, %q), want (%q, %q)",
					action, reason, testCase.wantAction, testCase.wantReason)
			}
		})
	}
}

// versionRefusingClient answers every dispatch the way a host too old for the escrow's protocol
// version does: the status carries the refusal, and no stream ever opens to carry an SSE error event.
type versionRefusingClient struct{}

func (versionRefusingClient) Send(
	context.Context, host.HostRequest, io.Writer, func(*host.HostResponse),
) (*host.HostResponse, error) {
	return nil, &transport.UpstreamStatusError{
		Path: "/v1/chat/completions", StatusCode: 404, Body: `version "v3" not found`,
	}
}

func (versionRefusingClient) VerifyTimeout(
	context.Context, uint64, types.TimeoutReason, *host.InferencePayload, []types.Diff,
) (bool, []byte, uint32, error) {
	return false, nil, 0, errors.New("verifier unavailable")
}

// The refusal reaches the tracker only from the dispatch path, so nothing short of a real dispatch
// proves the recording is wired at all. Routing must be unchanged by it: the refused nonce is the
// one that pays, and the next nonce goes to the same host as if nothing had been learned.
func TestARealDispatchRefusalIsCountedAndLeavesRoutingAlone(t *testing.T) {
	env := setupTestProxyWithClients(t, []user.HostClient{versionRefusingClient{}})
	env.proxy.redundancy.perf = NewPerfTracker(nil)
	participantKey := env.proxy.redundancy.participantKeyForHost(0)

	if refusals, _, _, _ := env.proxy.redundancy.perf.CapabilityRefusals(participantKey, ""); refusals != 0 {
		t.Fatal("a refusal was counted before the host had refused anything")
	}

	var buf bytes.Buffer
	if err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil); err == nil {
		t.Fatal("RunInference() error = nil, want the host's refusal")
	}

	refusals, _, _, _ := env.proxy.redundancy.perf.CapabilityRefusals(participantKey, "")
	if refusals == 0 {
		t.Fatal("a real dispatch refusal was not counted")
	}
	if reason, blocked := env.proxy.redundancy.capabilityBlocked(participantKey, defaultParams()); blocked {
		t.Fatalf("the host was withheld from routing after one refusal, reason = %q", reason)
	}
}

// Decode speed is the window after the first content chunk: charging the prompt this host read to how
// fast it writes would rank a host by prompt size.
func TestDecodeCostPerTokenMeasuresTheWindowAfterFirstContent(t *testing.T) {
	t.Parallel()
	firstContent := time.Unix(0, 1_000_000_000)

	cases := []struct {
		name         string
		tokens       int64
		firstContent time.Time
		lastChunk    time.Time
		want         time.Duration
	}{
		{
			name: "a hundred tokens over ten seconds", tokens: 100,
			firstContent: firstContent, lastChunk: firstContent.Add(10 * time.Second),
			want: 100 * time.Millisecond,
		},
		{name: "the host reported no tokens", firstContent: firstContent, lastChunk: firstContent.Add(time.Second)},
		{name: "content never arrived", tokens: 100, lastChunk: firstContent.Add(time.Second)},
		{name: "one chunk wide", tokens: 100, firstContent: firstContent, lastChunk: firstContent},
		{name: "the last chunk predates the first", tokens: 100, firstContent: firstContent, lastChunk: firstContent.Add(-time.Second)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			inf := &inflight{}
			inf.usageComplTokens.Store(testCase.tokens)
			if !testCase.firstContent.IsZero() {
				inf.firstContentNano.Store(testCase.firstContent.UnixNano())
			}
			if !testCase.lastChunk.IsZero() {
				inf.lastChunkAt.Store(testCase.lastChunk.UnixNano())
			}

			if got := decodeCostPerToken(inf); got != testCase.want {
				t.Fatalf("decodeCostPerToken() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestAVersionConflictCountsAsAVersionRefusal(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
		want bool
	}{
		{name: "route missing", body: `version "v4" not found`, want: true},
		{name: "stored under an older binary", body: `{"message":"session version conflict: stored v3, host v4"}`, want: true},
		{name: "unrelated failure", body: `{"message":"inference 8: expected started, got 0"}`, want: false},
		{name: "empty", body: "", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isVersionRefusal(testCase.body); got != testCase.want {
				t.Errorf("isVersionRefusal(%q) = %t, want %t: both refusals last until the host's binary changes",
					testCase.body, got, testCase.want)
			}
		})
	}
}
