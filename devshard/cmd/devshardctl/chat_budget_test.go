package main

import (
	"reflect"
	"testing"
	"time"

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
