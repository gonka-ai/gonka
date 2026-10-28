package registry

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"devshard/cmd/gateway/scheduler"
	"devshard/user"
)

// Test flow:
//  1. Advance a nonce stream whose session binds nonce 5 and then fails to prepare it after the commit.
//  2. Assert the scheduler is told nonce 5 was spent, not handed a bare session error.
func TestAdvanceReportsANonceSpentOnAnUnpreparedRequest(t *testing.T) {
	session := newFakeSession("hostA", "hostB")
	session.prepare = func(chooser user.ParamsForHost) (*user.PreparedInference, error) {
		if _, _, _, err := chooser(user.HostBinding{Nonce: 5, HostIdx: 1, ParticipantKey: "hostB"}); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("read catch-up for host 1 nonce 5: %w: %w", user.ErrPreparedAfterCommit, errors.New("database is locked"))
	}
	stream := newNonceStream(session, "test-model", time.Now)

	_, err := stream.Advance(func(scheduler.HostBinding) scheduler.NonceIntent {
		return scheduler.NonceIntent{Commit: true, Params: user.InferenceParams{Model: "test-model"}}
	})

	var spent *scheduler.SpentNonceError
	if !errors.As(err, &spent) || spent.Nonce != 5 {
		t.Fatalf("Advance err = %v, want nonce 5 reported spent", err)
	}
}
