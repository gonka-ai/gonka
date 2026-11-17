package api

import (
	"devshard/types"
	"errors"
	"fmt"
	"testing"
)

func TestStateRootDivergenceBaselineProfile(t *testing.T) {
	if divergedState(nil) || divergedState(errors.New("unrelated failure")) {
		t.Fatal("unrelated error classified as divergence")
	}
	if !divergedState(fmt.Errorf("wrapped: %w", types.ErrPostStateRootMismatch)) {
		t.Fatal("baseline post-state mismatch was lost")
	}
	if got := divergedState(fmt.Errorf("wrapped: %w", types.ErrStateHashMismatch)); got != true {
		t.Fatalf("state-hash mismatch=%t; want true", got)
	}
}
