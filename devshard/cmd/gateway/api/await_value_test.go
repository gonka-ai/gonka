package api

import (
	"testing"
	"time"
)

const awaitValueTimeout = 5 * time.Second

func awaitValue[T any](t *testing.T, values <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(awaitValueTimeout):
		t.Fatalf("%s never happened within %v", what, awaitValueTimeout)
		var zero T
		return zero
	}
}
