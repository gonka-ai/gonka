package poc

import (
	"context"
	"testing"

	"decentralized-api/chainphase"
	"decentralized-api/poc/earlyshare"

	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestEarlyShareCaptureTarget(t *testing.T) {
	mkState := func(phase types.EpochPhase, pocStart int64, dur int64) *chainphase.EpochState {
		return &chainphase.EpochState{
			IsSynced:     true,
			CurrentPhase: phase,
			LatestEpoch: types.EpochContext{
				PocStartBlockHeight: pocStart,
				EpochParams:         types.EpochParams{PocStageDuration: dur},
			},
		}
	}

	t.Run("regular poc generation", func(t *testing.T) {
		st := mkState(types.PoCGeneratePhase, 1000, 30)
		stage, target, ok := EarlyShareCaptureTarget(st, 1.0/3.0)
		if !ok {
			t.Fatal("expected ok")
		}
		if stage != 1000 {
			t.Fatalf("stage = %d, want 1000", stage)
		}
		if target != 1010 { // 1000 + floor(30/3)
			t.Fatalf("target = %d, want 1010", target)
		}
	})

	t.Run("wind-down still captures", func(t *testing.T) {
		st := mkState(types.PoCGenerateWindDownPhase, 1000, 30)
		_, _, ok := EarlyShareCaptureTarget(st, 1.0/3.0)
		if !ok {
			t.Fatal("wind-down should still allow capture")
		}
	})

	t.Run("non-generation phase skips", func(t *testing.T) {
		st := mkState(types.PoCValidatePhase, 1000, 30)
		if _, _, ok := EarlyShareCaptureTarget(st, 1.0/3.0); ok {
			t.Fatal("validate phase must not capture")
		}
	})

	t.Run("nil/not synced skips", func(t *testing.T) {
		if _, _, ok := EarlyShareCaptureTarget(nil, 1.0/3.0); ok {
			t.Fatal("nil must skip")
		}
		st := mkState(types.PoCGeneratePhase, 1000, 30)
		st.IsSynced = false
		if _, _, ok := EarlyShareCaptureTarget(st, 1.0/3.0); ok {
			t.Fatal("not synced must skip")
		}
	})

	t.Run("invalid fraction or duration skips", func(t *testing.T) {
		if _, _, ok := EarlyShareCaptureTarget(mkState(types.PoCGeneratePhase, 1000, 0), 1.0/3.0); ok {
			t.Fatal("zero duration must skip")
		}
		if _, _, ok := EarlyShareCaptureTarget(mkState(types.PoCGeneratePhase, 1000, 30), 0); ok {
			t.Fatal("zero fraction must skip")
		}
	})

	t.Run("fraction arithmetic is integer-deterministic", func(t *testing.T) {
		// Different float spellings of "one third" (the code default, the
		// documented config literal, a hand-typed shorter one) must quantize
		// to the same ppm and produce the identical target height for any
		// duration. This is what pins all validators to the same capture
		// block.
		spellings := []float64{1.0 / 3.0, 0.3333333333, 0.333333}
		for _, dur := range []int64{30, 100, 720, 7201, 99999} {
			want := int64(-1)
			for _, f := range spellings {
				_, target, ok := EarlyShareCaptureTarget(mkState(types.PoCGeneratePhase, 1000, dur), f)
				if !ok {
					t.Fatalf("fraction %v duration %d: expected ok", f, dur)
				}
				if want == -1 {
					want = target
				} else if target != want {
					t.Fatalf("fraction %v duration %d: target %d differs from %d", f, dur, target, want)
				}
			}
			// offset = round(dur * 333333 / 1e6), pure integer arithmetic.
			expected := 1000 + (dur*333333+500000)/1000000
			if want != expected {
				t.Fatalf("duration %d: target %d, want %d", dur, want, expected)
			}
		}
	})

	t.Run("confirmation poc generation", func(t *testing.T) {
		st := mkState(types.InferencePhase, 1000, 30)
		st.ActiveConfirmationPoCEvent = &types.ConfirmationPoCEvent{
			Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
			TriggerHeight:         5000,
			GenerationStartHeight: 5002,
		}
		stage, target, ok := EarlyShareCaptureTarget(st, 1.0/3.0)
		if !ok {
			t.Fatal("expected ok for CPoC")
		}
		if stage != 5000 {
			t.Fatalf("stage = %d, want 5000", stage)
		}
		if target != 5012 { // 5002 + 10
			t.Fatalf("target = %d, want 5012", target)
		}
	})
}

// fakeStore is an in-memory earlyShareStore for Evaluate tests.
type fakeStore struct {
	captured    map[int64]bool
	checkpoints map[int64][]earlyshare.Checkpoint
	state       map[string]earlyshare.GuardState
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		captured:    map[int64]bool{},
		checkpoints: map[int64][]earlyshare.Checkpoint{},
		state:       map[string]earlyshare.GuardState{},
	}
}

func (f *fakeStore) HasCompletedCapture(_ context.Context, stage int64) (bool, error) {
	return f.captured[stage], nil
}
func (f *fakeStore) UpsertCheckpoints(_ context.Context, cps []earlyshare.Checkpoint) error {
	for _, c := range cps {
		f.checkpoints[c.StageHeight] = append(f.checkpoints[c.StageHeight], c)
	}
	return nil
}
func (f *fakeStore) MarkStageCaptured(_ context.Context, stage int64, _, _ int64, _ map[string]int) error {
	f.captured[stage] = true
	return nil
}
func (f *fakeStore) MarkCaptureRun(_ context.Context, _ int64, _ string, _, _ int64, _ int, _ string) error {
	return nil
}
func (f *fakeStore) GetCheckpoints(_ context.Context, stage int64) ([]earlyshare.Checkpoint, error) {
	return f.checkpoints[stage], nil
}
func (f *fakeStore) GetGuardState(_ context.Context, p, m string) (earlyshare.GuardState, bool, error) {
	st, ok := f.state[p+"|"+m]
	if !ok {
		return earlyshare.GuardState{ParticipantAddress: p, ModelID: m}, false, nil
	}
	return st, true, nil
}
func (f *fakeStore) UpsertGuardState(_ context.Context, st earlyshare.GuardState) error {
	f.state[st.ParticipantAddress+"|"+st.ModelID] = st
	return nil
}
func (f *fakeStore) DeleteStage(_ context.Context, stage int64) error {
	delete(f.checkpoints, stage)
	delete(f.captured, stage)
	return nil
}

func TestEvaluate(t *testing.T) {
	ctx := context.Background()
	const stage = int64(1000)
	const model = "m1"

	store := newFakeStore()
	store.captured[stage] = true
	store.checkpoints[stage] = []earlyshare.Checkpoint{
		{StageHeight: stage, ParticipantAddress: "a", ModelID: model, EarlyCount: 10, EarlyRootHash: []byte{1}},
		{StageHeight: stage, ParticipantAddress: "b", ModelID: model, EarlyCount: 90, EarlyRootHash: []byte{2}},
		// c has no early checkpoint -> early_share 0
	}
	// Seed c with one grace miss already used so a fresh fail votes no.
	store.state["c|"+model] = earlyshare.GuardState{ParticipantAddress: "c", ModelID: model, ConsecutiveMisses: 1}

	guard := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce, RequireInclusionProof: true}, store)
	if guard == nil {
		t.Fatal("guard should be constructed")
	}

	finalCommits := []*types.PoCV2StoreCommitWithAddress{
		{ParticipantAddress: "a", ModelId: model, Count: 100, RootHash: []byte{0xaa}},
		{ParticipantAddress: "b", ModelId: model, Count: 100, RootHash: []byte{0xbb}},
		{ParticipantAddress: "c", ModelId: model, Count: 100, RootHash: []byte{0xcc}},
	}
	votingPowers := map[string]map[string]int64{
		model: {"a": 10, "b": 10, "c": 10},
	}
	// Validate a (passing) and c (failing) only.
	assigned := map[string]bool{
		earlyShareKey("a", model): true,
		earlyShareKey("c", model): true,
	}

	// Evaluate as a CPoC stage so a's pass advances guard state.
	decisions := guard.Evaluate(ctx, stage, true, finalCommits, votingPowers, assigned)

	// shares: a=0.1, b=0.9, c=0. Weighted median (equal weights) -> 0.1.
	// threshold = 0.1 * 0.5 = 0.05.
	decA, okA := decisions[earlyShareKey("a", model)]
	if !okA {
		t.Fatal("missing decision for a")
	}
	if decA.shareVoteNo {
		t.Fatalf("a should pass (share 0.1 >= 0.05); got vote no")
	}
	if !decA.requireInclusion {
		t.Fatal("a has early_count>0 so inclusion should be required")
	}

	decC, okC := decisions[earlyShareKey("c", model)]
	if !okC {
		t.Fatal("missing decision for c")
	}
	if !decC.shareVoteNo {
		t.Fatal("c should vote no (share 0 < 0.05, after grace already used)")
	}
	if decC.requireInclusion {
		t.Fatal("c has early_count 0 so inclusion must not be required")
	}

	// b was not assigned -> no decision, but its share still informed the median.
	if _, ok := decisions[earlyShareKey("b", model)]; ok {
		t.Fatal("b should not have a decision (not assigned)")
	}

	// a's guard state should now reflect a CPoC pass (streak reset).
	if st := store.state["a|"+model]; st.ConsecutiveMisses != 0 {
		t.Fatalf("a state not reset by CPoC pass: %+v", st)
	}
}

// TestEvaluatePoCPassDoesNotAdvance verifies that a passing regular PoC stage
// does not reset the miss streak (only a CPoC pass does).
func TestEvaluatePoCPassDoesNotAdvance(t *testing.T) {
	ctx := context.Background()
	const stage = int64(2000)
	const model = "m1"

	store := newFakeStore()
	store.captured[stage] = true
	store.checkpoints[stage] = []earlyshare.Checkpoint{
		{StageHeight: stage, ParticipantAddress: "a", ModelID: model, EarlyCount: 50, EarlyRootHash: []byte{1}},
	}
	// Seed a with one grace miss already used.
	store.state["a|"+model] = earlyshare.GuardState{ParticipantAddress: "a", ModelID: model, ConsecutiveMisses: 1}

	guard := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeObserve}, store)
	finalCommits := []*types.PoCV2StoreCommitWithAddress{
		{ParticipantAddress: "a", ModelId: model, Count: 100, RootHash: []byte{0xaa}},
	}
	votingPowers := map[string]map[string]int64{model: {"a": 10}}
	assigned := map[string]bool{earlyShareKey("a", model): true}

	// Regular PoC (isConfirmation=false): a passes (share 0.5 >= threshold) but
	// the pass must NOT reset the streak.
	guard.Evaluate(ctx, stage, false, finalCommits, votingPowers, assigned)
	if st := store.state["a|"+model]; st.ConsecutiveMisses != 1 {
		t.Fatalf("PoC pass must not reset streak: %+v", st)
	}
}

func TestEvaluateSkipsWhenNotCaptured(t *testing.T) {
	store := newFakeStore() // nothing captured
	guard := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeObserve}, store)
	got := guard.Evaluate(context.Background(), 1000, false, nil, nil, nil)
	if got != nil {
		t.Fatalf("expected nil decisions when stage not captured, got %v", got)
	}
}

func TestNewEarlyShareGuardDisabled(t *testing.T) {
	if g := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeDisabled}, newFakeStore()); g != nil {
		t.Fatal("disabled config should yield nil guard")
	}
	if g := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeObserve}, nil); g != nil {
		t.Fatal("nil store should yield nil guard")
	}
}

// fakeESQueryClient implements earlyShareQueryClient for MaybeCapture tests.
type fakeESQueryClient struct {
	resp    *types.QueryAllPoCV2StoreCommitsForStageResponse
	err     error
	calls   int
	lastCtx context.Context
}

func (f *fakeESQueryClient) AllPoCV2StoreCommitsForStage(ctx context.Context, _ *types.QueryAllPoCV2StoreCommitsForStageRequest, _ ...grpc.CallOption) (*types.QueryAllPoCV2StoreCommitsForStageResponse, error) {
	f.calls++
	f.lastCtx = ctx
	return f.resp, f.err
}

func TestMaybeCapture(t *testing.T) {
	ctx := context.Background()
	const stage = int64(1000)

	t.Run("captures once then is idempotent", func(t *testing.T) {
		store := newFakeStore()
		guard := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeObserve}, store)
		qc := &fakeESQueryClient{resp: &types.QueryAllPoCV2StoreCommitsForStageResponse{
			Commits: []*types.PoCV2StoreCommitWithAddress{
				{ParticipantAddress: "a", ModelId: "m1", Count: 10, RootHash: []byte{1}},
				{ParticipantAddress: "b", ModelId: "m1", Count: 20, RootHash: []byte{2}},
			},
		}}

		guard.MaybeCapture(ctx, qc, stage, 1010, 1010)
		if qc.calls != 1 {
			t.Fatalf("expected 1 query call, got %d", qc.calls)
		}
		if got := len(store.checkpoints[stage]); got != 2 {
			t.Fatalf("expected 2 checkpoints stored, got %d", got)
		}
		if !store.captured[stage] {
			t.Fatal("stage should be marked captured")
		}

		// Second call must not re-query (HasCompletedCapture is true).
		guard.MaybeCapture(ctx, qc, stage, 1010, 1010)
		if qc.calls != 1 {
			t.Fatalf("expected no extra query call, got %d", qc.calls)
		}
	})

	t.Run("query error fails open (no capture recorded)", func(t *testing.T) {
		store := newFakeStore()
		guard := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce}, store)
		qc := &fakeESQueryClient{err: context.DeadlineExceeded}

		guard.MaybeCapture(ctx, qc, stage, 1010, 1010)
		if store.captured[stage] {
			t.Fatal("stage must not be marked captured on query error")
		}
		if len(store.checkpoints[stage]) != 0 {
			t.Fatal("no checkpoints should be stored on query error")
		}
	})

	t.Run("disabled guard is a no-op", func(t *testing.T) {
		var guard *EarlyShareGuard // nil == disabled
		qc := &fakeESQueryClient{}
		guard.MaybeCapture(ctx, qc, stage, 1010, 1010)
		if qc.calls != 0 {
			t.Fatalf("disabled guard must not query, got %d calls", qc.calls)
		}
	})

	t.Run("query is pinned to the target height", func(t *testing.T) {
		store := newFakeStore()
		guard := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeObserve}, store)
		qc := &fakeESQueryClient{resp: &types.QueryAllPoCV2StoreCommitsForStageResponse{}}

		guard.MaybeCapture(ctx, qc, stage, 1010, 1042)
		if qc.calls != 1 {
			t.Fatalf("expected 1 query call, got %d", qc.calls)
		}
		md, ok := metadata.FromOutgoingContext(qc.lastCtx)
		if !ok {
			t.Fatal("capture query context missing outgoing metadata")
		}
		heights := md.Get(grpctypes.GRPCBlockHeightHeader)
		if len(heights) != 1 || heights[0] != "1010" {
			t.Fatalf("expected height header pinned to 1010, got %v", heights)
		}
	})

	t.Run("failed capture can be retried on a later block", func(t *testing.T) {
		store := newFakeStore()
		guard := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeObserve}, store)
		qc := &fakeESQueryClient{err: context.DeadlineExceeded}

		guard.MaybeCapture(ctx, qc, stage, 1010, 1010)
		if store.captured[stage] {
			t.Fatal("stage must not be marked captured on query error")
		}

		// Retry a few blocks later succeeds and still pins the original target.
		qc.err = nil
		qc.resp = &types.QueryAllPoCV2StoreCommitsForStageResponse{
			Commits: []*types.PoCV2StoreCommitWithAddress{
				{ParticipantAddress: "a", ModelId: "m1", Count: 10, RootHash: []byte{1}},
			},
		}
		guard.MaybeCapture(ctx, qc, stage, 1010, 1013)
		if !store.captured[stage] {
			t.Fatal("stage should be marked captured after retry")
		}
		md, _ := metadata.FromOutgoingContext(qc.lastCtx)
		if got := md.Get(grpctypes.GRPCBlockHeightHeader); len(got) != 1 || got[0] != "1010" {
			t.Fatalf("retry must pin the original target height, got %v", got)
		}
	})
}

// scriptedProofFetcher returns artifacts/errors keyed by the request root hash so
// the early vs final commitments can be distinguished.
type scriptedProofFetcher struct {
	byRoot map[string]struct {
		arts []VerifiedArtifact
		err  error
	}
	byNonce map[string]struct {
		arts []VerifiedArtifact
		err  error
	}
	calls      int
	nonceCalls int
}

func (s *scriptedProofFetcher) FetchAndVerifyProofs(_ context.Context, _ string, req ProofRequest) ([]VerifiedArtifact, error) {
	s.calls++
	r, ok := s.byRoot[string(req.RootHash)]
	if !ok {
		return nil, nil
	}
	return r.arts, r.err
}

func (s *scriptedProofFetcher) FetchAndVerifyProofsByNonce(_ context.Context, _ string, req ProofByNonceRequest) ([]VerifiedArtifact, error) {
	s.nonceCalls++
	r, ok := s.byNonce[string(req.RootHash)]
	if !ok {
		return nil, nil
	}
	return r.arts, r.err
}

func TestDecide(t *testing.T) {
	ctx := context.Background()
	const stage = int64(1000)
	guard := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce, RequireInclusionProof: true, InclusionSampleSize: 1}, newFakeStore())

	finalRoot := []byte{0xaa}
	earlyRoot := []byte{0xee}
	work := participantWork{address: "a", modelId: "m1", count: 100, url: "http://p", rootHash: finalRoot}

	mkFetcher := func(earlyArt, finalArt VerifiedArtifact, earlyErr, finalErr error) *scriptedProofFetcher {
		f := &scriptedProofFetcher{byRoot: map[string]struct {
			arts []VerifiedArtifact
			err  error
		}{}, byNonce: map[string]struct {
			arts []VerifiedArtifact
			err  error
		}{}}
		f.byRoot[string(earlyRoot)] = struct {
			arts []VerifiedArtifact
			err  error
		}{arts: []VerifiedArtifact{earlyArt}, err: earlyErr}
		f.byNonce[string(finalRoot)] = struct {
			arts []VerifiedArtifact
			err  error
		}{arts: []VerifiedArtifact{finalArt}, err: finalErr}
		return f
	}

	inclusionDec := earlyDecision{requireInclusion: true, earlyCount: 10, earlyRoot: earlyRoot, finalCount: 100, earlyShare: 0.1, threshold: 0.05}

	t.Run("matching inclusion and passing share -> pass", func(t *testing.T) {
		art := VerifiedArtifact{LeafIndex: 3, Nonce: 7, VectorB64: "vec"}
		f := mkFetcher(art, art, nil, nil)
		outcome, reason := guard.decide(ctx, f, stage, work, inclusionDec, "pub", "hash")
		if outcome != earlyGuardPass {
			t.Fatalf("expected pass, got %v: %s", outcome, reason)
		}
	})

	t.Run("vector mismatch -> immediate vote no", func(t *testing.T) {
		f := mkFetcher(
			VerifiedArtifact{LeafIndex: 3, Nonce: 7, VectorB64: "early-vec"},
			VerifiedArtifact{LeafIndex: 9, Nonce: 7, VectorB64: "final-vec"},
			nil,
			nil,
		)
		outcome, reason := guard.decide(ctx, f, stage, work, inclusionDec, "pub", "hash")
		if outcome != earlyGuardVoteNo {
			t.Fatal("vector mismatch must vote no")
		}
		if reason == "" {
			t.Fatal("expected mismatch reason")
		}
	})

	t.Run("early proof permanent error -> immediate vote no", func(t *testing.T) {
		art := VerifiedArtifact{LeafIndex: 3, Nonce: 7, VectorB64: "vec"}
		f := mkFetcher(art, art, ErrProofVerificationFailed, nil)
		outcome, _ := guard.decide(ctx, f, stage, work, inclusionDec, "pub", "hash")
		if outcome != earlyGuardVoteNo {
			t.Fatal("permanent early-proof error must vote no")
		}
	})

	t.Run("low share with no inclusion requirement -> vote no via miss streak", func(t *testing.T) {
		f := mkFetcher(VerifiedArtifact{}, VerifiedArtifact{}, nil, nil)
		dec := earlyDecision{shareVoteNo: true, requireInclusion: false, earlyShare: 0.01, threshold: 0.05}
		outcome, reason := guard.decide(ctx, f, stage, work, dec, "pub", "hash")
		if outcome != earlyGuardVoteNo {
			t.Fatal("shareVoteNo should vote no")
		}
		if f.calls != 0 {
			t.Fatalf("no inclusion required, fetcher should not be called; got %d", f.calls)
		}
		if reason == "" {
			t.Fatal("expected a low_early_share reason")
		}
	})

	t.Run("transient early-proof error requests retry", func(t *testing.T) {
		art := VerifiedArtifact{LeafIndex: 3, Nonce: 7, VectorB64: "vec"}
		f := mkFetcher(art, art, context.DeadlineExceeded, nil)
		dec := inclusionDec
		dec.shareVoteNo = false
		outcome, reason := guard.decide(ctx, f, stage, work, dec, "pub", "hash")
		if outcome != earlyGuardRetry {
			t.Fatalf("transient early-proof error must retry, got %v: %s", outcome, reason)
		}
	})

	t.Run("transient final by-nonce error requests retry", func(t *testing.T) {
		art := VerifiedArtifact{LeafIndex: 3, Nonce: 7, VectorB64: "vec"}
		f := mkFetcher(art, art, nil, context.DeadlineExceeded)
		dec := inclusionDec
		dec.shareVoteNo = false
		outcome, reason := guard.decide(ctx, f, stage, work, dec, "pub", "hash")
		if outcome != earlyGuardRetry {
			t.Fatalf("transient by-nonce error must retry, got %v: %s", outcome, reason)
		}
	})
}
