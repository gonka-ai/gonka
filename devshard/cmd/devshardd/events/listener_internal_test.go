package events

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListenerReadyHandler(t *testing.T) {
	l := NewListener("http://localhost:26657")
	var states []bool
	l.OnReady(func(ready bool) {
		states = append(states, ready)
	})

	l.setReady(true)
	l.setReady(false)

	require.Equal(t, []bool{true, false}, states)
}
