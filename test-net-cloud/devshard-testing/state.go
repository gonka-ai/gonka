package main

import (
	"encoding/json"
	"os"
	"time"
)

type testState struct {
	EscrowIDs []uint64  `json:"escrow_ids"`
	CreatedAt time.Time `json:"created_at"`
}

func loadState(path string) (*testState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s testState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveState(path string, ids []uint64) error {
	if ids == nil {
		ids = []uint64{}
	}
	s := testState{EscrowIDs: ids, CreatedAt: time.Now().UTC()}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0644)
}
