package main

import (
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	ids := []uint64{1, 42, 999}
	if err := saveState(path, ids); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	s, err := loadState(path)
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if len(s.EscrowIDs) != len(ids) {
		t.Fatalf("got %d ids, want %d", len(s.EscrowIDs), len(ids))
	}
	for i, id := range ids {
		if s.EscrowIDs[i] != id {
			t.Errorf("ids[%d]: got %d, want %d", i, s.EscrowIDs[i], id)
		}
	}
}

func TestLoadStateMissingFile(t *testing.T) {
	_, err := loadState(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
