package nonces

import (
	"testing"

	"devshard/cmd/gateway/registry"
	"devshard/types"
	"devshard/user"
)

type watchedSession struct {
	registry.EscrowSession
	session *user.Session
}

func (s watchedSession) UserSession() *user.Session { return s.session }

type silentJournal struct{}

func (silentJournal) DiffComposed(string, *types.Diff) {}

// Test flow:
//  1. Watch an escrow's diffs on one session, as a sweep that read the escrow just before it retired does after the retirement cleared the mark.
//  2. Watch the same escrow again on the session it was published with afterwards.
//  3. Assert the ledger now watches the new session: the mark names the session it was set on, so a stale one never keeps a re-published escrow unwatched.
func TestAReplacedSessionOfTheSameEscrowIsWatched(t *testing.T) {
	recorder := recorderFor(nil)
	retired, republished := &user.Session{}, &user.Session{}

	recorder.watchDiffs("escrow-1", watchedSession{session: retired}, silentJournal{})
	recorder.watchDiffs("escrow-1", watchedSession{session: republished}, silentJournal{})

	if watched, _ := recorder.observing.Load("escrow-1"); watched != republished {
		t.Fatal("the ledger still watches the retired session, so the re-published escrow's diffs never reach the journal")
	}
}
