package syncx

import "testing"

func TestKeyedForgetsAKeyNobodyHolds(t *testing.T) {
	// arrange
	var keyed Keyed[string]

	// act
	release := keyed.Lock("req-1")
	held := len(keyed.locks)
	release()

	// assert
	if held != 1 || len(keyed.locks) != 0 {
		t.Fatalf("held %d then %d keys, want the key kept while locked and gone once released", held, len(keyed.locks))
	}
}
