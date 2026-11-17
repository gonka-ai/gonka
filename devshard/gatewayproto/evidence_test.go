package gatewayproto

import (
	"os"
	"testing"
)

func TestEvidenceMatchesBothGatewayBaselines(t *testing.T) {
	wire, err := os.ReadFile("testdata/error.sse")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/error.json")
	if err != nil {
		t.Fatal(err)
	}
	r := NewEvidenceCollector(64*1024, nil, nil)
	r.AppendEvents(wire)
	if string(r.Payload()) != string(want) || !r.Complete() || r.Truncated() {
		t.Fatalf("unexpected retained proof: %s", r.Payload())
	}
}

func TestEvidenceResetCanReuseButReleaseCannot(t *testing.T) {
	held := 0
	reserve := func(n int) bool { held += n; return true }
	release := func(n int) { held -= n }
	r := NewEvidenceCollector(64*1024, reserve, release)
	events := []byte("data: {\"error\":{\"message\":\"boom\"}}\n\ndata: [DONE]\n\n")
	r.AppendEvents(events)
	if held == 0 || held != r.BytesHeld() {
		t.Fatal("bytes were not reserved")
	}
	r.Reset()
	if held != 0 || r.Complete() || r.Truncated() || len(r.Lines()) != 0 {
		t.Fatal("reset did not return retention to its initial state")
	}
	r.AppendEvents(events)
	if held == 0 {
		t.Fatal("reset did not allow reuse")
	}
	r.Release()
	r.Release()
	r.Reset()
	r.AppendEvents(events)
	if held != 0 || len(r.Lines()) != 0 {
		t.Fatal("released collector was reopened or refunded twice")
	}
}

func TestEvidenceCapAndFailedReservationKeepOnlyAcceptedPrefix(t *testing.T) {
	first := `data: {"error":{"message":"boom"}}`
	for _, external := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "external"}[external], func(t *testing.T) {
			held, calls := 0, 0
			cap := len(first) + 1
			var reserve func(int) bool
			var refund func(int)
			if external {
				cap = 64 * 1024
				reserve = func(n int) bool {
					calls++
					if calls == 2 {
						return false
					}
					held += n
					return true
				}
				refund = func(n int) { held -= n }
			}
			r := NewEvidenceCollector(cap, reserve, refund)
			r.AppendLines([]string{first, "data: [DONE]"})
			if !r.Truncated() || r.Complete() || len(r.Lines()) != 1 {
				t.Fatal("retained a rejected line or lost truncation")
			}
			if external && held != len(first)+1 {
				t.Fatal("wrong reserved byte count")
			}
			r.Reset()
			if held != 0 {
				t.Fatal("accepted prefix was not refunded")
			}
		})
	}
}

func TestEvidenceNilAndExplicitTruncation(t *testing.T) {
	var missing *EvidenceCollector
	missing.Reset()
	missing.Release()
	missing.AppendEvents(nil)
	if missing.Payload() != nil || missing.BytesHeld() != 0 || missing.Complete() || missing.Truncated() {
		t.Fatal("nil collector must carry no proof")
	}
	r := NewEvidenceCollector(1024, nil, nil)
	r.MarkTruncated()
	r.AppendEvents([]byte("data: [DONE]\n\n"))
	if !r.Truncated() || r.Complete() || len(r.Lines()) != 0 {
		t.Fatal("truncated collector accepted more data")
	}
}
