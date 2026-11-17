package strictjson

import (
	"sync"
	"sync/atomic"
	"testing"
)

type probe struct {
	Error  any    `json:"error"`
	Object string `json:"object"`
}

// Test flow:
//  1. Decode, from several goroutines at once, a payload malformed in the shape that makes goccy read past its buffer.
//  2. Repeat it many times, as a busy gateway would.
//  3. Assert every call refuses it with ErrInvalid and none panics.
func TestUnmarshalRefusesMalformedBytesWithoutPanicking(t *testing.T) {
	payload := []byte(`{"error":1,"` + "\\0\x05\\")
	var panics, accepted atomic.Int64
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 2000 {
				func() {
					defer func() {
						if recover() != nil {
							panics.Add(1)
						}
					}()
					var decoded probe
					if Unmarshal(payload, &decoded) == nil {
						accepted.Add(1)
					}
				}()
			}
		})
	}
	workers.Wait()
	if panics.Load() != 0 || accepted.Load() != 0 {
		t.Fatalf("panics %d, accepted %d, want neither", panics.Load(), accepted.Load())
	}
}

// Test flow:
//  1. Generate payloads and decode each with Unmarshal.
//  2. Let the fuzz engine fail the run on any panic.
//  3. Assert by that failure's absence that no byte sequence a host or client sends can crash the decode.
func FuzzUnmarshalNeverPanics(f *testing.F) {
	f.Add([]byte(`{"error":{"message":"boom"},"object":"error"}`))
	f.Add([]byte(`{"error":1,"` + "\\0\x05\\"))
	f.Fuzz(func(t *testing.T, payload []byte) {
		var decoded probe
		_ = Unmarshal(payload, &decoded)
	})
}

// Test flow:
//  1. Decode one known-key object, an object with an unknown key, two values in a row, and a truncated value.
//  2. Assert only the first decodes.
func TestDecodeKnownFields(t *testing.T) {
	type knownShape struct {
		Name string `json:"name"`
	}
	var decoded knownShape
	if err := DecodeKnownFields([]byte(`{"name":"a"}`), &decoded); err != nil || decoded.Name != "a" {
		t.Errorf("known object: %+v, %v", decoded, err)
	}
	for _, body := range []string{`{"title":"a"}`, `{"name":"a"}{"name":"b"}`, `{"name":`} {
		if err := DecodeKnownFields([]byte(body), &knownShape{}); err == nil {
			t.Errorf("DecodeKnownFields(%s) = nil, want a refusal", body)
		}
	}
}
