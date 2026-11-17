package registry

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"devshard/cmd/gateway/scheduler"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
	"devshard/user"
)

const liveEscrowID = "7"

// newLiveStream builds a real *user.Session over in-memory storage and nil host clients.
func newLiveStream(t *testing.T, slotsPerSigner ...int) (nonceStream, EscrowSession, []types.SlotAssignment) {
	t.Helper()
	signers := make([]*signing.Secp256k1Signer, len(slotsPerSigner))
	for index := range signers {
		signers[index] = testutil.MustGenerateKey(t)
	}
	group := testutil.MakeMultiSlotGroup(signers, slotsPerSigner)
	config := testutil.DefaultConfig(len(group))
	creator := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	store := testutil.MustMemoryStore(t, liveEscrowID, creator.Address(), config, group, 1_000_000)

	machine, err := state.NewStateMachine(liveEscrowID, config, group, 1_000_000, creator.Address(), verifier, store)
	if err != nil {
		t.Fatalf("NewStateMachine = %v, want nil", err)
	}
	session, err := user.NewSession(machine, creator, liveEscrowID, group, make([]user.HostClient, len(group)), verifier, user.WithStorage(store))
	if err != nil {
		t.Fatalf("NewSession = %v, want nil", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	handle := NewSessionHandle(session, machine)
	return newNonceStream(handle, "qwen", fixedClock()), handle, group
}

// Test flow:
//  1. Build a live stream over a 3-slot group and advance it, committing inference params for model qwen.
//  2. Assert the commit succeeds and returns a prepared nonce of 1 on host index 1.
//  3. Assert the binding handed to the decide callback names nonce 1 and the group's slot-1 participant.
//  4. Assert `SlotParticipants` maps that nonce's slot to the same bound participant.
//  5. Assert the session's nonce advances to 1 after the commit.
func TestAdvanceCommitsTheServeParams(t *testing.T) {
	t.Parallel()
	stream, session, group := newLiveStream(t, 1, 1, 1)
	params := user.InferenceParams{Model: "qwen", Prompt: []byte(`{"messages":[]}`), InputLength: 15, MaxTokens: 64}

	var seen scheduler.HostBinding
	prepared, err := stream.Advance(func(binding scheduler.HostBinding) scheduler.NonceIntent {
		seen = binding
		return scheduler.NonceIntent{Commit: true, Params: params}
	})
	if err != nil {
		t.Fatalf("Advance = %v, want nil", err)
	}
	if prepared == nil {
		t.Fatal("Advance returned no prepared nonce, want one")
	}
	if got, want := prepared.Nonce(), uint64(1); got != want {
		t.Errorf("prepared nonce = %d, want %d", got, want)
	}
	if got, want := prepared.HostIdx(), 1; got != want {
		t.Errorf("prepared host index = %d, want %d (nonce %% group size)", got, want)
	}
	if seen.Nonce != 1 || seen.HostIdx != 1 || seen.Participant != group[1].ValidatorAddress {
		t.Errorf("binding handed to decide = %+v, want nonce 1 on %s", seen, group[1].ValidatorAddress)
	}
	slots := stream.SlotParticipants()
	if indexed := slots[seen.Nonce%uint64(stream.GroupSize())]; indexed != seen.Participant {
		t.Errorf("SlotParticipants()[nonce %%%% groupSize] = %q, want the bound participant %q", indexed, seen.Participant)
	}
	if got, want := session.Nonce(), uint64(1); got != want {
		t.Errorf("session nonce after a commit = %d, want %d", got, want)
	}
}

// Test flow:
//  1. Build a live stream over a 2-slot group and advance it, committing a ghost intent with no request params.
//  2. Assert the commit succeeds and returns a prepared nonce.
//  3. Assert the session's nonce advances to 1.
func TestAdvanceBurnsAGhostNonceWithoutARequest(t *testing.T) {
	t.Parallel()
	stream, session, _ := newLiveStream(t, 1, 1)

	prepared, err := stream.Advance(func(scheduler.HostBinding) scheduler.NonceIntent {
		return scheduler.NonceIntent{Commit: true, Ghost: true}
	})
	if err != nil {
		t.Fatalf("Advance = %v, want nil", err)
	}
	if prepared == nil {
		t.Fatal("a ghost must still commit its nonce, got none")
	}
	if got, want := session.Nonce(), uint64(1); got != want {
		t.Errorf("session nonce after a ghost = %d, want %d", got, want)
	}
}

// Test flow:
//  1. Build a live stream and advance it with an intent that declines to commit.
//  2. Assert `Advance` returns no error and no prepared nonce.
//  3. Assert the session's nonce stays at 0.
func TestAdvanceLeavesADeclinedNonceUnconsumed(t *testing.T) {
	t.Parallel()
	stream, session, _ := newLiveStream(t, 1, 1)

	prepared, err := stream.Advance(func(scheduler.HostBinding) scheduler.NonceIntent {
		return scheduler.NonceIntent{}
	})
	if err != nil {
		t.Fatalf("Advance = %v, want nil (a decline is not a failure)", err)
	}
	if prepared != nil {
		t.Errorf("Advance returned %v for a declined nonce, want nil", prepared)
	}
	if got := session.Nonce(); got != 0 {
		t.Errorf("session nonce after a decline = %d, want 0", got)
	}
}

// Test flow:
//  1. Build a live stream and advance it, committing params of the wrong type.
//  2. Assert `Advance` returns an error naming the wrong params type.
//  3. Assert the session's nonce stays at 0.
func TestAdvanceRejectsParamsItCannotDispatch(t *testing.T) {
	t.Parallel()
	stream, session, _ := newLiveStream(t, 1, 1)

	_, err := stream.Advance(func(scheduler.HostBinding) scheduler.NonceIntent {
		return scheduler.NonceIntent{Commit: true, Params: "not an inference"}
	})

	if err == nil {
		t.Fatal("Advance = nil, want an error naming the wrong params type")
	}
	if got := session.Nonce(); got != 0 {
		t.Errorf("session nonce after rejected params = %d, want 0", got)
	}
}

// Test flow:
//  1. Build a nonce stream over a fake session whose prepare call always fails.
//  2. Advance it, committing inference params.
//  3. Assert the returned error wraps the fake session's failure.
func TestAdvanceReportsASessionFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("storage gone")
	session := newFakeSession("hostA")
	session.prepare = func(user.ParamsForHost) (*user.PreparedInference, error) { return nil, failure }
	stream := newNonceStream(session, "qwen", fixedClock())

	_, err := stream.Advance(func(scheduler.HostBinding) scheduler.NonceIntent {
		return scheduler.NonceIntent{Commit: true, Params: user.InferenceParams{}}
	})

	if !errors.Is(err, failure) {
		t.Errorf("Advance = %v, want it to wrap %v", err, failure)
	}
}

// Test flow:
//  1. Build a live stream over two signers, the first holding two slots, so the group spans three slots over two participants.
//  2. Assert `GroupSize` matches the group and `ParticipantKeys` reports the two distinct participants.
//  3. Assert `LatestNonce` starts at 0 on a fresh session.
//  4. Advance the stream with a ghost commit.
//  5. Assert `LatestNonce` reports 1 after the commit.
func TestStreamReportsTheGroupAndTheLatestNonce(t *testing.T) {
	t.Parallel()
	stream, _, group := newLiveStream(t, 2, 1)

	if got, want := stream.GroupSize(), len(group); got != want {
		t.Errorf("GroupSize() = %d, want %d", got, want)
	}
	if got, want := len(stream.ParticipantKeys()), 2; got != want {
		t.Errorf("len(ParticipantKeys()) = %d, want %d distinct participants", got, want)
	}
	if got := stream.LatestNonce(); got != 0 {
		t.Errorf("LatestNonce() on a fresh session = %d, want 0", got)
	}

	if _, err := stream.Advance(func(scheduler.HostBinding) scheduler.NonceIntent {
		return scheduler.NonceIntent{Commit: true, Ghost: true}
	}); err != nil {
		t.Fatalf("Advance = %v, want nil", err)
	}

	if got := stream.LatestNonce(); got != 1 {
		t.Errorf("LatestNonce() after one commit = %d, want 1", got)
	}
}

// Test flow:
//  1. Build a nonce stream for model kimi over a fake session with a fixed clock.
//  2. Read the stream's ghost params.
//  3. Assert they carry the model, the ghost prompt and its length, the ghost max-tokens reservation, and the clock's fixed instant as StartedAt.
func TestGhostParamsCarryTheEscrowModelAndTheInjectedClock(t *testing.T) {
	t.Parallel()
	stream := newNonceStream(newFakeSession("hostA"), "kimi", fixedClock())

	params := stream.ghostParams()

	want := user.InferenceParams{
		Model:       "kimi",
		Prompt:      ghostPrompt,
		InputLength: uint64(len(ghostPrompt)),
		MaxTokens:   ghostMaxTokens,
		StartedAt:   fixedClock()().Unix(),
	}
	if !reflect.DeepEqual(params, want) {
		t.Errorf("ghostParams() = %+v, want %+v", params, want)
	}
}

// Test flow:
//  1. Build a live stream and take its session handle.
//  2. Assert `Phase` reports the machine as active.
//  3. Assert `UserSession` returns the concrete handle rather than nil.
func TestSessionHandleReportsThePhaseOfItsOwnStateMachine(t *testing.T) {
	t.Parallel()
	_, session, _ := newLiveStream(t, 1, 1)

	if got, want := session.Phase(), types.PhaseActive; got != want {
		t.Errorf("Phase() = %v, want %v", got, want)
	}
	if session.UserSession() == nil {
		t.Error("UserSession() = nil, want the concrete handle the dispatch boundary needs")
	}
}

// Test flow:
//  1. Parse the ghost prompt's JSON body.
//  2. Assert its max_tokens field matches the ghostMaxTokens reservation.
//  3. Assert ghostMaxTokens is at or above `state.MinReservationTokens`, the floor the state machine refuses below.
func TestGhostPromptAgreesWithItsReservation(t *testing.T) {
	var body struct {
		MaxTokens uint64 `json:"max_tokens"`
	}
	if err := json.Unmarshal(ghostPrompt, &body); err != nil {
		t.Fatalf("parsing ghostPrompt: %v", err)
	}
	if body.MaxTokens != ghostMaxTokens {
		t.Errorf("ghostPrompt max_tokens = %d, reservation = %d", body.MaxTokens, ghostMaxTokens)
	}
	if ghostMaxTokens < state.MinReservationTokens {
		t.Errorf("ghostMaxTokens = %d, below the floor %d the state machine refuses", ghostMaxTokens, state.MinReservationTokens)
	}
}

type fixedChatBudget time.Duration

func (budget fixedChatBudget) ChatBudgetWait() time.Duration { return time.Duration(budget) }

// Test flow:
//  1. Build a nonce stream over a session whose host A has budget for a chat, whose host B would wait for one, and which has no connection for host C.
//  2. Ask the stream whether each host's chat budget is spent.
//  3. Assert only host B reads as spent: a host with budget or with no paced connection is never held back.
func TestANonceStreamReadsAHostWithNoChatBudgetAsSpent(t *testing.T) {
	t.Parallel()
	session := newFakeSession("hostA", "hostB", "hostC")
	session.budgets = map[string]ChatBudget{"hostA": fixedChatBudget(0), "hostB": fixedChatBudget(50 * time.Millisecond)}
	stream := newNonceStream(session, "qwen", fixedClock())

	spent := []bool{stream.ChatBudgetSpent("hostA"), stream.ChatBudgetSpent("hostB"), stream.ChatBudgetSpent("hostC")}

	if !reflect.DeepEqual(spent, []bool{false, true, false}) {
		t.Fatalf("ChatBudgetSpent(hostA, hostB, hostC) = %v, want [false true false]", spent)
	}
}
