package scheduler

import "testing"

// Test flow:
//  1. For a waiter delivered an assignment before it abandons, assert the delivery succeeds and the abandon call reports that same assignment.
//  2. For a waiter that abandons before any delivery, assert the abandon call reports nothing and a later delivery attempt fails.
func TestWaiterHandsOffToExactlyOneSide(t *testing.T) {
	t.Run("a caller that leaves after the handoff takes the assignment with it", func(t *testing.T) {
		queued := newWaiter(RequestProfile{Model: modelA}, baseTime)
		assignment := Assignment{Escrow: escrowA, Host: hostA}

		if !queued.deliver(pickResult{assignment: assignment}) {
			t.Fatal("deliver to a live waiter = false, want true")
		}
		delivered, wasDelivered := queued.abandon()

		if !wasDelivered || delivered.assignment.Escrow != assignment.Escrow || delivered.assignment.Host != assignment.Host {
			t.Fatalf("abandon = %+v/%v, want the assignment already handed over", delivered, wasDelivered)
		}
	})

	t.Run("a caller that leaves first refuses the handoff", func(t *testing.T) {
		queued := newWaiter(RequestProfile{Model: modelA}, baseTime)

		if _, wasDelivered := queued.abandon(); wasDelivered {
			t.Fatal("abandon on an unanswered waiter reported a reply")
		}

		if queued.deliver(pickResult{assignment: Assignment{Escrow: escrowA, Host: hostA}}) {
			t.Fatal("deliver to an abandoned waiter = true, want false")
		}
	})
}
