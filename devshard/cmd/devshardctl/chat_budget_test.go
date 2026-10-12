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
//  1. Build the throttle for a group whose host A has chat budget, host B would wait for it, host C sends chat over HTTP and host D is throttled by the limiter.
//  2. Ask whether each host is blocked.
//  3. Assert host B and host D are blocked, and host A and host C are not.
func TestAHostWithNoChatBudgetIsThrottled(t *testing.T) {
	clients := []user.HostClient{
		budgetedHostClient{wait: 0},
		budgetedHostClient{wait: 50 * time.Millisecond},
		struct{ user.HostClient }{},
		budgetedHostClient{wait: 0},
	}
	keys := []string{"hostA", "hostB", "hostC", "hostD"}
	limiterBlocked := func(participant string) bool { return participant == "hostD" }

	blocked := throttledOrOutOfChatBudget(clients, keys, limiterBlocked)
	verdicts := []bool{blocked("hostA"), blocked("hostB"), blocked("hostC"), blocked("hostD")}

	if !reflect.DeepEqual(verdicts, []bool{false, true, false, true}) {
		t.Fatalf("blocked(hostA, hostB, hostC, hostD) = %v, want [false true false true]", verdicts)
	}
}
