package poc

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"decentralized-api/poc/artifacts"
	"decentralized-api/poc/earlyshare"
	grpctypes "github.com/cosmos/cosmos-sdk/types/grpc"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type storedProofFetcher struct {
	early, final           artifacts.ArtifactStore
	earlyDepth, finalDepth uint32
}

func verifyStoredEntries(entries []artifacts.ProofEntry, root []byte, count, depth uint32, decode int64) ([]VerifiedArtifact, error) {
	out := make([]VerifiedArtifact, 0, len(entries))
	for _, e := range entries {
		p := ProofItem{LeafIndex: e.DenseIndex, NonceValue: e.Nonce, VectorBytes: base64.StdEncoding.EncodeToString(e.Vector)}
		for _, b := range e.Proof {
			p.Proof = append(p.Proof, base64.StdEncoding.EncodeToString(b))
		}
		a, err := verifyProofItem(root, count, depth, "p", p, decode)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}
func (f *storedProofFetcher) FetchAndVerifyProofs(_ context.Context, _ string, r ProofRequest) ([]VerifiedArtifact, error) {
	f.earlyDepth = r.TreeDepth
	entries, err := f.early.GetArtifactsAndProofs(r.LeafIndices, r.Count)
	if err != nil {
		return nil, err
	}
	return verifyStoredEntries(entries, r.RootHash, r.Count, r.TreeDepth, r.DecodeSteps)
}
func (f *storedProofFetcher) FetchAndVerifyProofsByNonce(_ context.Context, _ string, r ProofByNonceRequest) ([]VerifiedArtifact, error) {
	f.finalDepth = r.TreeDepth
	entries, err := f.final.GetArtifactsAndProofsByNonce(r.Nonces, r.Count)
	if err != nil {
		return nil, err
	}
	if len(entries) != len(r.Nonces) {
		return nil, ErrNonceAbsent
	}
	return verifyStoredEntries(entries, r.RootHash, r.Count, r.TreeDepth, r.DecodeSteps)
}

func TestEarlyDepthGrowthInclusion(t *testing.T) {
	for _, mode := range []string{"unchanged", "changed", "removed", "wrong early depth", "wrong final depth"} {
		t.Run(mode, func(t *testing.T) {
			early, err := artifacts.OpenSMST(t.TempDir())
			require.NoError(t, err)
			defer early.Close()
			final, err := artifacts.OpenSMST(t.TempDir())
			require.NoError(t, err)
			defer final.Close()
			require.NoError(t, early.AddWithNode(1, []byte{0, 0}, ""))
			require.NoError(t, early.Flush())
			ec, er, ed, err := early.GetFlushedSnapshot()
			require.NoError(t, err)
			if mode != "removed" {
				v := []byte{0, 0}
				if mode == "changed" {
					v = []byte{0, 1}
				}
				require.NoError(t, final.AddWithNode(1, v, ""))
			}
			require.NoError(t, final.AddWithNode(1<<24, []byte{0, 0}, ""))
			require.NoError(t, final.Flush())
			fc, fr, fd, err := final.GetFlushedSnapshot()
			require.NoError(t, err)
			require.Equal(t, uint32(24), ed)
			require.Equal(t, uint32(25), fd)
			dec := earlyDecision{earlyCount: ec, earlyRoot: er, earlyDepth: ed, finalCount: fc, requireInclusion: true}
			work := participantWork{address: "p", modelId: "m", rootHash: fr, count: fc, treeDepth: fd, decodeSteps: 1}
			if mode == "wrong early depth" {
				dec.earlyDepth = 25
			}
			if mode == "wrong final depth" {
				work.treeDepth = 24
			}
			f := &storedProofFetcher{early: early, final: final}
			g := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce, RequireInclusionProof: true}, newFakeStore())
			outcome, reason := g.decide(context.Background(), f, 100, work, dec, "validator", "hash")
			if mode == "unchanged" {
				require.Equal(t, earlyGuardPass, outcome, reason)
				require.Equal(t, uint32(24), f.earlyDepth)
				require.Equal(t, uint32(25), f.finalDepth)
			} else {
				require.Equal(t, earlyGuardVoteNo, outcome, reason)
			}
		})
	}
}

func TestEarlyDepthBackfillCompletedCapture(t *testing.T) {
	ctx := context.Background()
	s := newFakeStore()
	s.captured[100] = true
	cp := earlyshare.Checkpoint{StageHeight: 100, ParticipantAddress: "p", ModelID: "m", EarlyCount: 2, EarlyRootHash: []byte{1}, CheckpointBlockHeight: 110}
	s.checkpoints[100] = []earlyshare.Checkpoint{cp}
	g := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce}, s)
	q := &fakeESQueryClient{resp: &types.QueryAllPoCV2StoreCommitsForStageResponse{Commits: []*types.PoCV2StoreCommitWithAddress{{ParticipantAddress: "p", ModelId: "m", Count: 2, RootHash: []byte{1}, TreeDepth: 24}}}}
	g.MaybeCapture(ctx, q, 100, 999, 1000)
	require.Equal(t, 1, q.calls)
	md, _ := metadata.FromOutgoingContext(q.lastCtx)
	require.Equal(t, []string{"110"}, md.Get(grpctypes.GRPCBlockHeightHeader))
	require.Equal(t, uint32(24), *s.checkpoints[100][0].EarlyTreeDepth)
	require.NoError(t, g.PrepareDepths(ctx, q, 100))
	require.Equal(t, 1, q.calls)
	old := checkpointsDigest([]earlyshare.Checkpoint{cp})
	cp.EarlyTreeDepth = testEarlyDepth(24)
	require.NotEqual(t, old, checkpointsDigest([]earlyshare.Checkpoint{cp}))
}

func TestUnknownEarlyDepthAbstainsAndPreservesStreak(t *testing.T) {
	for _, depth := range []*uint32{nil, testEarlyDepth(0), testEarlyDepth(33)} {
		s := newFakeStore()
		s.captured[100] = true
		s.checkpoints[100] = []earlyshare.Checkpoint{{StageHeight: 100, ParticipantAddress: "p", ModelID: "m", EarlyCount: 2, EarlyRootHash: []byte{1}, EarlyTreeDepth: depth}}
		prev := earlyshare.GuardState{ParticipantAddress: "p", ModelID: "m", ConsecutiveMisses: 2, UpdatedStageHeight: 90}
		s.state["p|m"] = prev
		g := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce, RequireInclusionProof: true}, s)
		ds := g.Evaluate(context.Background(), 100, true, []*types.PoCV2StoreCommitWithAddress{{ParticipantAddress: "p", ModelId: "m", Count: 3}}, map[string]map[string]int64{"m": {"p": 100}}, map[string]bool{"p|m": true})
		require.Equal(t, prev, s.state["p|m"])
		outcome, _ := g.decide(context.Background(), nil, 100, participantWork{}, ds["p|m"], "", "")
		require.Equal(t, earlyGuardAbstainRetry, outcome)
	}
}

type depthBackfillQuery struct{ calls int }

func (q *depthBackfillQuery) AllPoCV2StoreCommitsForStage(context.Context, *types.QueryAllPoCV2StoreCommitsForStageRequest, ...grpc.CallOption) (*types.QueryAllPoCV2StoreCommitsForStageResponse, error) {
	q.calls++
	return nil, context.DeadlineExceeded
}
func TestEarlyDepthBackfillBoundedAndProvenance(t *testing.T) {
	s := newFakeStore()
	s.captured[100] = true
	for i := int64(0); i < 9; i++ {
		s.checkpoints[100] = append(s.checkpoints[100], earlyshare.Checkpoint{StageHeight: 100, ParticipantAddress: string(rune('a' + i)), ModelID: "m", EarlyCount: 2, EarlyRootHash: []byte{1}, CheckpointBlockHeight: 110 + i})
	}
	g := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce}, s)
	q := &depthBackfillQuery{}
	for i := 0; i < 12; i++ {
		before := q.calls
		require.Error(t, g.PrepareDepths(context.Background(), q, 100))
		require.LessOrEqual(t, q.calls-before, 4)
	}
	require.Equal(t, 27, q.calls)

	s = newFakeStore()
	s.captured[100] = true
	s.checkpoints[100] = []earlyshare.Checkpoint{{StageHeight: 100, ParticipantAddress: "p", ModelID: "m", EarlyCount: 2, EarlyRootHash: []byte{1}, CheckpointBlockHeight: 110}}
	g = NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce}, s)
	bad := &fakeESQueryClient{resp: &types.QueryAllPoCV2StoreCommitsForStageResponse{Commits: []*types.PoCV2StoreCommitWithAddress{{ParticipantAddress: "p", ModelId: "m", Count: 3, RootHash: []byte{1}, TreeDepth: 25}}}}
	require.Error(t, g.PrepareDepths(context.Background(), bad, 100))
	require.Nil(t, s.checkpoints[100][0].EarlyTreeDepth)
}

func TestMissedEarlyCapturePreservesValidationBehavior(t *testing.T) {
	for _, mode := range []earlyshare.Mode{earlyshare.ModeObserve, earlyshare.ModeEnforce} {
		t.Run(string(mode), func(t *testing.T) {
			s := newFakeStore()
			g := NewEarlyShareGuard(earlyshare.Config{Mode: mode}, s)
			assigned := map[string]bool{"p|m": true}
			decisions := g.Evaluate(context.Background(), 100, true, []*types.PoCV2StoreCommitWithAddress{{ParticipantAddress: "p", ModelId: "m", Count: 4}}, map[string]map[string]int64{"m": {"p": 100}}, assigned)
			require.Empty(t, decisions)
			require.Empty(t, s.state)
		})
	}
}

func TestEarlyDepthPreparationDoesNotWaitForCapture(t *testing.T) {
	g := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce}, newFakeStore())
	g.captureMu.Lock()
	defer g.captureMu.Unlock()
	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- g.PrepareDepths(ctx, nil, 100) }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("depth preparation blocked on an unrelated capture")
	}
}

type checkpointReadCounter struct {
	*fakeStore
	reads int
}

func (s *checkpointReadCounter) GetCheckpoints(ctx context.Context, stage int64) ([]earlyshare.Checkpoint, error) {
	s.reads++
	return s.fakeStore.GetCheckpoints(ctx, stage)
}
func TestEarlyDepthPreparationCachesReadyStages(t *testing.T) {
	s := &checkpointReadCounter{fakeStore: newFakeStore()}
	s.captured[100] = true
	s.checkpoints[100] = []earlyshare.Checkpoint{{StageHeight: 100, ParticipantAddress: "p", ModelID: "m", EarlyCount: 2, EarlyRootHash: []byte{1}, EarlyTreeDepth: testEarlyDepth(24), CheckpointBlockHeight: 110}}
	g := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce}, s)
	for i := 0; i < 3; i++ {
		require.NoError(t, g.PrepareDepths(context.Background(), nil, 100))
	}
	require.Equal(t, 1, s.reads)
	g.DeleteStage(context.Background(), 100)
	require.Error(t, g.PrepareDepths(context.Background(), nil, 100))
}

type canceledDepthQuery struct{ calls int }

func (q *canceledDepthQuery) AllPoCV2StoreCommitsForStage(ctx context.Context, _ *types.QueryAllPoCV2StoreCommitsForStageRequest, _ ...grpc.CallOption) (*types.QueryAllPoCV2StoreCommitsForStageResponse, error) {
	q.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestEarlyDepthPreparationHonorsTotalDeadline(t *testing.T) {
	s := newFakeStore()
	s.captured[100] = true
	for i := int64(0); i < 4; i++ {
		s.checkpoints[100] = append(s.checkpoints[100], earlyshare.Checkpoint{StageHeight: 100, ParticipantAddress: string(rune('a' + i)), ModelID: "m", EarlyCount: 2, EarlyRootHash: []byte{1}, CheckpointBlockHeight: 110 + i})
	}
	g := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce}, s)
	q := &canceledDepthQuery{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.Error(t, g.PrepareDepths(ctx, q, 100))
	require.Equal(t, 1, q.calls)
	require.False(t, g.depthPrepared[100])
}

type earlyCheckpointReadFailure struct {
	*fakeStore
	captureErr     error
	checkpointsErr error
}

func (s *earlyCheckpointReadFailure) HasCompletedCapture(ctx context.Context, stage int64) (bool, error) {
	if s.captureErr != nil {
		return false, s.captureErr
	}
	return s.fakeStore.HasCompletedCapture(ctx, stage)
}
func (s *earlyCheckpointReadFailure) GetCheckpoints(ctx context.Context, stage int64) ([]earlyshare.Checkpoint, error) {
	if s.checkpointsErr != nil {
		return nil, s.checkpointsErr
	}
	return s.fakeStore.GetCheckpoints(ctx, stage)
}
func TestEarlyGuardReadErrorsSkipStage(t *testing.T) {
	for _, mode := range []earlyshare.Mode{earlyshare.ModeObserve, earlyshare.ModeEnforce} {
		for _, operation := range []string{"capture", "checkpoints"} {
			t.Run(string(mode)+"/"+operation, func(t *testing.T) {
				s := &earlyCheckpointReadFailure{fakeStore: newFakeStore()}
				s.captured[100] = true
				s.state["p|m"] = earlyshare.GuardState{ParticipantAddress: "p", ModelID: "m", ConsecutiveMisses: 2, UpdatedStageHeight: 90}
				before := s.state["p|m"]
				if operation == "capture" {
					s.captureErr = fmt.Errorf("database is locked")
				} else {
					s.checkpointsErr = fmt.Errorf("database is locked")
				}
				g := NewEarlyShareGuard(earlyshare.Config{Mode: mode}, s)
				decisions := g.Evaluate(context.Background(), 100, true, []*types.PoCV2StoreCommitWithAddress{{ParticipantAddress: "p", ModelId: "m", Count: 4}}, map[string]map[string]int64{"m": {"p": 100}}, map[string]bool{"p|m": true})
				require.Empty(t, decisions)
				require.Equal(t, before, s.state["p|m"])
			})
		}
	}
}

func TestExhaustedBackfillDoesNotApproveLegacyCheckpoint(t *testing.T) {
	s := newFakeStore()
	s.captured[100] = true
	s.checkpoints[100] = []earlyshare.Checkpoint{{StageHeight: 100, ParticipantAddress: "p", ModelID: "m", EarlyCount: 2, EarlyRootHash: []byte{1}, CheckpointBlockHeight: 110}}
	prev := earlyshare.GuardState{ParticipantAddress: "p", ModelID: "m", ConsecutiveMisses: 2, UpdatedStageHeight: 90}
	s.state["p|m"] = prev
	g := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce, RequireInclusionProof: true}, s)
	q := &depthBackfillQuery{}
	for i := 0; i < 4; i++ {
		require.Error(t, g.PrepareDepths(context.Background(), q, 100))
	}
	require.Equal(t, 3, q.calls)
	decisions := g.Evaluate(context.Background(), 100, true, []*types.PoCV2StoreCommitWithAddress{{ParticipantAddress: "p", ModelId: "m", Count: 3}}, map[string]map[string]int64{"m": {"p": 100}}, map[string]bool{"p|m": true})
	outcome, _ := g.decide(context.Background(), nil, 100, participantWork{}, decisions["p|m"], "", "")
	require.Equal(t, earlyGuardAbstainRetry, outcome)
	require.Equal(t, prev, s.state["p|m"])
	require.Equal(t, 3, q.calls)
}

func TestEarlyDepthOnlyRequiredForInclusion(t *testing.T) {
	cases := []struct {
		name        string
		votingPower bool
		root        []byte
		count       uint32
		inclusion   bool
		decision    bool
		voteNo      bool
		misses      int
	}{
		{"no voting power", false, []byte{1}, 2, true, false, false, 1},
		{"empty early root", true, nil, 2, true, false, false, 1},
		{"early count exceeds final", true, []byte{1}, 5, true, true, true, 2},
		{"inclusion disabled", true, []byte{1}, 2, false, true, false, 0},
		{"empty early count", true, []byte{1}, 0, true, true, true, 2},
	}
	for _, tc := range cases {
		for _, depth := range []*uint32{nil, testEarlyDepth(0), testEarlyDepth(33), testEarlyDepth(24)} {
			label := "unknown"
			if depth != nil {
				label = fmt.Sprint(*depth)
			}
			t.Run(tc.name+"/"+label, func(t *testing.T) {
				s := newFakeStore()
				s.captured[100] = true
				s.checkpoints[100] = []earlyshare.Checkpoint{
					{StageHeight: 100, ParticipantAddress: "p", ModelID: "m", EarlyCount: tc.count, EarlyRootHash: tc.root, EarlyTreeDepth: depth},
					{StageHeight: 100, ParticipantAddress: "q", ModelID: "m", EarlyCount: 4, EarlyRootHash: []byte{2}, EarlyTreeDepth: testEarlyDepth(24)},
				}
				s.state["p|m"] = earlyshare.GuardState{ParticipantAddress: "p", ModelID: "m", ConsecutiveMisses: 1, UpdatedStageHeight: 90}
				g := NewEarlyShareGuard(earlyshare.Config{Mode: earlyshare.ModeEnforce, RequireInclusionProof: tc.inclusion}, s)
				powers := map[string]map[string]int64{}
				if tc.votingPower {
					powers["m"] = map[string]int64{"p": 10, "q": 90}
				}
				decisions := g.Evaluate(context.Background(), 100, true, []*types.PoCV2StoreCommitWithAddress{{ParticipantAddress: "p", ModelId: "m", Count: 4}, {ParticipantAddress: "q", ModelId: "m", Count: 4}}, powers, map[string]bool{"p|m": true})
				dec, ok := decisions["p|m"]
				require.Equal(t, tc.decision, ok)
				require.Empty(t, dec.metadataError)
				require.False(t, dec.requireInclusion)
				require.Equal(t, tc.voteNo, dec.shareVoteNo)
				require.Equal(t, tc.misses, s.state["p|m"].ConsecutiveMisses)
				if ok {
					outcome, _ := g.decide(context.Background(), nil, 100, participantWork{}, dec, "", "")
					if tc.voteNo {
						require.Equal(t, earlyGuardVoteNo, outcome)
					} else {
						require.Equal(t, earlyGuardPass, outcome)
					}
				}
			})
		}
	}
}
