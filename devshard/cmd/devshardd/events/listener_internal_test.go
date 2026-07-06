package events

import "testing"

func TestListenerReadyHandler(t *testing.T) {
	l := NewListener("http://localhost:26657")
	var states []bool
	l.OnReady(func(ready bool) {
		states = append(states, ready)
	})

	l.setReady(true)
	l.setReady(false)

	if len(states) != 2 || !states[0] || states[1] {
		t.Fatalf("ready states = %v, want [true false]", states)
	}
}
