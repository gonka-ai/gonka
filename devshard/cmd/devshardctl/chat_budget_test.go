package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/user"
)

type budgetedHostClient struct {
	user.HostClient
	wait time.Duration
}

func (client budgetedHostClient) ChatBudgetWait() time.Duration { return client.wait }

// Test flow:
//  1. Build the throttle for a group whose host A has chat budget, host B would wait for it longer than a request should wait, host E would wait a few milliseconds, host C sends chat over HTTP and host D is throttled by the limiter.
//  2. Ask whether each host is blocked.
//  3. Assert only host B and host D are blocked: a wait of a few milliseconds is left to the send rather than burning the nonce, and a longer one moves the request to a host with budget.
func TestAHostWithNoChatBudgetIsThrottled(t *testing.T) {
	clients := []user.HostClient{
		budgetedHostClient{wait: 0},
		budgetedHostClient{wait: chatBudgetSpentWait + time.Second},
		struct{ user.HostClient }{},
		budgetedHostClient{wait: 0},
		budgetedHostClient{wait: 50 * time.Millisecond},
	}
	keys := []string{"hostA", "hostB", "hostC", "hostD", "hostE"}
	limiterBlocked := func(participant string) bool { return participant == "hostD" }

	blocked := throttledOrOutOfChatBudget(clients, keys, limiterBlocked)
	verdicts := []bool{blocked("hostA"), blocked("hostB"), blocked("hostC"), blocked("hostD"), blocked("hostE")}

	if !reflect.DeepEqual(verdicts, []bool{false, true, false, true, false}) {
		t.Fatalf("blocked(hostA, hostB, hostC, hostD, hostE) = %v, want [false true false true false]", verdicts)
	}
}

type oneHostDownBridge struct {
	stubMainnetBridge
	down      string
	lookupErr error
}

func (b *oneHostDownBridge) GetHostInfo(address string) (*bridge.HostInfo, error) {
	if address == b.down {
		return nil, fmt.Errorf("Participant %s: %w", address, b.lookupErr)
	}
	return &bridge.HostInfo{Address: address, URL: "http://127.0.0.1:1"}, nil
}

// Test flow:
//  1. Open a session whose second host's URL lookup fails on the chain, or finds no such participant.
//  2. Build the chat throttle from the session's clients.
//  3. Assert the session opened and only the unresolved host is blocked, so no start is signed for a host the gateway cannot reach.
func TestAHostWhoseURLIsUnresolvedIsThrottled(t *testing.T) {
	cases := []struct {
		name      string
		lookupErr error
	}{
		{name: "chain unavailable", lookupErr: bridge.ErrChainUnavailable},
		{name: "participant absent from the chain", lookupErr: bridge.ErrParticipantNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			const hostA, hostB = "host-budget-a", "host-budget-b"
			session, _, err := user.NewHTTPSession(user.HTTPSessionConfig{
				PrivateKeyHex: "0000000000000000000000000000000000000000000000000000000000000001",
				EscrowID:      "9801",
				Bridge:        &oneHostDownBridge{down: hostB, lookupErr: testCase.lookupErr},
				Escrow: &bridge.EscrowInfo{
					EscrowID: "9801", Amount: 1_000_000, CreatorAddress: "creator",
					Slots: []string{hostA, hostB}, TokenPrice: 1, EpochID: 7,
				},
				StoragePath: filepath.Join(t.TempDir(), "session"),
				RoutePrefix: "/devshard/dev",
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = session.Close() })

			blocked := throttledOrOutOfChatBudget(session.Clients(), session.HostParticipantKeyList(), nil)
			require.False(t, blocked(hostA))
			require.True(t, blocked(hostB))
		})
	}
}
