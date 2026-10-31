package api

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/registry"
	"devshard/host"
	"devshard/types"
	"devshard/user"
)

// fixedEscrows publishes one escrow; retired takes it out of the acquirable set while leaving it settling.
type fixedEscrows struct {
	escrowID string
	session  registry.EscrowSession
	retired  bool

	holds    atomic.Int64
	releases atomic.Int64
}

func (f *fixedEscrows) AcquireCommitted(escrowID string) (registry.EscrowSession, func(), bool) {
	if f.retired {
		return nil, nil, false
	}
	session, held := f.SettlementSession(escrowID)
	if !held {
		return nil, nil, false
	}
	f.holds.Add(1)
	var once sync.Once
	return session, func() { once.Do(func() { f.releases.Add(1) }) }, true
}

func (f *fixedEscrows) SettlementSession(escrowID string) (registry.EscrowSession, bool) {
	if escrowID != f.escrowID || f.session == nil {
		return nil, false
	}
	return f.session, true
}

// pointReadSession answers point reads and counts every full copy of the escrow state it is asked for.
type pointReadSession struct {
	registry.EscrowSession
	balance   uint64
	version   string
	records   map[uint64]types.InferenceRecord
	snapshots atomic.Int64
}

func (s *pointReadSession) SnapshotState() types.EscrowState {
	s.snapshots.Add(1)
	return types.EscrowState{}
}

func (s *pointReadSession) Nonce() uint64             { return 12 }
func (s *pointReadSession) PublishedNonce() uint64    { return 12 }
func (s *pointReadSession) Phase() types.SessionPhase { return types.PhaseActive }
func (s *pointReadSession) Balance() uint64           { return s.balance }
func (s *pointReadSession) ProtocolVersion() string   { return s.version }

func (s *pointReadSession) Inference(nonce uint64) (types.InferenceRecord, bool) {
	record, committed := s.records[nonce]
	return record, committed
}

func (s *pointReadSession) SealedInference(uint64) (types.InferenceRecord, bool) {
	return types.InferenceRecord{}, false
}

// Test flow:
//  1. Build an escrow session holding one committed inference record for nonce 4 that counts every full copy of its state.
//  2. Define a table of nonces to look up, varying across a committed nonce, a nonce the escrow never committed, and an empty escrow.
//  3. For each case, build the timeout payload for the nonce through the session's votes.
//  4. Assert the payload matches the case's expectation, the record's own numbers for the committed case and nil otherwise, and that no full copy was taken.
func TestTimeoutPayloadIsRebuiltFromTheCommittedRecord(t *testing.T) {
	t.Parallel()
	committed := map[uint64]types.InferenceRecord{
		4: {Model: liveModel, InputLength: 512, MaxTokens: 64, StartedAt: 1_700_000_000},
	}
	cases := []struct {
		name    string
		records map[uint64]types.InferenceRecord
		nonce   uint64
		want    *host.InferencePayload
	}{
		{
			name:    "a committed nonce carries the record's own numbers",
			records: committed,
			nonce:   4,
			want: &host.InferencePayload{
				Prompt:      livePrompt,
				Model:       liveModel,
				InputLength: 512,
				MaxTokens:   64,
				StartedAt:   1_700_000_000,
			},
		},
		{
			name:    "a nonce the escrow never committed has no payload to verify",
			records: committed,
			nonce:   5,
		},
		{
			name:  "an empty escrow has none either",
			nonce: 4,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			session := &pointReadSession{records: testCase.records}
			votes := escrowVotes{session: session, prompt: livePrompt}

			payload := votes.payload(testCase.nonce)

			if !reflect.DeepEqual(payload, testCase.want) {
				t.Errorf("payload = %+v, want %+v", payload, testCase.want)
			}
			if copies := session.snapshots.Load(); copies != 0 {
				t.Errorf("SnapshotState called %d times, want 0", copies)
			}
		})
	}
}

// Test flow:
//  1. Build a live session and a `fixedEscrows` publishing only that escrow.
//  2. Ask `sessions.Poster` for an escrow ID it does not hold.
//  3. Assert it reports not found.
//  4. Ask for the live escrow ID and assert it resolves a poster.
func TestPosterRefusesAnEscrowThisProcessNoLongerHolds(t *testing.T) {
	t.Parallel()
	session, machine := newLiveSession(t)
	sessions := NewSessions(&fixedEscrows{escrowID: liveEscrowID, session: registry.NewSessionHandle(session, machine)})

	if _, ok := sessions.Poster("8", liveParams()); ok {
		t.Error("Poster(8) resolved a poster for an escrow that is gone, want not found")
	}
	if _, ok := sessions.Poster(liveEscrowID, liveParams()); !ok {
		t.Errorf("Poster(%q) = not found, want the live escrow's poster", liveEscrowID)
	}
}

// Test flow:
//  1. Build a live session inside a `fixedEscrows` marked `retired`.
//  2. Ask `sessions.Target` for the retired escrow and assert it is not routable.
//  3. Ask `sessions.Poster` for the same escrow.
//  4. Assert it still resolves, so a retired escrow can still settle the votes its last race owed.
func TestPosterSettlesARetiredEscrowThatRoutingHasLost(t *testing.T) {
	t.Parallel()
	session, machine := newLiveSession(t)
	retired := &fixedEscrows{
		escrowID: liveEscrowID,
		session:  registry.NewSessionHandle(session, machine),
		retired:  true,
	}
	sessions := NewSessions(retired)

	if _, _, routable := sessions.Target(liveEscrowID); routable {
		t.Error("Target resolved a retired escrow, want not found: it must take no further request")
	}
	if _, settling := sessions.Poster(liveEscrowID, liveParams()); !settling {
		t.Errorf("Poster(%q) on a retired escrow = not found: every vote its last race owed is dropped", liveEscrowID)
	}
}

// Test flow:
//  1. Build a live session and a `fixedEscrows` publishing it.
//  2. Ask `sessions.Poster` with a raw prompt body instead of dispatch params.
//  3. Assert it reports not found, since no nonce could have committed such params.
func TestPosterRefusesParamsNoNonceCouldHaveCommitted(t *testing.T) {
	t.Parallel()
	session, machine := newLiveSession(t)
	sessions := NewSessions(&fixedEscrows{escrowID: liveEscrowID, session: registry.NewSessionHandle(session, machine)})

	if _, ok := sessions.Poster(liveEscrowID, livePrompt); ok {
		t.Error("Poster resolved a poster for a raw request body, want not found")
	}
}

// Test flow:
//  1. Prepare a live session with one committed nonce and resolve its poster.
//  2. Cancel the context up front.
//  3. Call `SettleTimeout` with the cancelled context.
//  4. Assert the error matches `context.Canceled`.
//  5. Assert the returned vote is empty, since the protocol deadline was never reached.
func TestSettleTimeoutReportsACancelledWaitAsNoVote(t *testing.T) {
	t.Parallel()
	session, machine := newLiveSession(t)
	prepared := prepareNonce(t, session)
	sessions := NewSessions(&fixedEscrows{escrowID: liveEscrowID, session: registry.NewSessionHandle(session, machine)})
	poster, ok := sessions.Poster(liveEscrowID, liveParams())
	if !ok {
		t.Fatalf("Poster(%q) = not found, want the live escrow's poster", liveEscrowID)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	vote, err := poster.SettleTimeout(cancelled, engine.TimeoutStep{Nonce: prepared.Nonce(), StartedAt: time.Now()})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("SettleTimeout error = %v, want it to match context.Canceled", err)
	}
	if vote.Kind != "" {
		t.Errorf("vote = %+v, want empty: the protocol deadline was never reached", vote)
	}
}

// Test flow:
//  1. Prepare a live session with one committed nonce and resolve its poster.
//  2. Call `SettleTimeout` with a deadline 24 hours in the past, with no host client registered as a verifier so the tally stays empty.
//  3. Assert the error matches `user.ErrTimeoutNotApplied`.
//  4. Assert the returned vote kind is "refused".
func TestSettleTimeoutReportsAnInsufficientVoteTallyAsAFailure(t *testing.T) {
	t.Parallel()
	session, machine := newLiveSession(t)
	prepared := prepareNonce(t, session)
	sessions := NewSessions(&fixedEscrows{escrowID: liveEscrowID, session: registry.NewSessionHandle(session, machine)})
	poster, ok := sessions.Poster(liveEscrowID, liveParams())
	if !ok {
		t.Fatalf("Poster(%q) = not found, want the live escrow's poster", liveEscrowID)
	}
	elapsedDeadline := time.Now().Add(-24 * time.Hour)

	vote, err := poster.SettleTimeout(context.Background(), engine.TimeoutStep{Nonce: prepared.Nonce(), StartedAt: elapsedDeadline})

	if !errors.Is(err, user.ErrTimeoutNotApplied) {
		t.Errorf("SettleTimeout error = %v, want it to match user.ErrTimeoutNotApplied", err)
	}
	if got, want := vote.Kind, "refused"; got != want {
		t.Errorf("vote kind = %q, want %q", got, want)
	}
}
