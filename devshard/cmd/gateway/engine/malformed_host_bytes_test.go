package engine

import (
	"sync"
	"sync/atomic"
	"testing"

	"devshard/cmd/gateway/filters"
)

func malformedHostPayloads() [][]byte {
	tail := "\\0\x05\\"
	return [][]byte{
		[]byte(`{"error":1,"` + tail),
		[]byte(`{"choices":[],"` + tail),
		[]byte(`{"usage":{},"` + tail),
		[]byte(`{"object":"error","message":"x","` + tail),
	}
}

// Test flow:
//  1. Decode payloads a host could send, each malformed in the shape that trips a lenient decoder, from several goroutines at once as concurrent attempts do.
//  2. Run them through the attempt classifier's event decode and its error decode, many times over.
//  3. Assert no decode panicked: a host's bytes must never be able to bring the gateway down.
func TestDecodeEvent_AMalformedHostEventNeverPanics(t *testing.T) {
	var panics atomic.Int64
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 2000 {
				for _, payload := range malformedHostPayloads() {
					func() {
						defer func() {
							if recover() != nil {
								panics.Add(1)
							}
						}()
						_, _ = decodeEvent(payload)
						_, _ = filters.DecodeUpstreamError(payload)
					}()
				}
			}
		})
	}
	workers.Wait()
	if count := panics.Load(); count != 0 {
		t.Fatalf("%d decodes of a host's malformed event panicked", count)
	}
}
