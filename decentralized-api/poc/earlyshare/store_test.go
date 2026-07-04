package earlyshare

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "earlyshare_test.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := NewStore(db)
	if err := s.EnsureSchema(context.Background()); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return s
}

func TestStoreCheckpointRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	cps := []Checkpoint{
		{StageHeight: 10, ParticipantAddress: "a", ModelID: "m1", EarlyCount: 5, EarlyRootHash: []byte{1, 2, 3}, CheckpointBlockHeight: 3, CapturedAtBlockHeight: 4},
		{StageHeight: 10, ParticipantAddress: "b", ModelID: "m1", EarlyCount: 9, EarlyRootHash: []byte{4, 5}, CheckpointBlockHeight: 3, CapturedAtBlockHeight: 4},
	}
	if err := s.UpsertCheckpoints(ctx, cps); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Upsert again updates in place (idempotent capture).
	cps[0].EarlyCount = 6
	if err := s.UpsertCheckpoints(ctx, cps[:1]); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	got, err := s.GetCheckpoints(ctx, 10)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d checkpoints, want 2", len(got))
	}
	byAddr := map[string]Checkpoint{}
	for _, c := range got {
		byAddr[c.ParticipantAddress] = c
	}
	if byAddr["a"].EarlyCount != 6 {
		t.Fatalf("a early_count = %d, want 6", byAddr["a"].EarlyCount)
	}
	if string(byAddr["b"].EarlyRootHash) != string([]byte{4, 5}) {
		t.Fatalf("b root mismatch")
	}
}

func TestStoreCaptureRuns(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	ok, err := s.HasCompletedCapture(ctx, 10)
	if err != nil {
		t.Fatalf("has: %v", err)
	}
	if ok {
		t.Fatal("should not have capture before marking")
	}

	if err := s.MarkStageCaptured(ctx, 10, 3, 4, map[string]int{"m1": 2, "m2": 1}); err != nil {
		t.Fatalf("mark: %v", err)
	}
	ok, err = s.HasCompletedCapture(ctx, 10)
	if err != nil {
		t.Fatalf("has: %v", err)
	}
	if !ok {
		t.Fatal("should have completed capture after marking")
	}

	// Empty capture still records the stage marker.
	if err := s.MarkStageCaptured(ctx, 20, 5, 6, nil); err != nil {
		t.Fatalf("mark empty: %v", err)
	}
	if ok, _ := s.HasCompletedCapture(ctx, 20); !ok {
		t.Fatal("empty capture should still mark stage completed")
	}
}

func TestStoreGuardState(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	_, ok, err := s.GetGuardState(ctx, "a", "m1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ok {
		t.Fatal("no state expected initially")
	}

	st := GuardState{ParticipantAddress: "a", ModelID: "m1", ConsecutiveMisses: 1, UpdatedStageHeight: 42}
	if err := s.UpsertGuardState(ctx, st); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, ok, err := s.GetGuardState(ctx, "a", "m1")
	if err != nil || !ok {
		t.Fatalf("get after upsert: ok=%v err=%v", ok, err)
	}
	if got.ConsecutiveMisses != 1 || got.UpdatedStageHeight != 42 {
		t.Fatalf("state mismatch: %+v", got)
	}
}

func TestStoreDeleteStageKeepsGuardState(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.UpsertCheckpoints(ctx, []Checkpoint{
		{StageHeight: 10, ParticipantAddress: "a", ModelID: "m1", EarlyCount: 5, EarlyRootHash: []byte{1}},
	}); err != nil {
		t.Fatalf("upsert cp: %v", err)
	}
	if err := s.MarkStageCaptured(ctx, 10, 3, 4, map[string]int{"m1": 1}); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := s.UpsertGuardState(ctx, GuardState{ParticipantAddress: "a", ModelID: "m1", ConsecutiveMisses: 1}); err != nil {
		t.Fatalf("upsert state: %v", err)
	}

	if err := s.DeleteStage(ctx, 10); err != nil {
		t.Fatalf("delete: %v", err)
	}

	cps, _ := s.GetCheckpoints(ctx, 10)
	if len(cps) != 0 {
		t.Fatalf("checkpoints not pruned: %d", len(cps))
	}
	if ok, _ := s.HasCompletedCapture(ctx, 10); ok {
		t.Fatal("capture run not pruned")
	}
	// Guard state must survive stage pruning (cross-epoch streak).
	if _, ok, _ := s.GetGuardState(ctx, "a", "m1"); !ok {
		t.Fatal("guard state must persist across stage prune")
	}
}
