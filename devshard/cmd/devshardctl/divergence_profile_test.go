package main

import (
	"devshard/types"
	"errors"
	"fmt"
	"testing"
)

func TestStateRootDivergenceBaselineProfile(t *testing.T) {
	if isStateRootDivergenceError(nil) || isStateRootDivergenceError(errors.New("unrelated failure")) {
		t.Fatal("unrelated error classified as divergence")
	}
	if !isStateRootDivergenceError(fmt.Errorf("wrapped: %w", types.ErrPostStateRootMismatch)) {
		t.Fatal("baseline post-state mismatch was lost")
	}
	if got := isStateRootDivergenceError(fmt.Errorf("wrapped: %w", types.ErrStateHashMismatch)); got != false {
		t.Fatalf("state-hash mismatch=%t; want false", got)
	}
}
