package poc

import (
	"context"
	"testing"
	"time"
)

func TestRetryBackoffDelay(t *testing.T) {
	base := 3 * time.Second
	want := []time.Duration{
		3 * time.Second,
		4500 * time.Millisecond,
		6750 * time.Millisecond,
		10125 * time.Millisecond,
		15187500 * time.Microsecond,
		22781250 * time.Microsecond,
		34171875 * time.Microsecond,
		45 * time.Second,
		45 * time.Second,
	}
	for attempt, expected := range want {
		if got := retryBackoffDelay(base, attempt); got != expected {
			t.Fatalf("attempt %d: delay = %v, want %v", attempt, got, expected)
		}
	}

	var total time.Duration
	for attempt := 0; attempt < DefaultValidationConfig().MaxRetries-1; attempt++ {
		total += retryBackoffDelay(base, attempt)
	}
	if total < 14*time.Minute || total > 15*time.Minute {
		t.Fatalf("total retry window = %v, want about 15m", total)
	}
}

func TestRetryQueueWaitingItemDoesNotBlockReadyItems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workChan := make(chan participantWork, 8)
	waiting := participantWork{address: "waiting", retryAfter: time.Now().Add(time.Hour)}
	ready := []participantWork{
		{address: "ready-1"},
		{address: "ready-2"},
		{address: "ready-3"},
	}
	workChan <- waiting
	for _, item := range ready {
		workChan <- item
	}

	processed := make(chan string, len(ready))
	go func() {
		for len(processed) < len(ready) {
			select {
			case work := <-workChan:
				if time.Now().Before(work.retryAfter) {
					select {
					case workChan <- work:
					case <-ctx.Done():
						return
					}
					time.Sleep(10 * time.Millisecond)
					continue
				}
				processed <- work.address
			case <-ctx.Done():
				return
			}
		}
	}()

	deadline := time.After(200 * time.Millisecond)
	seen := map[string]bool{}
	for len(seen) < len(ready) {
		select {
		case addr := <-processed:
			seen[addr] = true
		case <-deadline:
			t.Fatalf("ready items were starved by waiting item; seen=%v", seen)
		}
	}
}
