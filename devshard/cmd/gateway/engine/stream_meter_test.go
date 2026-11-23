package engine

import (
	"testing"
	"time"
)

// Test flow:
//  1. Build a meter on a clock the test advances, let 30 s pass as a catch-up, and mark the send.
//  2. Advance the clock 2 s and stamp the receipt.
//  3. Assert the outcome's send-to-receipt time is the 2 s after the mark, not the 32 s since the meter was built.
func TestMeterTimesTheReceiptFromTheMarkedSend(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	meter := NewStreamMeter(func() time.Time { return current }, "participant", fixedNonce{})

	current = current.Add(30 * time.Second)
	meter.Sent()
	current = current.Add(2 * time.Second)
	meter.Receipt()
	outcome := meter.Outcome(0, nil)

	if got, want := outcome.ReceiptTime.Sub(outcome.SendTime), 2*time.Second; got != want {
		t.Errorf("send to receipt = %s, want %s", got, want)
	}
}
